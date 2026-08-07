package review

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/execpolicy"
)

// remoteSessions is the production shape: the same value satisfies Sessions and
// execpolicy.BindingSource (*sqlite.Store does), so the engine can consult the
// durable binding without taking a storage dependency.
type remoteSessions struct {
	rec        domain.SessionRecord
	binding    domain.SessionExecutionBinding
	hasBinding bool
	bindingErr error
}

func (s remoteSessions) GetSession(context.Context, domain.SessionID) (domain.SessionRecord, bool, error) {
	return s.rec, true, nil
}

func (s remoteSessions) GetSessionExecutionBinding(context.Context, domain.SessionID) (domain.SessionExecutionBinding, bool, error) {
	return s.binding, s.hasBinding, s.bindingErr
}

func remoteWorker(handleID string) domain.SessionRecord {
	return domain.SessionRecord{
		ID:        "mer-1",
		ProjectID: "mer",
		Harness:   domain.HarnessCodex,
		// A remotely executed session carries a branch but never a local
		// workspace path — that is what makes observe/scm find its PRs while
		// every local-worktree feature has nothing to stand on.
		Metadata: domain.SessionMetadata{Branch: "ao/mer-1", RuntimeHandleID: handleID},
	}
}

// A launched remote worker is refused by handle alone, before any PR lookup, and
// the refusal names the reason rather than reading as an unprovisioned workspace.
func TestTriggerRefusesRemoteWorkerByHandle(t *testing.T) {
	store := &fakeStore{}
	launcher := &fakeLauncher{handle: "review-mer-1"}
	eng := newEngineForTest(store, remoteSessions{rec: remoteWorker("paseo:host-1/agt_abc")}, prAt("sha1"), fakeProjects{}, launcher)

	_, err := eng.Trigger(context.Background(), "mer-1")
	if err == nil {
		t.Fatal("Trigger: want a refusal for a remotely executed worker")
	}
	if !errors.Is(err, execpolicy.ErrRemoteUnsupported) {
		t.Errorf("err = %v, want it to carry execpolicy.ErrRemoteUnsupported", err)
	}
	// ErrInvalid keeps the HTTP boundary answering 422 rather than 500: the
	// request is well formed, the session simply cannot be reviewed here.
	if !errors.Is(err, ErrInvalid) {
		t.Errorf("err = %v, want it to carry ErrInvalid", err)
	}
	if strings.Contains(err.Error(), "no workspace to review") {
		t.Errorf("err = %q, want the remote refusal rather than the unprovisioned-workspace message", err)
	}
	if launcher.spawned || launcher.notified {
		t.Error("launcher was used; a refused worker must never reach the reviewer spawn")
	}
}

// The dispatched-but-not-yet-launched window: no runtime handle exists yet, so
// only the durable binding can tell this apart from a local session waiting on
// its worktree.
func TestTriggerRefusesRemoteWorkerByBindingBeforeLaunch(t *testing.T) {
	store := &fakeStore{}
	launcher := &fakeLauncher{handle: "review-mer-1"}
	sessions := remoteSessions{
		rec:        remoteWorker(""),
		binding:    domain.SessionExecutionBinding{BackendType: domain.ExecutionBackendPaseo},
		hasBinding: true,
	}
	eng := newEngineForTest(store, sessions, prAt("sha1"), fakeProjects{}, launcher)

	_, err := eng.Trigger(context.Background(), "mer-1")
	if !errors.Is(err, execpolicy.ErrRemoteUnsupported) {
		t.Fatalf("err = %v, want the remote refusal", err)
	}
	if launcher.spawned || launcher.notified {
		t.Error("launcher was used; a refused worker must never reach the reviewer spawn")
	}
}

// A failed binding lookup must not silently resolve to "local" and let the
// reviewer spawn against a worktree that does not exist on this machine.
func TestTriggerFailsWhenRemoteCheckIsInconclusive(t *testing.T) {
	store := &fakeStore{}
	launcher := &fakeLauncher{handle: "review-mer-1"}
	lookupErr := errors.New("database is locked")
	sessions := remoteSessions{rec: remoteWorker(""), bindingErr: lookupErr}
	eng := newEngineForTest(store, sessions, prAt("sha1"), fakeProjects{}, launcher)

	_, err := eng.Trigger(context.Background(), "mer-1")
	if !errors.Is(err, lookupErr) {
		t.Fatalf("err = %v, want the binding lookup failure", err)
	}
	if launcher.spawned || launcher.notified {
		t.Error("launcher was used; a refused worker must never reach the reviewer spawn")
	}
}

// The local path is unchanged: a local worker with no workspace still gets the
// original message, so the refusal cannot be mistaken for a behaviour change to
// ordinary sessions.
func TestTriggerKeepsUnprovisionedLocalWorkerMessage(t *testing.T) {
	store := &fakeStore{}
	launcher := &fakeLauncher{handle: "review-mer-1"}
	worker := liveWorker()
	worker.Metadata.WorkspacePath = ""
	eng := newEngineForTest(store, fakeSessions{rec: worker, ok: true}, prAt("sha1"), fakeProjects{}, launcher)

	_, err := eng.Trigger(context.Background(), "mer-1")
	if err == nil {
		t.Fatal("Trigger: want an error for a worker with no workspace")
	}
	if errors.Is(err, execpolicy.ErrRemoteUnsupported) {
		t.Errorf("err = %v, want the local unprovisioned-workspace error, not the remote refusal", err)
	}
	if !strings.Contains(err.Error(), "no workspace to review") {
		t.Errorf("err = %q, want the unprovisioned-workspace message", err)
	}
}
