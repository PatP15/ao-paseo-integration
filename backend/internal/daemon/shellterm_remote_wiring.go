package daemon

import (
	"context"
	"fmt"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/execpolicy"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	shelltermsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/shellterm"
)

// remoteAwareSessionWorkspaceLocator refuses to resolve a shell's working
// directory for a session whose agent runs on a remote execution host, ahead of
// the local locator it wraps.
//
// This is a safety refusal, not a missing feature. shellterm's fallback chain
// is session workspace → project root → data dir, and a remotely executed
// session's Metadata.WorkspacePath is permanently "" by design. Left alone,
// "open a shell for this session" resolves to the PROJECT ROOT — the operator's
// real main checkout — and records it as that session's shell, tab-titled
// accordingly. Every command typed there, and every `git checkout`/`git clean`
// that follows, lands on the operator's own working tree. Failing the open is
// strictly safer than opening the wrong directory under a convincing label.
//
// The refusal carries both an apierr envelope, so the HTTP boundary answers a
// 409 with a stable code instead of a generic 500, and execpolicy's sentinel,
// so it is recognisable as a deliberate refusal rather than a lookup failure.
type remoteAwareSessionWorkspaceLocator struct {
	inner    shelltermsvc.SessionWorkspaceLocator
	sessions sessionGetter
	bindings execpolicy.BindingSource
}

// newSessionWorkspaceLocator builds the locator startShellTerminals installs:
// the local resolver, fenced by the remote refusal. bindings supplies the
// durable execution binding, which is what catches a session dispatched to a
// host but not yet launched — the window in which no runtime handle exists yet
// and the local fallback would otherwise fire.
func newSessionWorkspaceLocator(sessions sessionGetter, bindings execpolicy.BindingSource) shelltermsvc.SessionWorkspaceLocator {
	return &remoteAwareSessionWorkspaceLocator{
		inner:    &sessionWorkspaceLocator{sessions: sessions},
		sessions: sessions,
		bindings: bindings,
	}
}

// SessionWorkspace refuses remote sessions and otherwise delegates unchanged.
// A nil session lookup delegates too: the wrapped locator already answers that
// case, and duplicating its contract here would be a second place to keep in
// step with it.
func (l *remoteAwareSessionWorkspaceLocator) SessionWorkspace(ctx context.Context, id domain.SessionID) (string, domain.ProjectID, error) {
	if l.sessions == nil {
		return l.inner.SessionWorkspace(ctx, id)
	}
	sess, err := l.sessions.Get(ctx, id)
	if err != nil {
		return "", "", err
	}
	remote, err := execpolicy.Remote(ctx, l.bindings, id, sess.Metadata)
	if err != nil {
		return "", "", err
	}
	if remote {
		return "", "", fmt.Errorf("%w: %w",
			apierr.Conflict("SHELL_TERMINAL_REMOTE_SESSION",
				"This session runs on a remote execution host, so AO cannot open a local shell in its workspace", nil),
			execpolicy.Refuse("a shell terminal", id))
	}
	return l.inner.SessionWorkspace(ctx, id)
}
