package fake_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/execution/fake"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const testHost domain.ExecutionHostID = "worker-1"

func TestBackendCoversCompleteLifecycle(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, time.August, 7, 12, 30, 0, 0, time.UTC)
	backend := fake.New()
	backend.SetTime(now)
	backend.SetHostStatus(domain.ExecutionHostStatus{
		HostID: testHost, Reachable: true, ServerID: "server-1", Version: "0.2.5",
	})

	workspace, err := backend.Provision(ctx, ports.ExecutionProvisionRequest{
		SessionID: "session-1", ProjectID: "project-1", HostID: testHost,
		WorkspaceTitle: "ao:session-1:1", RepoPath: "/repos/ao", Branch: "ao/task-1",
		BaseBranch: "main", Provider: "codex", Model: "gpt-test", Mode: "auto",
	})
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if workspace.WorkspaceID != "fake-workspace-001" || workspace.CreatedAt != now || workspace.Branch != "ao/task-1" {
		t.Fatalf("workspace = %#v", workspace)
	}

	labels := map[string]string{"ao.intent": "intent-1", "paseo.worktree": "session-1:1"}
	agent, err := backend.Launch(ctx, ports.ExecutionLaunchRequest{
		SessionID: "session-1", HostID: testHost, WorkspaceID: workspace.WorkspaceID,
		IntentID: "intent-1", Prompt: "implement it", Labels: labels,
		Provider: "codex", Model: "gpt-test", Mode: "auto",
	})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	if agent.AgentID != "fake-agent-001" || agent.WorkspaceID != workspace.WorkspaceID || agent.LaunchedAt != now {
		t.Fatalf("agent = %#v", agent)
	}
	handle := fake.Handle(testHost, agent.AgentID)

	alive, err := backend.Alive(ctx, handle)
	if err != nil || !alive {
		t.Fatalf("Alive = (%v, %v), want (true, nil)", alive, err)
	}
	if err := backend.SetOutput(testHost, agent.AgentID, "one\ntwo\nthree\n"); err != nil {
		t.Fatal(err)
	}
	output, err := backend.Output(ctx, handle, 2)
	if err != nil || output != "two\nthree" {
		t.Fatalf("Output = (%q, %v), want trailing lines", output, err)
	}
	if err := backend.SendMessage(ctx, handle, "continue"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if got := backend.Messages(testHost, agent.AgentID); !reflect.DeepEqual(got, []string{"continue"}) {
		t.Fatalf("messages = %#v", got)
	}

	status, err := backend.Status(ctx, testHost)
	if err != nil || status.ServerID != "server-1" || status.Version != "0.2.5" {
		t.Fatalf("Status = (%#v, %v)", status, err)
	}
	owned, err := backend.ListOwned(ctx, testHost)
	if err != nil || len(owned) != 1 || owned[0].AgentID != agent.AgentID || owned[0].Worktree != "session-1:1" {
		t.Fatalf("ListOwned = (%#v, %v)", owned, err)
	}
	detail, err := backend.Inspect(ctx, testHost, agent.AgentID)
	if err != nil || detail.Status != domain.ExecutionAgentRunning || detail.Cwd != "/repos/ao" {
		t.Fatalf("Inspect = (%#v, %v)", detail, err)
	}

	if err := backend.Stop(ctx, handle); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	alive, err = backend.Alive(ctx, handle)
	if err != nil || alive {
		t.Fatalf("Alive after Stop = (%v, %v), want (false, nil)", alive, err)
	}
	detail, err = backend.Inspect(ctx, testHost, agent.AgentID)
	if err != nil || detail.Status != domain.ExecutionAgentClosed {
		t.Fatalf("Inspect after Stop = (%#v, %v)", detail, err)
	}

	wantOperations := []fake.Operation{
		fake.OperationProvision, fake.OperationLaunch, fake.OperationAlive,
		fake.OperationOutput, fake.OperationSendMessage, fake.OperationStatus,
		fake.OperationListOwned, fake.OperationInspect, fake.OperationStop,
		fake.OperationAlive, fake.OperationInspect,
	}
	calls := backend.Calls()
	gotOperations := make([]fake.Operation, len(calls))
	for i := range calls {
		gotOperations[i] = calls[i].Operation
	}
	if !reflect.DeepEqual(gotOperations, wantOperations) {
		t.Fatalf("operations = %#v, want %#v", gotOperations, wantOperations)
	}
	labels["ao.intent"] = "mutated-after-call"
	if calls[1].Launch.Labels["ao.intent"] != "intent-1" {
		t.Fatalf("call log aliases caller labels: %#v", calls[1].Launch.Labels)
	}
}

func TestHostUnreachableFailsEveryHostOperation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	backend, workspace, agent := launchedBackend(t)
	handle := fake.Handle(testHost, agent.AgentID)
	backend.SetHostReachable(testHost, false)

	tests := []struct {
		name string
		call func() error
	}{
		{"provision", func() error {
			_, err := backend.Provision(ctx, ports.ExecutionProvisionRequest{HostID: testHost})
			return err
		}},
		{"launch", func() error {
			_, err := backend.Launch(ctx, ports.ExecutionLaunchRequest{HostID: testHost, WorkspaceID: workspace.WorkspaceID})
			return err
		}},
		{"stop", func() error { return backend.Stop(ctx, handle) }},
		{"alive", func() error {
			alive, err := backend.Alive(ctx, handle)
			if alive {
				t.Error("Alive returned true for unreachable host")
			}
			return err
		}},
		{"output", func() error {
			_, err := backend.Output(ctx, handle, 1)
			return err
		}},
		{"send message", func() error { return backend.SendMessage(ctx, handle, "hello") }},
		{"status", func() error {
			_, err := backend.Status(ctx, testHost)
			return err
		}},
		{"list owned", func() error {
			_, err := backend.ListOwned(ctx, testHost)
			return err
		}},
		{"inspect", func() error {
			_, err := backend.Inspect(ctx, testHost, agent.AgentID)
			return err
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.call()
			if !errors.Is(err, fake.ErrHostUnreachable) {
				t.Fatalf("error = %v, want ErrHostUnreachable", err)
			}
		})
	}
}

func TestListOwnedModelsAmbiguousEmptyAndDuplicateMatches(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	backend, _, agent := launchedBackend(t)

	// A reachable host can return no rows even though direct inspection proves
	// the agent still exists. Recovery must therefore treat zero as ambiguous.
	backend.SetListOwned(testHost, nil)
	got, err := backend.ListOwned(ctx, testHost)
	if err != nil || len(got) != 0 {
		t.Fatalf("ambiguous empty ListOwned = (%#v, %v)", got, err)
	}
	if _, err := backend.Inspect(ctx, testHost, agent.AgentID); err != nil {
		t.Fatalf("direct Inspect after empty list: %v", err)
	}

	// Labels are hints rather than unique keys. An exact snapshot can contain
	// more than one candidate and must not be silently deduplicated by the fake.
	duplicate := []domain.ExecutionAgentObservation{
		{HostID: testHost, AgentID: "agent-a", WorkspaceID: "workspace-1", Status: domain.ExecutionAgentIdle},
		{HostID: testHost, AgentID: "agent-b", WorkspaceID: "workspace-1", Status: domain.ExecutionAgentIdle},
	}
	backend.SetListOwned(testHost, duplicate)
	got, err = backend.ListOwned(ctx, testHost)
	if err != nil || !reflect.DeepEqual(got, duplicate) {
		t.Fatalf("duplicate ListOwned = (%#v, %v), want %#v", got, err, duplicate)
	}
	got[0].AgentID = "mutated"
	again, err := backend.ListOwned(ctx, testHost)
	if err != nil || again[0].AgentID != "agent-a" {
		t.Fatalf("snapshot aliases returned result: (%#v, %v)", again, err)
	}
}

func TestLaunchFailureCanLeaveZombieForReconciliation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	backend := fake.New()
	workspace, err := backend.Provision(ctx, ports.ExecutionProvisionRequest{
		HostID: testHost, RepoPath: "/repos/ao", Branch: "ao/zombie",
	})
	if err != nil {
		t.Fatal(err)
	}
	providerErr := errors.New("provider rejected initial prompt after persistence")
	backend.FailNextAfterMutation(fake.OperationLaunch, providerErr)
	request := ports.ExecutionLaunchRequest{
		HostID: testHost, WorkspaceID: workspace.WorkspaceID, IntentID: "intent-zombie",
		Labels: map[string]string{"ao.intent": "intent-zombie", "paseo.worktree": "session-zombie:1"},
	}
	if _, err := backend.Launch(ctx, request); !errors.Is(err, providerErr) {
		t.Fatalf("Launch error = %v, want provider error", err)
	}

	owned, err := backend.ListOwned(ctx, testHost)
	if err != nil || len(owned) != 1 {
		t.Fatalf("zombie ListOwned = (%#v, %v), want one persisted agent", owned, err)
	}
	if owned[0].AgentID != "fake-agent-001" || owned[0].Worktree != "session-zombie:1" {
		t.Fatalf("zombie observation = %#v", owned[0])
	}
	alive, err := backend.Alive(ctx, fake.Handle(testHost, owned[0].AgentID))
	if err != nil || !alive {
		t.Fatalf("zombie Alive = (%v, %v), want live persisted agent", alive, err)
	}
}

func TestFailureBeforeMutationLeavesNoAgent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	backend := fake.New()
	workspace, err := backend.Provision(ctx, ports.ExecutionProvisionRequest{HostID: testHost})
	if err != nil {
		t.Fatal(err)
	}
	backend.FailNext(fake.OperationLaunch, nil)
	if _, err := backend.Launch(ctx, ports.ExecutionLaunchRequest{HostID: testHost, WorkspaceID: workspace.WorkspaceID}); !errors.Is(err, fake.ErrInjected) {
		t.Fatalf("Launch error = %v, want ErrInjected", err)
	}
	owned, err := backend.ListOwned(ctx, testHost)
	if err != nil || len(owned) != 0 {
		t.Fatalf("agents after pre-mutation failure = (%#v, %v)", owned, err)
	}
}

func TestBackendHonorsCancellationAndRejectsInvalidHandles(t *testing.T) {
	t.Parallel()
	backend := fake.New()
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := backend.Status(canceled, testHost); !errors.Is(err, context.Canceled) {
		t.Fatalf("Status error = %v, want context.Canceled", err)
	}
	if len(backend.Calls()) != 0 {
		t.Fatal("canceled call was recorded")
	}

	bad := ports.RuntimeHandle{ID: "not-namespaced"}
	if _, err := backend.Alive(context.Background(), bad); !errors.Is(err, fake.ErrInvalidHandle) {
		t.Fatalf("Alive error = %v, want ErrInvalidHandle", err)
	}
	if _, err := backend.Output(context.Background(), fake.Handle(testHost, "missing"), 1); !errors.Is(err, fake.ErrNotFound) {
		t.Fatalf("Output error = %v, want ErrNotFound", err)
	}
}

func launchedBackend(t *testing.T) (*fake.Backend, domain.ExecutionWorkspace, domain.ExecutionAgent) {
	t.Helper()
	backend := fake.New()
	workspace, err := backend.Provision(context.Background(), ports.ExecutionProvisionRequest{
		HostID: testHost, RepoPath: "/repos/ao", Branch: "ao/test",
	})
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	agent, err := backend.Launch(context.Background(), ports.ExecutionLaunchRequest{
		HostID: testHost, WorkspaceID: workspace.WorkspaceID,
		Labels: map[string]string{"paseo.worktree": "session-1:1"},
	})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	return backend, workspace, agent
}
