// Package runtimeselect picks the correct runtime backend by platform:
// tmux on Darwin/Linux, conpty (ConPTY) on Windows.
package runtimeselect

import (
	"context"
	"log/slog"
	"runtime"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/runtime/conpty"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/runtime/tmux"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// Runtime is the union interface that both tmux and conpty satisfy.
// It extends ports.Runtime (Create/Destroy/IsAlive) with the additional methods
// the daemon wires directly, including ports.Attacher (Attach) so the terminal
// layer can open a Stream against the selected runtime.
//
// ports.SupervisedProcessInspector is part of the union on purpose. It is
// discovered by TYPE ASSERTION on the concrete value, not by a declared
// parameter type — observe/reaper's New does:
//
//	if workload, ok := runtime.(ports.SupervisedProcessInspector); ok { … }
//
// so any decorator that wraps a Runtime without re-implementing this method
// silently sets r.workload = nil. The reaper then stops producing
// Runtime=ProbeAlive/Workload=ProbeDead facts, and a local agent whose process
// exits inside a still-live tmux pane renders as "working" forever. There is no
// compile error and no failing assertion at the call site, so keeping it in the
// union is what makes that regression impossible rather than merely unlikely.
//
// ports.RuntimeRestarter is deliberately NOT in the union: it is a genuine
// per-platform capability (tmux has Restart, conpty does not), and
// session_manager.restartRuntime falls back to Destroy+Create when the
// assertion fails. Widening the union would force conpty to grow a stub, and a
// stub returning "unsupported" would break that fallback. It is guarded by
// TestNew_PreservesOptionalRuntimeCapabilities instead.
type Runtime interface {
	ports.Runtime // Create, Destroy, IsAlive
	ports.Attacher
	ports.SupervisedProcessInspector
	Interrupt(ctx context.Context, handle ports.RuntimeHandle) error
	SendMessage(ctx context.Context, handle ports.RuntimeHandle, message string) error
	GetOutput(ctx context.Context, handle ports.RuntimeHandle, lines int) (string, error)
}

// Compile-time assertions: both adapters must implement the union interface.
var _ Runtime = (*tmux.Runtime)(nil)
var _ Runtime = (*conpty.Runtime)(nil)

// New returns the per-platform runtime: tmux on Darwin/Linux, conpty on Windows.
// log is accepted for signature stability with callers but is currently unused.
func New(_ *slog.Logger) Runtime {
	if runtime.GOOS != "windows" {
		return tmux.New(tmux.Options{})
	}
	return conpty.New(conpty.Options{})
}
