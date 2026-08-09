package reaper

import (
	"context"
	"errors"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type fakeExecutionSessions struct {
	fakeSessions
	binding    domain.SessionExecutionBinding
	found      bool
	bindingErr error
}

func (s fakeExecutionSessions) GetSessionExecutionBinding(context.Context, domain.SessionID) (domain.SessionExecutionBinding, bool, error) {
	return s.binding, s.found, s.bindingErr
}

func TestTick_RemoteAmbiguousDeadProbeDoesNotTerminate(t *testing.T) {
	lcm := &fakeLCM{}
	sessions := fakeExecutionSessions{
		fakeSessions: fakeSessions{rows: []domain.SessionRecord{probableSession("mer-remote")}},
		binding: domain.SessionExecutionBinding{
			SessionID:   "mer-remote",
			BackendType: domain.ExecutionBackendPaseo,
		},
		found: true,
	}
	r := New(lcm, sessions, fakeRuntime{alive: false}, Config{Logger: quietLogger()})

	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if got := lcm.observed["mer-remote"].Runtime; got != ports.ProbeFailed {
		t.Fatalf("remote ambiguous probe = %q, want %q so lifecycle cannot terminate the session", got, ports.ProbeFailed)
	}
}

func TestTick_LocalDeadProbeRemainsConclusiveWithExecutionStorage(t *testing.T) {
	lcm := &fakeLCM{}
	sessions := fakeExecutionSessions{
		fakeSessions: fakeSessions{rows: []domain.SessionRecord{probableSession("mer-local")}},
		binding: domain.SessionExecutionBinding{
			SessionID:   "mer-local",
			BackendType: domain.ExecutionBackendLocal,
		},
		found: true,
	}
	r := New(lcm, sessions, fakeRuntime{alive: false}, Config{Logger: quietLogger()})

	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if got := lcm.observed["mer-local"].Runtime; got != ports.ProbeDead {
		t.Fatalf("local dead probe = %q, want %q", got, ports.ProbeDead)
	}
}

func TestTick_BindingLookupFailureMakesDeadProbeInconclusive(t *testing.T) {
	lcm := &fakeLCM{}
	sessions := fakeExecutionSessions{
		fakeSessions: fakeSessions{rows: []domain.SessionRecord{probableSession("mer-unknown")}},
		bindingErr:   errors.New("sqlite unavailable"),
	}
	r := New(lcm, sessions, fakeRuntime{alive: false}, Config{Logger: quietLogger()})

	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if got := lcm.observed["mer-unknown"].Runtime; got != ports.ProbeFailed {
		t.Fatalf("probe with failed binding lookup = %q, want %q", got, ports.ProbeFailed)
	}
}
