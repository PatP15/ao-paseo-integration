// Package execpolicy holds this fork's explicit refusals: the AO features that
// are bound to a local worktree or a local terminal pane and therefore must
// fail loudly for a session whose agent runs on a remote execution host.
//
// Each refusal exists because the feature currently MISBEHAVES rather than
// erroring. Three call sites infer "this machine" from state a remotely
// executed session also has:
//
//   - internal/review — the reviewer engine spawns a reviewer process over the
//     worker's worktree. A remote session has none, so the engine falls through
//     to "has no workspace to review", which is indistinguishable from a local
//     session that simply has not been provisioned yet.
//   - internal/daemon's shell-terminal workspace locator — shellterm's fallback
//     chain is session workspace → project root → data dir. A remote session's
//     Metadata.WorkspacePath is permanently "" by design, so "open a shell for
//     this session" opens one in the operator's REAL main checkout, tab-titled
//     as that session. A destructive-git hazard, not a degraded feature.
//   - internal/observe/activity — capability is keyed on the harness, and the
//     fork reuses an existing harness value because widening the
//     sessions.harness CHECK is forbidden. The harness's TerminalActivityDetector
//     would run its tmux-pane regex over a Paseo transcript and write a bogus
//     ActivityIdle from whatever it matched.
//
// The full MVP non-goal list lives in docs/paseo-integration/ARCHITECTURE.md
// §5.5-§5.6. This package enforces the subset that would otherwise degrade
// silently; the rest already fail on their own.
package execpolicy

import (
	"context"
	"errors"
	"fmt"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/runtimehandle"
)

// ErrRemoteUnsupported is carried by every refusal in this package, so a caller
// can tell "AO deliberately does not do this remotely" apart from an ordinary
// failure without matching on message text.
var ErrRemoteUnsupported = errors.New("not supported for a session that runs on a remote execution host")

// BindingSource is the durable execution-binding lookup. *sqlite.Store
// implements it; it is stated here as a narrow optional interface so packages
// that already hold a store-shaped dependency can consult it without taking a
// storage dependency, matching the idiom in observe/reaper.
type BindingSource interface {
	GetSessionExecutionBinding(ctx context.Context, id domain.SessionID) (domain.SessionExecutionBinding, bool, error)
}

// IsRemoteHandle reports whether a runtime handle belongs to an execution
// backend rather than to a local runtime. It is deliberately built on
// runtimehandle rather than on the session's harness: the harness value is
// reused from an existing local harness, so inferring anything local from it is
// exactly the bug observe/activity has.
//
// A prefixed but malformed handle answers true, inheriting runtimehandle's rule
// that such a handle must never fall through to a local code path by accident.
func IsRemoteHandle(handleID string) bool {
	return handleID != "" && runtimehandle.IsNamespaced(handleID)
}

// Remote reports whether a session's agent executes on a remote host.
//
// It answers from the runtime handle first because that needs no I/O, then
// falls back to the durable execution binding. Consulting the binding is not
// redundant: the hazard window opens BEFORE launch. A dispatched but not yet
// launched remote session has a binding row and no handle at all, and that is
// precisely the window in which Metadata.WorkspacePath is empty and a local
// fallback would fire.
//
// bindings is taken as any so callers can pass a dependency they hold under a
// narrower interface; one that does not implement BindingSource reduces this to
// the handle check, which is complete for any session that has launched.
//
// A binding lookup failure is returned, never swallowed. Reporting an unknown
// answer as "local" would send the caller down the very path these refusals
// exist to prevent.
func Remote(ctx context.Context, bindings any, id domain.SessionID, meta domain.SessionMetadata) (bool, error) {
	if IsRemoteHandle(meta.RuntimeHandleID) {
		return true, nil
	}
	source, ok := bindings.(BindingSource)
	if !ok {
		return false, nil
	}
	binding, found, err := source.GetSessionExecutionBinding(ctx, id)
	if err != nil {
		return false, fmt.Errorf("execution binding for session %s: %w", id, err)
	}
	return found && binding.BackendType != domain.ExecutionBackendLocal, nil
}

// Refuse builds the refusal for feature on session id. Callers wrap it in their
// own transport vocabulary (a service sentinel, an apierr envelope); whatever
// they wrap it in, the result still answers errors.Is(err, ErrRemoteUnsupported).
func Refuse(feature string, id domain.SessionID) error {
	return fmt.Errorf("%s for session %s: %w", feature, id, ErrRemoteUnsupported)
}
