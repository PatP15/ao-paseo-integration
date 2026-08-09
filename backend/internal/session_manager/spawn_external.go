// Spawn's remote path and the execution-backend seam. Kept in-package because
// it reuses spawn's unexported invariants — seedRecord, buildPrompt,
// rollbackSpawnSeedRow, markSpawnFailedTerminated — rather than reimplementing
// them in a sibling package that would have to be kept behaviourally identical
// across upstream's churn in manager.go.
package sessionmanager

import (
	"context"
	"errors"
	"fmt"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/execpolicy"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// externalSpawner routes a spawn to an execution host instead of a local
// worktree and a local runtime. It is the whole seam: Session Manager asks
// whether a spawn belongs to a backend and, if so, hands it the committed
// session id. Host selection, trust zones, project bindings, and the outbox
// stay behind this interface, so nothing here learns what a host is.
//
// Unexported interface behind an exported Deps field, matching the file's
// existing runtimeController / lifecycleRecorder idiom: an implementation in
// another package satisfies it structurally.
type externalSpawner interface {
	// PlaceSpawn resolves the execution host for a spawn. ok=false keeps the
	// spawn on the local path, byte-for-byte as before — which is also what a
	// nil Deps.ExternalSpawner does for every spawn.
	PlaceSpawn(ctx context.Context, cfg ports.SpawnConfig) (placement ExternalPlacement, ok bool, err error)

	// EnqueueSpawn commits the durable execution binding and the start command
	// for a session row that already exists. Nothing remote has happened when
	// it returns: the outbox worker delivers, so a crash in between replays
	// rather than dropping or duplicating the launch.
	EnqueueSpawn(ctx context.Context, req ExternalSpawnRequest) error
}

// ExternalPlacement names the execution backend and host a spawn was routed to.
type ExternalPlacement struct {
	BackendType domain.ExecutionBackendType
	HostID      domain.ExecutionHostID
}

// ExternalSpawnRequest is AO's committed description of one remote spawn. Every
// field is already durable in the session row when EnqueueSpawn is called, so
// an implementation never has to re-derive them and a replay sees the same
// values.
type ExternalSpawnRequest struct {
	SessionID domain.SessionID
	Placement ExternalPlacement
	// Config is the original spawn request, for the project, kind, harness, and
	// issue an implementation needs to build its payload.
	Config ports.SpawnConfig
	// Branch is the resolved branch name — never empty, and the same value
	// written to Metadata.Branch. It is what makes observe/scm discover this
	// session's PRs, so AO's existing CI and review machinery works on a remote
	// session with no further change.
	Branch string
	// Prompt is the work prompt as persisted in Metadata.Prompt. It carries no
	// system prompt: AO's standing instructions are written to a local file that
	// the local argv references, and a remote launch has neither.
	Prompt string
}

// spawnExternal is Spawn's remote path: no worktree, no agent adapter, no argv,
// no local runtime. It creates the same seed row a local spawn does, publishes
// the branch and prompt through the LCM, and then hands the session to the
// backend's outbox.
//
// Metadata.WorkspacePath stays "" permanently — the worktree belongs to the
// host — which is exactly the state execpolicy fences the local-only features
// against. Metadata.RuntimeHandleID also stays empty here: it is minted only
// when the outbox worker has a real remote agent id to put in it, so a handle's
// presence keeps meaning "an agent exists on a host".
//
// Order is deliberate. MarkSpawned commits BEFORE the enqueue, so a crash
// between the two leaves an idle session with no command — visible, killable,
// and with nothing running anywhere — rather than a committed command that will
// launch an agent for a session AO has already given up on.
//
// The signature mirrors Spawn's so the branch stays a tail call, which is why
// the system-prompt byte count is a return value that is always zero.
//
//nolint:unparam // a remote launch carries no system prompt; see above.
func (m *Manager) spawnExternal(ctx context.Context, cfg ports.SpawnConfig, project domain.ProjectRecord, placement ExternalPlacement) (domain.SessionRecord, int, int, error) {
	if placement.HostID == "" || placement.BackendType == "" || placement.BackendType == domain.ExecutionBackendLocal {
		return domain.SessionRecord{}, 0, 0, fmt.Errorf("spawn: external placement %q/%q is not a remote host", placement.BackendType, placement.HostID)
	}
	// Attachments are written into the session worktree and referenced by path
	// in the prompt. A remote agent's worktree is on the host and AO cannot put
	// files in it, so the references would resolve to nothing. Refuse rather
	// than launch an agent pointed at files that do not exist.
	if len(cfg.Attachments) > 0 {
		return domain.SessionRecord{}, 0, 0, fmt.Errorf("spawn: attachments are written into the session worktree, which the execution host owns: %w", execpolicy.ErrRemoteUnsupported)
	}

	// Only the user prompt, deliberately: buildSpawnTexts' other half is the
	// system prompt, and delivering it needs the local system-prompt file the
	// launch argv points at. Reporting 0 system-prompt bytes is the honest
	// answer for a launch that carries none.
	prompt := buildPrompt(cfg)

	rec, err := m.store.CreateSession(ctx, seedRecord(cfg, m.clock()))
	if err != nil {
		return domain.SessionRecord{}, 0, 0, fmt.Errorf("spawn: create: %w", err)
	}
	id := rec.ID

	branch := cfg.Branch
	if branch == "" {
		branch = DefaultSpawnBranch(id, cfg.Kind, sessionPrefix(project), project.Kind.WithDefault(), m.dataDir)
	}
	if err := m.lcm.MarkSpawned(ctx, id, domain.SessionMetadata{Branch: branch, Prompt: prompt}); err != nil {
		// Nothing has been enqueued and nothing remote exists, so this is still
		// a spawn that never started.
		m.rollbackSpawnSeedRow(ctx, id)
		return domain.SessionRecord{}, 0, 0, fmt.Errorf("spawn %s: completed: %w", id, err)
	}
	if err := m.external.EnqueueSpawn(ctx, ExternalSpawnRequest{
		SessionID: id,
		Placement: placement,
		Config:    cfg,
		Branch:    branch,
		Prompt:    prompt,
	}); err != nil {
		// The row now carries a prompt, so it is past seed state and cannot be
		// deleted; park it terminated so it never reads as a live session
		// waiting on a host that was never told about it.
		m.markSpawnFailedTerminated(ctx, id)
		return domain.SessionRecord{}, 0, 0, fmt.Errorf("spawn %s: enqueue: %w", id, err)
	}

	rec, err = m.getRecord(ctx, id)
	if err != nil {
		return domain.SessionRecord{}, 0, 0, err
	}
	return rec, len(prompt), 0, nil
}

// remoteSession reports whether a session's agent executes on a remote host.
//
// The durable execution binding is consulted as well as the runtime handle
// because the window that matters opens before launch: a session that has been
// dispatched but not yet acknowledged has a binding, no handle, and an empty
// WorkspacePath — indistinguishable, by handle alone, from a local spawn that
// failed partway. m.store is passed as the binding source under execpolicy's
// optional-interface rule: production's *sqlite.Store implements it, and a
// store that does not reduces this to the handle check, which is complete for
// any session that has launched.
func (m *Manager) remoteSession(ctx context.Context, rec domain.SessionRecord) (bool, error) {
	return execpolicy.Remote(ctx, m.store, rec.ID, rec.Metadata)
}

// stopSessionRuntime is Kill's runtime teardown. A local failure is returned so
// Kill refuses to declare a session dead while its pane may still be running on
// this machine. A remote failure is logged and swallowed: it usually means the
// host is unreachable, and "a host is offline" must not become "this session
// cannot be terminated" — the operator would be locked out for the length of
// the outage, and the agent's process and worktree are the host's to reclaim
// anyway. reconcileReap stops the agent when the host comes back.
func (m *Manager) stopSessionRuntime(ctx context.Context, rec domain.SessionRecord, handle ports.RuntimeHandle) error {
	err := m.runtime.Destroy(ctx, handle)
	if err == nil {
		return nil
	}
	remote, remoteErr := m.remoteSession(ctx, rec)
	if remoteErr != nil {
		return errors.Join(err, remoteErr)
	}
	if !remote {
		return err
	}
	m.logger.Warn("kill: remote agent stop failed; terminating anyway", "sessionID", rec.ID, "error", err)
	return nil
}

// incompleteHandleOrRemote names the reason restore and resume-agent cannot run.
//
// Both rebuild a local worktree and relaunch a local argv, so both are
// documented non-goals for a remote session — but ErrIncompleteHandle is the
// wrong story to tell about one. Nothing is incomplete: the worktree belongs to
// the host and WorkspacePath is empty by design. Reporting a broken spawn
// invites a retry that can never succeed, so a remote session gets the refusal
// sentinel instead.
func (m *Manager) incompleteHandleOrRemote(ctx context.Context, rec domain.SessionRecord, operation string) error {
	remote, err := m.remoteSession(ctx, rec)
	if err != nil {
		return fmt.Errorf("%s %s: %w", operation, rec.ID, err)
	}
	if remote {
		return execpolicy.Refuse(operation, rec.ID)
	}
	return fmt.Errorf("%s %s: %w", operation, rec.ID, ErrIncompleteHandle)
}

// skipRemoteTeardown is the shutdown decision: daemon shutdown does NOT stop
// remote agents.
//
// SaveAndTeardownAll exists because a local agent dies with its tmux and its
// uncommitted work would go with the worktree. Neither is true on an execution
// host, where the process and the worktree outlive this daemon by design.
// Stopping them at shutdown would destroy in-flight work on every restart —
// including every auto-update — in exchange for nothing, since the session is
// re-observed on the next boot from its stored ids. The skip already happened
// incidentally through the WorkspacePath test; stating it here makes it survive
// a change to that test.
//
// A binding lookup that fails also skips: refusing to tear down a session AO
// cannot classify is the safe direction, and the error is already logged.
func (m *Manager) skipRemoteTeardown(ctx context.Context, rec domain.SessionRecord) bool {
	remote, err := m.remoteSession(ctx, rec)
	if err != nil {
		m.logger.Error("save-teardown-all: execution binding lookup failed, skipping", "sessionID", rec.ID, "error", err)
		return true
	}
	return remote
}

// adoptRemoteOnBoot reports whether boot reconciliation should leave a session
// exactly as it found it.
//
// A remote agent's process and worktree live on another host and survive this
// daemon entirely, so AO knowing nothing about them at boot is the expected
// case, not evidence of death: there is no local work to capture, no local
// runtime to reap, and a single probe could not tell a finished agent from a
// working one anyway — Paseo's `idle` conflates finished, never started, and
// awaiting prompt. The polling observer owns remote liveness.
//
// Keyed on the handle alone, so boot needs no extra read and gains no new
// failure mode. A dispatched-but-unlaunched session has no handle yet and is
// adopted by reconcileLive's local early return immediately below this call.
func (m *Manager) adoptRemoteOnBoot(rec domain.SessionRecord) bool {
	return execpolicy.IsRemoteHandle(rec.Metadata.RuntimeHandleID)
}

// cleanupExternal reclaims what a terminated remote session still holds — the
// agent occupying a slot on its host — and records the outcome in result.
// handled=false means the session is local and the caller should continue with
// its existing handling.
//
// Before this, Cleanup's empty-workspace `continue` fired ahead of the runtime
// Destroy, so a remote agent was never stopped and its session appeared in
// neither Cleaned nor Skipped: cleanup silently did nothing and said nothing.
//
// A stop that fails is reported as a skip rather than swallowed. Cleanup is
// retryable by design and an unreachable host is the common case, so the
// operator should see it and run cleanup again rather than read a success over
// an agent still holding capacity.
func (m *Manager) cleanupExternal(ctx context.Context, rec domain.SessionRecord, result *CleanupResult) (handled bool, err error) {
	remote, err := m.remoteSession(ctx, rec)
	if err != nil || !remote {
		return false, err
	}
	// An empty handle means the session was dispatched but never acknowledged,
	// so no agent was ever bound to it and there is nothing on the host to stop.
	if handle := runtimeHandle(rec.Metadata); handle.ID != "" {
		if stopErr := m.runtime.Destroy(ctx, handle); stopErr != nil {
			m.logger.Warn("cleanup: remote agent stop failed", "sessionID", rec.ID, "error", stopErr)
			result.Skipped = append(result.Skipped, CleanupSkip{
				SessionID: rec.ID, Reason: "remote agent could not be stopped",
			})
			return true, nil
		}
	}
	m.cleanupSystemPromptDir(rec.ID)
	result.Cleaned = append(result.Cleaned, rec.ID)
	return true, nil
}
