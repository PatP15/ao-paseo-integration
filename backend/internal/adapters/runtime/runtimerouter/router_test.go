package runtimerouter

import (
	"context"
	"errors"
	"io"
	"reflect"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/runtimehandle"
)

type fakeLocalRuntime struct {
	calls []string
}

func (r *fakeLocalRuntime) Create(context.Context, ports.RuntimeConfig) (ports.RuntimeHandle, error) {
	r.calls = append(r.calls, "create")
	return ports.RuntimeHandle{ID: "local-created"}, nil
}
func (r *fakeLocalRuntime) Destroy(context.Context, ports.RuntimeHandle) error {
	r.calls = append(r.calls, "destroy")
	return nil
}
func (r *fakeLocalRuntime) GetOutput(context.Context, ports.RuntimeHandle, int) (string, error) {
	r.calls = append(r.calls, "output")
	return "local output", nil
}
func (r *fakeLocalRuntime) IsAlive(context.Context, ports.RuntimeHandle) (bool, error) {
	r.calls = append(r.calls, "alive")
	return true, nil
}
func (r *fakeLocalRuntime) Attach(context.Context, ports.RuntimeHandle, uint16, uint16) (ports.Stream, error) {
	r.calls = append(r.calls, "attach")
	return fakeStream{}, nil
}
func (r *fakeLocalRuntime) Interrupt(context.Context, ports.RuntimeHandle) error {
	r.calls = append(r.calls, "interrupt")
	return nil
}
func (r *fakeLocalRuntime) SendMessage(context.Context, ports.RuntimeHandle, string) error {
	r.calls = append(r.calls, "send")
	return nil
}
func (r *fakeLocalRuntime) IsSupervisedProcessAlive(context.Context, ports.RuntimeHandle, ports.SupervisedProcessRef) (bool, error) {
	r.calls = append(r.calls, "supervised")
	return true, nil
}

type fakeRestartableLocal struct{ fakeLocalRuntime }

func (r *fakeRestartableLocal) Restart(context.Context, ports.RuntimeHandle, ports.RuntimeConfig) (ports.RuntimeHandle, error) {
	r.calls = append(r.calls, "restart")
	return ports.RuntimeHandle{ID: "local-restarted"}, nil
}

type fakeRemoteRuntime struct {
	calls   []string
	handles []string
}

func (r *fakeRemoteRuntime) record(operation string, handle ports.RuntimeHandle) {
	r.calls = append(r.calls, operation)
	r.handles = append(r.handles, handle.ID)
}
func (r *fakeRemoteRuntime) Stop(_ context.Context, handle ports.RuntimeHandle) error {
	r.record("stop", handle)
	return nil
}
func (r *fakeRemoteRuntime) Alive(_ context.Context, handle ports.RuntimeHandle) (bool, error) {
	r.record("alive", handle)
	return true, nil
}
func (r *fakeRemoteRuntime) Output(_ context.Context, handle ports.RuntimeHandle, _ int) (string, error) {
	r.record("output", handle)
	return "remote output", nil
}
func (r *fakeRemoteRuntime) SendMessage(_ context.Context, handle ports.RuntimeHandle, _ string) error {
	r.record("send", handle)
	return nil
}

type fakeStream struct{}

func (fakeStream) Read([]byte) (int, error)    { return 0, io.EOF }
func (fakeStream) Write(p []byte) (int, error) { return len(p), nil }
func (fakeStream) Close() error                { return nil }
func (fakeStream) Resize(uint16, uint16) error { return nil }

func TestRouterLeavesLocalHandlesOnSelectedRuntime(t *testing.T) {
	local := &fakeRestartableLocal{}
	router := New(local, func(domain.ExecutionBackendType, domain.ExecutionHostID) (ports.ExecutionRuntime, bool) {
		t.Fatal("resolver called for local handle")
		return nil, false
	})
	ctx := context.Background()
	handle := ports.RuntimeHandle{ID: "local-handle"}
	cfg := ports.RuntimeConfig{SessionID: "session-1"}

	if _, err := router.Create(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if err := router.Destroy(ctx, handle); err != nil {
		t.Fatal(err)
	}
	if _, err := router.GetOutput(ctx, handle, 10); err != nil {
		t.Fatal(err)
	}
	if _, err := router.IsAlive(ctx, handle); err != nil {
		t.Fatal(err)
	}
	if _, err := router.Attach(ctx, handle, 24, 80); err != nil {
		t.Fatal(err)
	}
	if err := router.Interrupt(ctx, handle); err != nil {
		t.Fatal(err)
	}
	if err := router.SendMessage(ctx, handle, "hello"); err != nil {
		t.Fatal(err)
	}
	if _, err := router.IsSupervisedProcessAlive(ctx, handle, ports.SupervisedProcessRef{}); err != nil {
		t.Fatal(err)
	}
	if _, err := router.Restart(ctx, handle, cfg); err != nil {
		t.Fatal(err)
	}

	want := []string{"create", "destroy", "output", "alive", "attach", "interrupt", "send", "supervised", "restart"}
	if !reflect.DeepEqual(local.calls, want) {
		t.Fatalf("calls = %v, want %v", local.calls, want)
	}
}

func TestRouterDispatchesRemoteLifecycleByNamespaceAndHost(t *testing.T) {
	local := &fakeRestartableLocal{}
	remote := &fakeRemoteRuntime{}
	router := New(local, func(backend domain.ExecutionBackendType, hostID domain.ExecutionHostID) (ports.ExecutionRuntime, bool) {
		if backend == domain.ExecutionBackendPaseo && hostID == "worker-1" {
			return remote, true
		}
		return nil, false
	})
	handle, err := runtimehandle.New(domain.ExecutionBackendPaseo, "worker-1", "agent-1")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := router.Destroy(ctx, handle); err != nil {
		t.Fatal(err)
	}
	if alive, err := router.IsAlive(ctx, handle); err != nil || !alive {
		t.Fatalf("alive=%v err=%v", alive, err)
	}
	if output, err := router.GetOutput(ctx, handle, 20); err != nil || output != "remote output" {
		t.Fatalf("output=%q err=%v", output, err)
	}
	if err := router.SendMessage(ctx, handle, "continue"); err != nil {
		t.Fatal(err)
	}

	wantCalls := []string{"stop", "alive", "output", "send"}
	if !reflect.DeepEqual(remote.calls, wantCalls) {
		t.Fatalf("remote calls = %v, want %v", remote.calls, wantCalls)
	}
	for _, got := range remote.handles {
		if got != handle.ID {
			t.Fatalf("remote received stripped handle %q, want %q", got, handle.ID)
		}
	}
	if len(local.calls) != 0 {
		t.Fatalf("local calls = %v", local.calls)
	}
}

func TestRouterPreservesTypeAssertedCapabilities(t *testing.T) {
	router := New(&fakeRestartableLocal{}, nil)
	if _, ok := any(router).(ports.SupervisedProcessInspector); !ok {
		t.Fatal("router dropped ports.SupervisedProcessInspector")
	}
	if _, ok := any(router).(ports.RuntimeRestarter); !ok {
		t.Fatal("router dropped ports.RuntimeRestarter")
	}
}

func TestRouterRestartFallsBackWhenLocalRuntimeCannotRestart(t *testing.T) {
	local := &fakeLocalRuntime{}
	router := New(local, nil)
	got, err := router.Restart(context.Background(), ports.RuntimeHandle{ID: "local"}, ports.RuntimeConfig{SessionID: "session-1"})
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "local-created" || !reflect.DeepEqual(local.calls, []string{"destroy", "create"}) {
		t.Fatalf("handle=%q calls=%v", got.ID, local.calls)
	}
}

func TestRouterRefusesUnknownMalformedAndUnsupportedRemoteHandles(t *testing.T) {
	router := New(&fakeRestartableLocal{}, nil)
	ctx := context.Background()
	if _, err := router.IsAlive(ctx, ports.RuntimeHandle{ID: "paseo:worker-1/agent-1"}); !errors.Is(err, ErrRemoteRuntimeNotFound) {
		t.Fatalf("unknown remote error = %v", err)
	}
	if err := router.Destroy(ctx, ports.RuntimeHandle{ID: "paseo:malformed"}); err == nil {
		t.Fatal("malformed remote handle fell through to local runtime")
	}

	remote := &fakeRemoteRuntime{}
	router = New(&fakeRestartableLocal{}, func(domain.ExecutionBackendType, domain.ExecutionHostID) (ports.ExecutionRuntime, bool) {
		return remote, true
	})
	handle := ports.RuntimeHandle{ID: "paseo:worker-1/agent-1"}
	if _, err := router.Attach(ctx, handle, 24, 80); !errors.Is(err, ErrRemoteCapabilityUnsupported) {
		t.Fatalf("attach error = %v", err)
	}
	if err := router.Interrupt(ctx, handle); !errors.Is(err, ErrRemoteCapabilityUnsupported) {
		t.Fatalf("interrupt error = %v", err)
	}
	if _, err := router.Restart(ctx, handle, ports.RuntimeConfig{}); !errors.Is(err, ErrRemoteCapabilityUnsupported) {
		t.Fatalf("restart error = %v", err)
	}
}
