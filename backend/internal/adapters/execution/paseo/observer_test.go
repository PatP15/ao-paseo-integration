package paseo

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/runtimehandle"
)

func observerBackend(client *fakeExecutionClient, store *memoryExecutionStore) *Backend {
	return newBackend(client, store, func() time.Time { return backendTestNow })
}

func TestObserverStatusReportsDesktopOwnershipInsteadOfRefusing(t *testing.T) {
	t.Parallel()
	client := newFakeExecutionClient(nil)
	client.status.DesktopManaged = boolPointer(true)
	backend := observerBackend(client, newMemoryExecutionStore(nil))

	status, err := backend.Status(context.Background(), "host-1")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	want := domain.ExecutionHostStatus{
		HostID: "host-1", Reachable: true, DesktopManaged: true,
		ServerID: "server-1", Version: SupportedVersion, ObservedAt: backendTestNow,
	}
	if !reflect.DeepEqual(status, want) {
		t.Fatalf("status = %#v, want %#v", status, want)
	}
	// The mutating guard refuses a desktop-managed host; the observer must be
	// able to report it as a fact instead.
	handle, err := runtimehandle.New(domain.ExecutionBackendPaseo, "host-1", "agent-1")
	if err != nil {
		t.Fatalf("build handle: %v", err)
	}
	if err := backend.Stop(context.Background(), handle); err == nil {
		t.Fatal("stop on a desktop-managed host should be refused")
	}
}

func TestObserverStatusErrorsWhenHostIsUnreachable(t *testing.T) {
	t.Parallel()
	client := newFakeExecutionClient(nil)
	client.statusErr = errors.New("connection refused")
	backend := observerBackend(client, newMemoryExecutionStore(nil))

	status, err := backend.Status(context.Background(), "host-1")
	if err == nil {
		t.Fatalf("unreachable host returned %#v with no error", status)
	}
	if status.Reachable {
		t.Fatalf("unreachable host reported reachable: %#v", status)
	}
}

func TestObserverStatusRejectsUnregisteredHost(t *testing.T) {
	t.Parallel()
	client := newFakeExecutionClient(nil)
	backend := observerBackend(client, newMemoryExecutionStore(nil))

	if _, err := backend.Status(context.Background(), "host-unknown"); err == nil {
		t.Fatal("expected an unregistered host to be refused")
	}
	if len(client.calls) != 0 {
		t.Fatalf("unregistered host still reached the CLI: %v", client.calls)
	}
}

func TestObserverListOwnedIsUnfilteredAndCheap(t *testing.T) {
	t.Parallel()
	client := newFakeExecutionClient(nil)
	client.agents = []Agent{
		{ID: "agent-1", Status: "running", Cwd: "/remote/worktree"},
		{ID: "agent-2", Status: "idle", Cwd: "/elsewhere"},
	}
	backend := observerBackend(client, newMemoryExecutionStore(nil))

	owned, err := backend.ListOwned(context.Background(), "host-1")
	if err != nil {
		t.Fatalf("list owned: %v", err)
	}
	want := []domain.ExecutionAgentObservation{
		{HostID: "host-1", AgentID: "agent-1", Status: domain.ExecutionAgentRunning, Cwd: "/remote/worktree"},
		{HostID: "host-1", AgentID: "agent-2", Status: domain.ExecutionAgentIdle, Cwd: "/elsewhere"},
	}
	if !reflect.DeepEqual(owned, want) {
		t.Fatalf("owned = %#v, want %#v", owned, want)
	}
	// A label filter cannot surface an agent AO has lost track of, which is the
	// only reason to list a whole host.
	if !reflect.DeepEqual(client.listLabels, []string{""}) {
		t.Fatalf("list labels = %#v, want one unfiltered call", client.listLabels)
	}
	// One invocation, not a status probe plus a list: the budget is ~5 commands
	// per hot tick.
	if !reflect.DeepEqual(client.calls, []string{"list-agents"}) {
		t.Fatalf("calls = %v, want exactly one list", client.calls)
	}
}

func TestObserverListOwnedRefusesUnknownStatus(t *testing.T) {
	t.Parallel()
	client := newFakeExecutionClient(nil)
	client.agents = []Agent{{ID: "agent-1", Status: "brand-new-state"}}
	backend := observerBackend(client, newMemoryExecutionStore(nil))

	if _, err := backend.ListOwned(context.Background(), "host-1"); err == nil {
		t.Fatal("expected an unknown remote status to be refused, not mapped")
	}
}

func TestObserverInspectMapsPermissionsWithFullIDs(t *testing.T) {
	t.Parallel()
	client := newFakeExecutionClient(nil)
	detail := validCandidate("agent-1")
	detail.PendingPermissions = []PendingPermission{
		{ID: "perm_0f9c1d2e3a4b5c6d7e8f", ToolName: "Bash", Reason: "run the test suite"},
	}
	client.details["agent-1"] = detail
	backend := observerBackend(client, newMemoryExecutionStore(nil))

	got, err := backend.Inspect(context.Background(), "host-1", "agent-1")
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if got.Status != domain.ExecutionAgentRunning || got.Worktree != "session-1:1" {
		t.Fatalf("detail = %#v", got)
	}
	want := []domain.ExecutionPermission{
		{ID: "perm_0f9c1d2e3a4b5c6d7e8f", ToolName: "Bash", Reason: "run the test suite"},
	}
	if !reflect.DeepEqual(got.PendingPermissions, want) {
		t.Fatalf("permissions = %#v, want %#v", got.PendingPermissions, want)
	}
	if !reflect.DeepEqual(client.calls, []string{"inspect:agent-1"}) {
		t.Fatalf("calls = %v, want exactly one inspect", client.calls)
	}
}

func TestObserverInspectRefusesMismatchedAgent(t *testing.T) {
	t.Parallel()
	client := newFakeExecutionClient(nil)
	client.details["agent-1"] = validCandidate("agent-other")
	backend := observerBackend(client, newMemoryExecutionStore(nil))

	_, err := backend.Inspect(context.Background(), "host-1", "agent-1")
	if err == nil || !strings.Contains(err.Error(), "agent-other") {
		t.Fatalf("expected a mismatched inspect to be refused, got %v", err)
	}
}
