package sessionmanager

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/execpolicy"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const testRemoteHandle = "paseo:host-1/agent-1"

type fakeExternalSpawner struct {
	placement  ExternalPlacement
	place      bool
	placeErr   error
	placed     int
	enqueued   []ExternalSpawnRequest
	enqueueErr error
}

func (f *fakeExternalSpawner) PlaceSpawn(context.Context, ports.SpawnConfig) (ExternalPlacement, bool, error) {
	f.placed++
	if f.placeErr != nil {
		return ExternalPlacement{}, false, f.placeErr
	}
	return f.placement, f.place, nil
}

func (f *fakeExternalSpawner) EnqueueSpawn(_ context.Context, req ExternalSpawnRequest) error {
	f.enqueued = append(f.enqueued, req)
	return f.enqueueErr
}

func newRemoteSpawner() *fakeExternalSpawner {
	return &fakeExternalSpawner{
		placement: ExternalPlacement{BackendType: domain.ExecutionBackendPaseo, HostID: "host-1"},
		place:     true,
	}
}

// bindingStore adds the durable execution binding to the package's fake store,
// the way *sqlite.Store carries it in production. It exists so tests can cover
// the window that a runtime handle cannot describe: a session dispatched to a
// host but not yet acknowledged, which has a binding, no handle, and an empty
// WorkspacePath.
type bindingStore struct {
	*fakeStore
	bindings map[domain.SessionID]domain.SessionExecutionBinding
	err      error
}

func (b *bindingStore) GetSessionExecutionBinding(_ context.Context, id domain.SessionID) (domain.SessionExecutionBinding, bool, error) {
	if b.err != nil {
		return domain.SessionExecutionBinding{}, false, b.err
	}
	binding, ok := b.bindings[id]
	return binding, ok, nil
}

func newManagerWithSpawner(external externalSpawner) (*Manager, *fakeStore, *fakeRuntime, *fakeWorkspace, *fakeLCM) {
	st := newFakeStore()
	st.projects["mer"] = domain.ProjectRecord{ID: "mer", Config: testRoleAgents()}
	rt := &fakeRuntime{}
	ws := &fakeWorkspace{}
	lcm := &fakeLCM{store: st}
	m := New(Deps{
		Runtime: rt, Agents: fakeAgents{}, Workspace: ws, Store: st, Messenger: &fakeMessenger{},
		Lifecycle: lcm, ExternalSpawner: external,
		LookPath: func(string) (string, error) { return "/bin/true", nil },
	})
	return m, st, rt, ws, lcm
}

// seedRemoteSession writes a session in the shape a remote spawn leaves behind:
// a branch, a prompt, no local workspace, and a namespaced runtime handle.
func seedRemoteSession(st *fakeStore, id domain.SessionID, terminated bool) domain.SessionRecord {
	rec := domain.SessionRecord{
		ID: id, ProjectID: "mer", Kind: domain.KindWorker, Harness: domain.HarnessClaudeCode,
		IsTerminated: terminated,
		Metadata: domain.SessionMetadata{
			Branch: "ao/mer-1", Prompt: "ship it", RuntimeHandleID: testRemoteHandle,
		},
		Activity:  domain.Activity{State: domain.ActivityIdle, LastActivityAt: time.Now()},
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	st.sessions[id] = rec
	return rec
}

func TestSpawnRoutesAPlacedSessionToTheExecutionBackend(t *testing.T) {
	external := newRemoteSpawner()
	m, _, rt, ws, lcm := newManagerWithSpawner(external)

	rec, promptBytes, systemPromptBytes, err := m.Spawn(context.Background(), ports.SpawnConfig{
		ProjectID: "mer", Kind: domain.KindWorker, Prompt: "ship it",
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	// Nothing local was built: no worktree, no PTY, no launch flight.
	if ws.lastCfg.SessionID != "" || rt.created != 0 {
		t.Fatalf("local resources created: workspace cfg = %#v, runtimes = %d", ws.lastCfg, rt.created)
	}
	if len(lcm.prepared) != 0 {
		t.Fatalf("PrepareLaunch called for a remote spawn: %v", lcm.prepared)
	}
	// WorkspacePath stays empty (the worktree is the host's) and Branch is set,
	// which is what makes observe/scm discover this session's PRs.
	if rec.Metadata.WorkspacePath != "" {
		t.Errorf("WorkspacePath = %q, want empty", rec.Metadata.WorkspacePath)
	}
	if rec.Metadata.Branch == "" {
		t.Error("Branch is empty; observe/scm would never find this session's PRs")
	}
	if rec.Metadata.RuntimeHandleID != "" {
		t.Errorf("RuntimeHandleID = %q, want empty until the outbox binds a real agent", rec.Metadata.RuntimeHandleID)
	}
	if rec.Metadata.Prompt != "ship it" {
		t.Errorf("Prompt = %q", rec.Metadata.Prompt)
	}
	if promptBytes != len("ship it") || systemPromptBytes != 0 {
		t.Errorf("bytes = (%d, %d), want (%d, 0): a remote launch carries no system prompt", promptBytes, systemPromptBytes, len("ship it"))
	}

	if len(external.enqueued) != 1 {
		t.Fatalf("enqueued %d spawns, want 1", len(external.enqueued))
	}
	req := external.enqueued[0]
	if req.SessionID != rec.ID || req.Branch != rec.Metadata.Branch || req.Prompt != "ship it" {
		t.Errorf("enqueued request = %#v, want it to match the committed row", req)
	}
	if req.Placement != external.placement {
		t.Errorf("placement = %#v, want %#v", req.Placement, external.placement)
	}
}

func TestSpawnStaysLocalWhenTheSpawnerDeclines(t *testing.T) {
	external := &fakeExternalSpawner{} // place=false: not ours
	m, _, rt, ws, _ := newManagerWithSpawner(external)

	rec, _, _, err := m.Spawn(context.Background(), ports.SpawnConfig{ProjectID: "mer", Kind: domain.KindWorker})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if external.placed != 1 || len(external.enqueued) != 0 {
		t.Fatalf("placed=%d enqueued=%d, want 1 and 0", external.placed, len(external.enqueued))
	}
	if rt.created != 1 || ws.lastCfg.SessionID != rec.ID {
		t.Fatalf("local spawn did not run: runtimes=%d workspace=%#v", rt.created, ws.lastCfg)
	}
	if rec.Metadata.WorkspacePath == "" || rec.Metadata.RuntimeHandleID == "" {
		t.Errorf("local metadata = %#v, want workspace path and runtime handle", rec.Metadata)
	}
}

func TestSpawnExternalParksTheSessionWhenTheEnqueueFails(t *testing.T) {
	external := newRemoteSpawner()
	external.enqueueErr = errors.New("outbox unavailable")
	m, st, _, _, lcm := newManagerWithSpawner(external)

	if _, _, _, err := m.Spawn(context.Background(), ports.SpawnConfig{
		ProjectID: "mer", Kind: domain.KindWorker, Prompt: "ship it",
	}); err == nil {
		t.Fatal("Spawn succeeded despite an enqueue failure")
	}
	if len(st.sessions) != 1 {
		t.Fatalf("sessions = %d, want the row kept for inspection", len(st.sessions))
	}
	for id, rec := range st.sessions {
		if !rec.IsTerminated {
			t.Errorf("session %s is live but no host was ever told about it", id)
		}
		if lcm.terminated[id] != 1 {
			t.Errorf("MarkTerminated calls for %s = %d, want 1", id, lcm.terminated[id])
		}
	}
}

func TestSpawnExternalRefusesAttachments(t *testing.T) {
	external := newRemoteSpawner()
	m, st, _, _, _ := newManagerWithSpawner(external)

	_, _, _, err := m.Spawn(context.Background(), ports.SpawnConfig{
		ProjectID: "mer", Kind: domain.KindWorker, Prompt: "look at this",
		Attachments: []ports.SpawnAttachment{{Ext: ".png", Data: []byte("x")}},
	})
	if !errors.Is(err, execpolicy.ErrRemoteUnsupported) {
		t.Fatalf("err = %v, want it to carry execpolicy.ErrRemoteUnsupported", err)
	}
	if len(st.sessions) != 0 || len(external.enqueued) != 0 {
		t.Errorf("refusal left state behind: %d sessions, %d enqueued", len(st.sessions), len(external.enqueued))
	}
}

func TestKillTerminatesARemoteSessionWhenTheHostIsUnreachable(t *testing.T) {
	m, st, rt, _, lcm := newManagerWithSpawner(nil)
	seedRemoteSession(st, "mer-1", false)
	rt.destroyErr = errors.New("dial host-1: connection refused")

	freed, err := m.Kill(context.Background(), "mer-1")
	if err != nil {
		t.Fatalf("Kill: %v — an offline host must not block terminating the session", err)
	}
	if freed {
		t.Error("freed = true, want false: no local workspace was reclaimed")
	}
	if lcm.terminated["mer-1"] != 1 {
		t.Errorf("MarkTerminated calls = %d, want 1", lcm.terminated["mer-1"])
	}
	if !st.sessions["mer-1"].IsTerminated {
		t.Error("session is not terminated")
	}
}

func TestKillStillFailsWhenALocalRuntimeCannotBeDestroyed(t *testing.T) {
	m, st, rt, _, lcm := newManagerWithSpawner(nil)
	st.sessions["mer-1"] = domain.SessionRecord{
		ID: "mer-1", ProjectID: "mer", Harness: domain.HarnessClaudeCode,
		Metadata: domain.SessionMetadata{Branch: "b", WorkspacePath: "/ws/mer-1", RuntimeHandleID: "h1"},
	}
	rt.destroyErr = errors.New("tmux kill-session failed")

	if _, err := m.Kill(context.Background(), "mer-1"); err == nil {
		t.Fatal("Kill succeeded despite a local runtime that may still be alive")
	}
	if lcm.terminated["mer-1"] != 0 {
		t.Errorf("MarkTerminated calls = %d, want 0", lcm.terminated["mer-1"])
	}
}

func TestRestoreAndResumeRefuseRemoteSessions(t *testing.T) {
	m, st, _, _, _ := newManagerWithSpawner(nil)
	seedRemoteSession(st, "mer-1", true)

	_, err := m.RestoreWithMode(context.Background(), "mer-1")
	if !errors.Is(err, execpolicy.ErrRemoteUnsupported) {
		t.Errorf("restore err = %v, want execpolicy.ErrRemoteUnsupported", err)
	}
	if errors.Is(err, ErrIncompleteHandle) {
		t.Error("restore reported an incomplete handle; nothing is incomplete, the worktree is the host's")
	}

	exited := seedRemoteSession(st, "mer-2", false)
	exited.Activity = domain.Activity{State: domain.ActivityExited}
	st.sessions["mer-2"] = exited
	if _, err := m.ResumeAgentWithMode(context.Background(), "mer-2"); !errors.Is(err, execpolicy.ErrRemoteUnsupported) {
		t.Errorf("resume err = %v, want execpolicy.ErrRemoteUnsupported", err)
	}
}

func TestRestoreStillReportsAnIncompleteHandleForALocalSession(t *testing.T) {
	m, st, _, _, _ := newManagerWithSpawner(nil)
	st.sessions["mer-1"] = domain.SessionRecord{
		ID: "mer-1", ProjectID: "mer", Harness: domain.HarnessClaudeCode, IsTerminated: true,
	}
	if _, err := m.RestoreWithMode(context.Background(), "mer-1"); !errors.Is(err, ErrIncompleteHandle) {
		t.Errorf("err = %v, want ErrIncompleteHandle", err)
	}
}

func TestCleanupStopsTheRemoteAgentAndReportsIt(t *testing.T) {
	m, st, rt, _, _ := newManagerWithSpawner(nil)
	seedRemoteSession(st, "mer-1", true)

	result, err := m.Cleanup(context.Background(), "mer")
	if err != nil {
		t.Fatalf("Cleanup: %v", err)
	}
	if len(result.Cleaned) != 1 || result.Cleaned[0] != "mer-1" {
		t.Fatalf("Cleaned = %v, want [mer-1]", result.Cleaned)
	}
	if len(rt.destroyedIDs) != 1 || rt.destroyedIDs[0] != testRemoteHandle {
		t.Errorf("destroyed = %v, want the remote agent stopped once", rt.destroyedIDs)
	}
}

func TestCleanupReportsARemoteAgentItCouldNotStop(t *testing.T) {
	m, st, rt, _, _ := newManagerWithSpawner(nil)
	seedRemoteSession(st, "mer-1", true)
	rt.destroyErr = errors.New("dial host-1: connection refused")

	result, err := m.Cleanup(context.Background(), "mer")
	if err != nil {
		t.Fatalf("Cleanup: %v", err)
	}
	if len(result.Cleaned) != 0 {
		t.Errorf("Cleaned = %v, want none", result.Cleaned)
	}
	if len(result.Skipped) != 1 || result.Skipped[0].SessionID != "mer-1" {
		t.Fatalf("Skipped = %#v, want the failure visible and retryable", result.Skipped)
	}
}

func TestCleanupLeavesAPathlessLocalSessionAlone(t *testing.T) {
	m, st, rt, _, _ := newManagerWithSpawner(nil)
	st.sessions["mer-1"] = domain.SessionRecord{
		ID: "mer-1", ProjectID: "mer", Harness: domain.HarnessClaudeCode, IsTerminated: true,
		Metadata: domain.SessionMetadata{RuntimeHandleID: "h1"},
	}

	result, err := m.Cleanup(context.Background(), "mer")
	if err != nil {
		t.Fatalf("Cleanup: %v", err)
	}
	if len(result.Cleaned) != 0 || len(result.Skipped) != 0 || rt.destroyed != 0 {
		t.Errorf("local behaviour changed: cleaned=%v skipped=%#v destroys=%d", result.Cleaned, result.Skipped, rt.destroyed)
	}
}

func TestCleanupTreatsADispatchedSessionWithNoHandleAsRemote(t *testing.T) {
	inner := newFakeStore()
	inner.projects["mer"] = domain.ProjectRecord{ID: "mer", Config: testRoleAgents()}
	st := &bindingStore{
		fakeStore: inner,
		bindings: map[domain.SessionID]domain.SessionExecutionBinding{
			"mer-1": {SessionID: "mer-1", BackendType: domain.ExecutionBackendPaseo, HostID: "host-1"},
		},
	}
	inner.sessions["mer-1"] = domain.SessionRecord{
		ID: "mer-1", ProjectID: "mer", Harness: domain.HarnessClaudeCode, IsTerminated: true,
		Metadata: domain.SessionMetadata{Branch: "ao/mer-1", Prompt: "ship it"},
	}
	rt := &fakeRuntime{}
	m := New(Deps{
		Runtime: rt, Agents: fakeAgents{}, Workspace: &fakeWorkspace{}, Store: st,
		Messenger: &fakeMessenger{}, Lifecycle: &fakeLCM{store: inner},
	})

	result, err := m.Cleanup(context.Background(), "mer")
	if err != nil {
		t.Fatalf("Cleanup: %v", err)
	}
	// No agent was ever bound, so there is nothing to stop — but the session is
	// still recognised as remote and reported rather than silently dropped.
	if len(result.Cleaned) != 1 || rt.destroyed != 0 {
		t.Errorf("Cleaned = %v, destroys = %d; want the session reported and no stop attempted", result.Cleaned, rt.destroyed)
	}
}

func TestSaveAndTeardownAllLeavesRemoteSessionsRunning(t *testing.T) {
	m, st, rt, ws, lcm := newManagerWithSpawner(nil)
	rec := seedRemoteSession(st, "mer-1", false)
	// A local workspace path cannot happen for a real remote session; it is set
	// here so the skip can only come from the remote branch, not from the
	// WorkspacePath test underneath it.
	rec.Metadata.WorkspacePath = "/ws/mer-1"
	st.sessions["mer-1"] = rec

	if err := m.SaveAndTeardownAll(context.Background()); err != nil {
		t.Fatalf("SaveAndTeardownAll: %v", err)
	}
	if lcm.terminated["mer-1"] != 0 || st.sessions["mer-1"].IsTerminated {
		t.Error("shutdown terminated a remote session whose agent outlives this daemon")
	}
	if ws.stashCalls != 0 || len(st.worktrees["mer-1"]) != 0 || rt.destroyed != 0 {
		t.Errorf("shutdown tore down remote state: stashes=%d markers=%d destroys=%d",
			ws.stashCalls, len(st.worktrees["mer-1"]), rt.destroyed)
	}
}

func TestSaveAndTeardownAllStillTearsDownLocalSessions(t *testing.T) {
	m, st, _, ws, lcm := newManagerWithSpawner(nil)
	st.sessions["mer-1"] = domain.SessionRecord{
		ID: "mer-1", ProjectID: "mer", Harness: domain.HarnessClaudeCode,
		Metadata: domain.SessionMetadata{Branch: "b", WorkspacePath: "/ws/mer-1", RuntimeHandleID: "h1"},
	}

	if err := m.SaveAndTeardownAll(context.Background()); err != nil {
		t.Fatalf("SaveAndTeardownAll: %v", err)
	}
	if lcm.terminated["mer-1"] != 1 || ws.stashCalls != 1 || len(st.worktrees["mer-1"]) != 1 {
		t.Errorf("local shutdown changed: terminated=%d stashes=%d markers=%d",
			lcm.terminated["mer-1"], ws.stashCalls, len(st.worktrees["mer-1"]))
	}
}

func TestReconcileLiveAdoptsARemoteSessionWithoutProbingIt(t *testing.T) {
	m, st, rt, ws, lcm := newManagerWithSpawner(nil)
	rec := seedRemoteSession(st, "mer-1", false)
	// Same isolation as the shutdown test: the workspace path is what the local
	// early return keys on, so setting it proves the remote branch above it is
	// what adopts the session. rt reports (false, nil) for any unknown handle —
	// the ambiguous "empty result" a remote CLI produces for a malformed label,
	// an archived agent, or a host that fell through to the wrong daemon.
	rec.Metadata.WorkspacePath = "/ws/mer-1"
	st.sessions["mer-1"] = rec

	if err := m.reconcileLive(context.Background(), st.sessions["mer-1"]); err != nil {
		t.Fatalf("reconcileLive: %v", err)
	}
	if lcm.terminated["mer-1"] != 0 || ws.stashCalls != 0 || rt.destroyed != 0 {
		t.Errorf("boot reconciliation killed a remote session: terminated=%d stashes=%d destroys=%d",
			lcm.terminated["mer-1"], ws.stashCalls, rt.destroyed)
	}
}

func TestReconcileLiveStillTearsDownADeadLocalSession(t *testing.T) {
	m, st, _, ws, lcm := newManagerWithSpawner(nil)
	rec := domain.SessionRecord{
		ID: "mer-1", ProjectID: "mer", Harness: domain.HarnessClaudeCode,
		Metadata: domain.SessionMetadata{Branch: "b", WorkspacePath: "/ws/mer-1", RuntimeHandleID: "h1"},
	}
	st.sessions["mer-1"] = rec

	if err := m.reconcileLive(context.Background(), rec); err != nil {
		t.Fatalf("reconcileLive: %v", err)
	}
	if lcm.terminated["mer-1"] != 1 || ws.stashCalls != 1 {
		t.Errorf("local reconcile changed: terminated=%d stashes=%d", lcm.terminated["mer-1"], ws.stashCalls)
	}
}
