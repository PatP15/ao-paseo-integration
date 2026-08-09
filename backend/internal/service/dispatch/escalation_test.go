package dispatch

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/executionerror"
	"github.com/aoagents/agent-orchestrator/backend/internal/paseoevent"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestAmbiguousProvisionEscalatesOnceThenCreatesOnFreshTitle(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, time.August, 8, 1, 0, 0, 0, time.UTC)
	store := newDispatchTestStore(t, now)
	dispatched := dispatchWithStableIDs(t, store, now)
	backend := &escalationBackend{ambiguousRemaining: 1}
	worker := escalationWorker(store, backend, &now)
	worker.briefs = paseoevent.NewBriefs(store)

	if delivered, err := worker.DeliverOne(ctx); !delivered || !errors.Is(err, executionerror.ErrProvisionOutcomeUnknown) {
		t.Fatalf("ambiguous delivery = (%v, %v)", delivered, err)
	}
	binding, found, err := store.GetSessionExecutionBinding(ctx, dispatched.Session.ID)
	if err != nil || !found {
		t.Fatalf("binding after escalation: found=%v err=%v", found, err)
	}
	if binding.Attempt != 2 || binding.WorkspaceTitle != "ao:project-1:2" ||
		binding.IntentID == dispatched.Binding.IntentID || binding.LaunchID == dispatched.Binding.LaunchID {
		t.Fatalf("binding after escalation = %#v", binding)
	}
	command, found, err := store.GetExecutionCommand(ctx, dispatched.Command.ID)
	if err != nil || !found || command.State != domain.ExecutionCommandPending {
		t.Fatalf("command after escalation = (%#v, %v, %v)", command, found, err)
	}
	payload, err := decodeStartPayload(command.PayloadJSON)
	if err != nil || payload.Attempt != 2 || payload.IntentID != binding.IntentID || payload.LaunchID != binding.LaunchID {
		t.Fatalf("payload after escalation = (%#v, %v)", payload, err)
	}

	if delivered, err := worker.DeliverOne(ctx); err != nil || !delivered {
		t.Fatalf("fresh-attempt delivery = (%v, %v)", delivered, err)
	}
	if len(backend.provisions) != 2 || backend.provisions[0].WorkspaceTitle != "ao:project-1:1" ||
		backend.provisions[1].WorkspaceTitle != "ao:project-1:2" {
		t.Fatalf("provision requests = %#v", backend.provisions)
	}
	if len(backend.launches) != 1 || backend.launches[0].IntentID != binding.IntentID ||
		backend.launches[0].Labels["ao.attempt"] != "2" {
		t.Fatalf("launch requests = %#v", backend.launches)
	}
	briefRow, found, err := store.GetLatestSessionBrief(ctx, dispatched.Session.ID)
	if err != nil || !found || briefRow.Version != 2 {
		t.Fatalf("brief after escalation = (%#v, %v, %v)", briefRow, found, err)
	}
	brief, err := paseoevent.DecodeBrief(briefRow.BriefJSON)
	if err != nil || brief.Attempt != 2 || brief.LaunchID != binding.LaunchID {
		t.Fatalf("decoded brief after escalation = (%#v, %v)", brief, err)
	}
}

func TestAmbiguousProvisionEscalationCapFailsCommand(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, time.August, 8, 2, 0, 0, 0, time.UTC)
	store := newDispatchTestStore(t, now)
	dispatched := dispatchWithStableIDs(t, store, now)
	backend := &escalationBackend{ambiguousRemaining: 100}
	worker := escalationWorker(store, backend, &now)

	for attempt := 1; attempt <= 3; attempt++ {
		delivered, err := worker.DeliverOne(ctx)
		if !delivered || !errors.Is(err, executionerror.ErrProvisionOutcomeUnknown) {
			t.Fatalf("delivery %d = (%v, %v)", attempt, delivered, err)
		}
	}
	binding, found, err := store.GetSessionExecutionBinding(ctx, dispatched.Session.ID)
	if err != nil || !found || binding.Attempt != 3 {
		t.Fatalf("binding at cap = (%#v, %v, %v)", binding, found, err)
	}
	command, found, err := store.GetExecutionCommand(ctx, dispatched.Command.ID)
	if err != nil || !found || command.State != domain.ExecutionCommandFailed ||
		command.LastError == "" {
		t.Fatalf("command at cap = (%#v, %v, %v)", command, found, err)
	}
	if len(backend.provisions) != 3 {
		t.Fatalf("provision calls = %d, want one per allowed attempt", len(backend.provisions))
	}
}

func TestTransientProvisionFailureUsesBackoffWithoutEscalation(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, time.August, 8, 3, 0, 0, 0, time.UTC)
	store := newDispatchTestStore(t, now)
	dispatched := dispatchWithStableIDs(t, store, now)
	transient := errors.New("host temporarily unavailable")
	backend := &escalationBackend{transientRemaining: 1, transientErr: transient}
	worker := escalationWorker(store, backend, &now)

	if delivered, err := worker.DeliverOne(ctx); !delivered || !errors.Is(err, transient) {
		t.Fatalf("transient delivery = (%v, %v)", delivered, err)
	}
	binding, found, err := store.GetSessionExecutionBinding(ctx, dispatched.Session.ID)
	if err != nil || !found || binding.Attempt != 1 || binding.IntentID != dispatched.Binding.IntentID {
		t.Fatalf("binding after transient failure = (%#v, %v, %v)", binding, found, err)
	}
	command, found, err := store.GetExecutionCommand(ctx, dispatched.Command.ID)
	if err != nil || !found || command.State != domain.ExecutionCommandPending ||
		!command.NextAttemptAt.Equal(now.Add(defaultBaseBackoff)) {
		t.Fatalf("command after transient failure = (%#v, %v, %v)", command, found, err)
	}
	if payload, err := decodeStartPayload(command.PayloadJSON); err != nil || payload.Attempt != 1 {
		t.Fatalf("payload after transient failure = (%#v, %v)", payload, err)
	}
}

func dispatchWithStableIDs(t *testing.T, store dispatchStore, now time.Time) domain.ExecutionDispatch {
	t.Helper()
	ids := 0
	service := newService(store, func() time.Time { return now }, func() string {
		ids++
		return fmt.Sprintf("escalation-id-%d", ids)
	})
	dispatched, err := service.Dispatch(context.Background(), testDispatchRequest())
	if err != nil {
		t.Fatal(err)
	}
	return dispatched
}

func escalationWorker(store commandStore, backend ports.ExecutionBackend, now *time.Time) *Worker {
	worker := NewWorker(store, BackendResolverFunc(func(domain.ExecutionHostID) (ports.ExecutionBackend, bool) {
		return backend, true
	}))
	worker.now = func() time.Time { return *now }
	worker.lease = time.Second
	return worker
}

type escalationBackend struct {
	ambiguousRemaining int
	transientRemaining int
	transientErr       error
	provisions         []ports.ExecutionProvisionRequest
	launches           []ports.ExecutionLaunchRequest
}

func (b *escalationBackend) Provision(_ context.Context, req ports.ExecutionProvisionRequest) (domain.ExecutionWorkspace, error) {
	b.provisions = append(b.provisions, req)
	if b.ambiguousRemaining > 0 {
		b.ambiguousRemaining--
		return domain.ExecutionWorkspace{}, fmt.Errorf("create response lost: %w", executionerror.ErrProvisionOutcomeUnknown)
	}
	if b.transientRemaining > 0 {
		b.transientRemaining--
		return domain.ExecutionWorkspace{}, b.transientErr
	}
	return domain.ExecutionWorkspace{
		HostID: req.HostID, WorkspaceID: domain.ExecutionWorkspaceID(fmt.Sprintf("workspace-%d", len(b.provisions))),
		Title: req.WorkspaceTitle, RepoPath: req.RepoPath, Branch: req.Branch,
	}, nil
}

func (b *escalationBackend) Launch(_ context.Context, req ports.ExecutionLaunchRequest) (domain.ExecutionAgent, error) {
	b.launches = append(b.launches, req)
	return domain.ExecutionAgent{HostID: req.HostID, AgentID: "agent-1", WorkspaceID: req.WorkspaceID}, nil
}
