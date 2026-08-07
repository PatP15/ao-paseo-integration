// Package runtimerouter dispatches opaque local handles and namespaced remote
// handles through one runtime surface.
package runtimerouter

import (
	"context"
	"errors"
	"fmt"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/runtime/runtimeselect"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/runtimehandle"
)

var (
	// ErrRemoteRuntimeNotFound means no runtime is registered for the backend
	// and host encoded in a namespaced handle.
	ErrRemoteRuntimeNotFound = errors.New("remote execution runtime not found")
	// ErrRemoteCapabilityUnsupported marks local-only terminal operations.
	ErrRemoteCapabilityUnsupported = errors.New("remote runtime capability unsupported")
)

// RemoteResolver finds the runtime for one backend/host pair. The handle
// remains namespaced when passed to the selected runtime so the adapter can
// validate its own ownership boundary too.
type RemoteResolver func(domain.ExecutionBackendType, domain.ExecutionHostID) (ports.ExecutionRuntime, bool)

// Router preserves the complete local runtime surface while routing lifecycle,
// output, and message operations for namespaced handles to execution runtimes.
type Router struct {
	local   runtimeselect.Runtime
	resolve RemoteResolver
}

var _ runtimeselect.Runtime = (*Router)(nil)
var _ ports.RuntimeRestarter = (*Router)(nil)

// New wraps the selected platform runtime. A nil resolver is valid while no
// remote hosts are registered; local handles continue to route unchanged.
func New(local runtimeselect.Runtime, resolve RemoteResolver) *Router {
	return &Router{local: local, resolve: resolve}
}

// Create is always local. External backends provision and launch through
// ExecutionBackend before returning a namespaced handle.
func (r *Router) Create(ctx context.Context, cfg ports.RuntimeConfig) (ports.RuntimeHandle, error) {
	return r.local.Create(ctx, cfg)
}

// Destroy stops remote agents and delegates local teardown unchanged.
func (r *Router) Destroy(ctx context.Context, handle ports.RuntimeHandle) error {
	remote, found, err := r.remote(handle)
	if err != nil {
		return err
	}
	if found {
		return remote.Stop(ctx, handle)
	}
	return r.local.Destroy(ctx, handle)
}

// GetOutput returns output from the runtime that owns handle.
func (r *Router) GetOutput(ctx context.Context, handle ports.RuntimeHandle, lines int) (string, error) {
	remote, found, err := r.remote(handle)
	if err != nil {
		return "", err
	}
	if found {
		return remote.Output(ctx, handle, lines)
	}
	return r.local.GetOutput(ctx, handle, lines)
}

// IsAlive probes the runtime that owns handle.
func (r *Router) IsAlive(ctx context.Context, handle ports.RuntimeHandle) (bool, error) {
	remote, found, err := r.remote(handle)
	if err != nil {
		return false, err
	}
	if found {
		return remote.Alive(ctx, handle)
	}
	return r.local.IsAlive(ctx, handle)
}

// SendMessage delivers input through the runtime that owns handle.
func (r *Router) SendMessage(ctx context.Context, handle ports.RuntimeHandle, message string) error {
	remote, found, err := r.remote(handle)
	if err != nil {
		return err
	}
	if found {
		return remote.SendMessage(ctx, handle, message)
	}
	return r.local.SendMessage(ctx, handle, message)
}

// Attach remains local-only for the MVP. Session read models suppress remote
// handles, so the frontend never enters its attach/reconnect loop for them.
func (r *Router) Attach(ctx context.Context, handle ports.RuntimeHandle, rows, cols uint16) (ports.Stream, error) {
	if _, found, err := r.remote(handle); err != nil {
		return nil, err
	} else if found {
		return nil, fmt.Errorf("attach: %w", ErrRemoteCapabilityUnsupported)
	}
	return r.local.Attach(ctx, handle, rows, cols)
}

// Interrupt is not part of ExecutionRuntime; remote interaction uses
// SendMessage and explicit Stop instead.
func (r *Router) Interrupt(ctx context.Context, handle ports.RuntimeHandle) error {
	if _, found, err := r.remote(handle); err != nil {
		return err
	} else if found {
		return fmt.Errorf("interrupt: %w", ErrRemoteCapabilityUnsupported)
	}
	return r.local.Interrupt(ctx, handle)
}

// IsSupervisedProcessAlive preserves the optional capability discovered by
// the reaper's concrete-value assertion. Paseo exposes agent liveness, not a
// separate local supervisor-process identity.
func (r *Router) IsSupervisedProcessAlive(ctx context.Context, handle ports.RuntimeHandle, ref ports.SupervisedProcessRef) (bool, error) {
	if _, found, err := r.remote(handle); err != nil {
		return false, err
	} else if found {
		return false, fmt.Errorf("supervised process inspection: %w", ErrRemoteCapabilityUnsupported)
	}
	return r.local.IsSupervisedProcessAlive(ctx, handle, ref)
}

// Restart preserves the optional capability discovered by Session Manager's
// concrete-value assertion. When the platform runtime lacks native restart
// support (currently conpty), this performs the same Destroy+Create fallback
// Session Manager used before the router was introduced.
func (r *Router) Restart(ctx context.Context, handle ports.RuntimeHandle, cfg ports.RuntimeConfig) (ports.RuntimeHandle, error) {
	if _, found, err := r.remote(handle); err != nil {
		return ports.RuntimeHandle{}, err
	} else if found {
		return ports.RuntimeHandle{}, fmt.Errorf("restart: %w", ErrRemoteCapabilityUnsupported)
	}
	if restarter, ok := r.local.(ports.RuntimeRestarter); ok {
		return restarter.Restart(ctx, handle, cfg)
	}
	if err := r.local.Destroy(ctx, handle); err != nil {
		return ports.RuntimeHandle{}, err
	}
	return r.local.Create(ctx, cfg)
}

func (r *Router) remote(handle ports.RuntimeHandle) (ports.ExecutionRuntime, bool, error) {
	parts, namespaced, err := runtimehandle.Parse(handle)
	if err != nil {
		return nil, true, err
	}
	if !namespaced {
		return nil, false, nil
	}
	if r.resolve == nil {
		return nil, true, fmt.Errorf("%w: %s/%s", ErrRemoteRuntimeNotFound, parts.Backend, parts.HostID)
	}
	remote, ok := r.resolve(parts.Backend, parts.HostID)
	if !ok || remote == nil {
		return nil, true, fmt.Errorf("%w: %s/%s", ErrRemoteRuntimeNotFound, parts.Backend, parts.HostID)
	}
	return remote, true, nil
}
