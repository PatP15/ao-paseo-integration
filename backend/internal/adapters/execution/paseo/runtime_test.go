package paseo

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/runtimehandle"
)

func paseoRuntimeFixture(t *testing.T) (*Backend, *fakeExecutionClient, ports.RuntimeHandle) {
	t.Helper()
	client := newFakeExecutionClient(nil)
	client.details["agent-1"] = AgentDetail{ID: "agent-1", Status: "running"}
	store := newMemoryExecutionStore(nil)
	backend := newBackend(client, store, func() time.Time { return backendTestNow })
	handle, err := runtimehandle.New(domain.ExecutionBackendPaseo, "host-1", "agent-1")
	if err != nil {
		t.Fatal(err)
	}
	return backend, client, handle
}

func TestRuntimeRoutesStopAliveOutputAndSendToVerifiedHost(t *testing.T) {
	backend, client, handle := paseoRuntimeFixture(t)
	client.logs = "one\ntwo\nthree\n"
	ctx := context.Background()

	if alive, err := backend.Alive(ctx, handle); err != nil || !alive {
		t.Fatalf("alive=%v err=%v", alive, err)
	}
	if output, err := backend.Output(ctx, handle, 2); err != nil || output != "two\nthree" {
		t.Fatalf("output=%q err=%v", output, err)
	}
	if err := backend.SendMessage(ctx, handle, "continue"); err != nil {
		t.Fatal(err)
	}
	if err := backend.Stop(ctx, handle); err != nil {
		t.Fatal(err)
	}

	want := []string{
		"status", "inspect:agent-1",
		"status", "logs:agent-1",
		"status", "send:agent-1:continue",
		"status", "stop:agent-1",
	}
	if !reflect.DeepEqual(client.calls, want) {
		t.Fatalf("calls = %v, want %v", client.calls, want)
	}
}

func TestAliveErrorsWhenHostIsUnreachable(t *testing.T) {
	backend, client, handle := paseoRuntimeFixture(t)
	client.statusErr = &Error{Kind: ErrorNetwork, Message: "Paseo command failed: connection refused"}

	alive, err := backend.Alive(context.Background(), handle)
	if err == nil || alive {
		t.Fatalf("alive=%v err=%v; unreachable must never return false,nil", alive, err)
	}
	if !IsKind(err, ErrorNetwork) {
		t.Fatalf("error lost network classification: %v", err)
	}
}

func TestAliveErrorsWhenInspectIsAmbiguous(t *testing.T) {
	backend, client, handle := paseoRuntimeFixture(t)
	client.inspectErr = errors.New("empty inspect result")

	alive, err := backend.Alive(context.Background(), handle)
	if err == nil || alive {
		t.Fatalf("alive=%v err=%v; ambiguous inspect must not report death", alive, err)
	}
}

func TestRuntimeRejectsNonPaseoHandleBeforeCallingClient(t *testing.T) {
	backend, client, _ := paseoRuntimeFixture(t)
	if _, err := backend.Alive(context.Background(), ports.RuntimeHandle{ID: "local"}); err == nil {
		t.Fatal("local handle accepted by Paseo runtime")
	}
	if _, err := backend.Alive(context.Background(), ports.RuntimeHandle{ID: "fake:host-1/agent-1"}); err == nil {
		t.Fatal("other backend handle accepted by Paseo runtime")
	}
	if len(client.calls) != 0 {
		t.Fatalf("client calls = %v", client.calls)
	}
}
