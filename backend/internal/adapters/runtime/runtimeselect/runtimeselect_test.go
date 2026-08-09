package runtimeselect

import (
	"runtime"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// TestNew_PreservesOptionalRuntimeCapabilities pins the two runtime capabilities
// that are discovered by TYPE ASSERTION on the concrete value rather than by a
// declared parameter type:
//
//	observe/reaper.New:                runtime.(ports.SupervisedProcessInspector)
//	session_manager.restartRuntime:    m.runtime.(ports.RuntimeRestarter)
//
// Because both are optional at the call site, a decorator that wraps the
// selected runtime — a multi-backend router, a tracing shim, a test double —
// compiles cleanly while silently answering "not supported" to both probes. The
// user-visible results are a local agent that renders "working" forever after
// its process exits inside a live pane, and a resume-agent that mints a fresh
// handle and drops every attached terminal client. Neither produces an error.
//
// SupervisedProcessInspector is additionally part of the Runtime union
// interface, so this test is belt-and-braces for it. RuntimeRestarter cannot be
// in the union (conpty legitimately lacks Restart, and restartRuntime's
// Destroy+Create fallback depends on the assertion failing), so for that
// capability this test is the only guard that exists.
func TestNew_PreservesOptionalRuntimeCapabilities(t *testing.T) {
	rt := New(nil)

	if _, ok := rt.(ports.SupervisedProcessInspector); !ok {
		t.Errorf("selected runtime (%T) does not implement ports.SupervisedProcessInspector; "+
			"observe/reaper will set workload=nil and stop detecting supervised process exits", rt)
	}

	// tmux implements Restart; conpty deliberately does not. Assert per platform
	// rather than unconditionally, so this test states the real contract instead
	// of a convenient one.
	_, restarter := rt.(ports.RuntimeRestarter)
	if runtime.GOOS == "windows" {
		if restarter {
			t.Log("note: the Windows runtime now implements ports.RuntimeRestarter; " +
				"confirm session_manager.restartRuntime's Destroy+Create fallback is still correct")
		}
		return
	}
	if !restarter {
		t.Errorf("selected runtime (%T) does not implement ports.RuntimeRestarter; "+
			"session_manager.restartRuntime will fall back to Destroy+Create, minting a new "+
			"handle and dropping attached terminal clients on every resume", rt)
	}
}

// TestNew_ReturnsUnionInterface guards the union itself. If a future change
// narrows Runtime, or wraps it in something that satisfies fewer methods, the
// daemon's direct wiring of Interrupt/SendMessage/GetOutput/Attach breaks at
// runtime rather than here.
func TestNew_ReturnsUnionInterface(t *testing.T) {
	// New's signature already returns Runtime, so the union is enforced at
	// compile time; this guards the runtime half — that it hands back a usable
	// value rather than a typed nil.
	rt := New(nil)
	if rt == nil {
		t.Fatal("New returned nil")
	}
	if _, ok := rt.(ports.Attacher); !ok {
		t.Errorf("selected runtime (%T) does not implement ports.Attacher; "+
			"the terminal layer cannot open a Stream against it", rt)
	}
}
