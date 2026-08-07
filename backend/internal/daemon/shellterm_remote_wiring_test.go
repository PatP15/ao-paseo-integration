package daemon

import (
	"context"
	"errors"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/execpolicy"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
)

type fakeBindingSource struct {
	binding domain.SessionExecutionBinding
	found   bool
	err     error
}

func (f *fakeBindingSource) GetSessionExecutionBinding(context.Context, domain.SessionID) (domain.SessionExecutionBinding, bool, error) {
	return f.binding, f.found, f.err
}

func remoteSessionGetter(handleID string) *fakeSessionGetter {
	return &fakeSessionGetter{sessions: map[domain.SessionID]domain.Session{
		"mer-1": {SessionRecord: domain.SessionRecord{
			ID: "mer-1", ProjectID: "mer",
			// WorkspacePath is empty for every remotely executed session: the
			// worktree belongs to the host. That emptiness is exactly what would
			// otherwise send the shell to the project root.
			Metadata: domain.SessionMetadata{Branch: "ao/mer-1", RuntimeHandleID: handleID},
		}},
	}}
}

// The hazard this refusal exists for: without it, an empty WorkspacePath falls
// through to the project root — the operator's real main checkout — and the
// shell is recorded and tab-titled as belonging to the remote session.
func TestSessionWorkspaceLocator_RefusesRemoteSessionByHandle(t *testing.T) {
	loc := newSessionWorkspaceLocator(remoteSessionGetter("paseo:host-1/agt_abc"), &fakeBindingSource{})

	path, _, err := loc.SessionWorkspace(context.Background(), "mer-1")
	if err == nil {
		t.Fatal("SessionWorkspace: want a refusal for a remotely executed session")
	}
	if path != "" {
		t.Errorf("path = %q, want empty; a refusal must never hand back a directory", path)
	}
	if !errors.Is(err, execpolicy.ErrRemoteUnsupported) {
		t.Errorf("err = %v, want it to carry execpolicy.ErrRemoteUnsupported", err)
	}
	var apiErr *apierr.Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("err = %v, want an apierr so the boundary answers 409 rather than 500", err)
	}
	if apiErr.Kind != apierr.KindConflict {
		t.Errorf("kind = %v, want KindConflict", apiErr.Kind)
	}
	if apiErr.Code != "SHELL_TERMINAL_REMOTE_SESSION" {
		t.Errorf("code = %q, want SHELL_TERMINAL_REMOTE_SESSION", apiErr.Code)
	}
}

// Before launch there is no runtime handle, and WorkspacePath is already empty.
// The durable binding is the only thing that distinguishes this session from a
// local one still waiting on its worktree.
func TestSessionWorkspaceLocator_RefusesRemoteSessionByBindingBeforeLaunch(t *testing.T) {
	bindings := &fakeBindingSource{
		binding: domain.SessionExecutionBinding{BackendType: domain.ExecutionBackendPaseo},
		found:   true,
	}
	loc := newSessionWorkspaceLocator(remoteSessionGetter(""), bindings)

	if _, _, err := loc.SessionWorkspace(context.Background(), "mer-1"); !errors.Is(err, execpolicy.ErrRemoteUnsupported) {
		t.Fatalf("err = %v, want the remote refusal", err)
	}
}

// An inconclusive binding lookup must fail the open rather than fall back to
// the project root: failing is recoverable, opening a shell in the operator's
// checkout is not.
func TestSessionWorkspaceLocator_FailsClosedWhenBindingLookupFails(t *testing.T) {
	lookupErr := errors.New("database is locked")
	loc := newSessionWorkspaceLocator(remoteSessionGetter(""), &fakeBindingSource{err: lookupErr})

	path, _, err := loc.SessionWorkspace(context.Background(), "mer-1")
	if !errors.Is(err, lookupErr) {
		t.Fatalf("err = %v, want the binding lookup failure", err)
	}
	if path != "" {
		t.Errorf("path = %q, want empty", path)
	}
}

// Local sessions delegate unchanged, including the recorded-path-was-removed
// fallback the wrapped locator owns.
func TestSessionWorkspaceLocator_LocalSessionsDelegateUnchanged(t *testing.T) {
	dir := t.TempDir()
	getter := &fakeSessionGetter{sessions: map[domain.SessionID]domain.Session{
		"mer-1": {SessionRecord: domain.SessionRecord{
			ID: "mer-1", ProjectID: "mer",
			Metadata: domain.SessionMetadata{WorkspacePath: dir, RuntimeHandleID: "ao-mer-1"},
		}},
	}}
	loc := newSessionWorkspaceLocator(getter, &fakeBindingSource{})

	path, projectID, err := loc.SessionWorkspace(context.Background(), "mer-1")
	if err != nil {
		t.Fatalf("SessionWorkspace: %v", err)
	}
	if path != dir {
		t.Errorf("path = %q, want %q", path, dir)
	}
	if projectID != "mer" {
		t.Errorf("projectID = %q, want mer", projectID)
	}
}

func TestSessionWorkspaceLocator_PropagatesUnknownSessionErrorThroughWrapper(t *testing.T) {
	loc := newSessionWorkspaceLocator(&fakeSessionGetter{}, &fakeBindingSource{})

	if _, _, err := loc.SessionWorkspace(context.Background(), "ghost"); err == nil {
		t.Fatal("SessionWorkspace: want error for an unknown session")
	}
}
