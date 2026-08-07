package execpolicy

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

type fakeBindings struct {
	binding domain.SessionExecutionBinding
	found   bool
	err     error
	calls   int
}

func (f *fakeBindings) GetSessionExecutionBinding(context.Context, domain.SessionID) (domain.SessionExecutionBinding, bool, error) {
	f.calls++
	return f.binding, f.found, f.err
}

func TestIsRemoteHandle(t *testing.T) {
	cases := []struct {
		name   string
		handle string
		want   bool
	}{
		{"empty handle is not remote", "", false},
		{"local tmux handle", "ao-mer-1", false},
		{"local shellterm handle", "shellterm-0123456789abcdef", false},
		{"namespaced paseo handle", "paseo:host-1/agt_abc", true},
		// A prefixed-but-malformed handle must never be read as local: it was
		// minted for an execution backend and a local code path would act on
		// a machine that does not own the agent.
		{"malformed namespaced handle is still remote", "paseo:", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsRemoteHandle(tc.handle); got != tc.want {
				t.Errorf("IsRemoteHandle(%q) = %v, want %v", tc.handle, got, tc.want)
			}
		})
	}
}

func TestRemoteFromHandleNeedsNoBindingLookup(t *testing.T) {
	bindings := &fakeBindings{}
	meta := domain.SessionMetadata{RuntimeHandleID: "paseo:host-1/agt_abc"}

	remote, err := Remote(context.Background(), bindings, "mer-1", meta)
	if err != nil {
		t.Fatalf("Remote: %v", err)
	}
	if !remote {
		t.Error("remote = false, want true for a namespaced handle")
	}
	if bindings.calls != 0 {
		t.Errorf("binding lookups = %d, want 0: the handle already answers", bindings.calls)
	}
}

// The dispatched-but-not-yet-launched window is the whole reason the binding is
// consulted: no runtime handle exists yet, and WorkspacePath is empty, so a
// handle-only check would report "local" at exactly the moment a local fallback
// would land in the operator's own checkout.
func TestRemoteFromBindingBeforeLaunch(t *testing.T) {
	bindings := &fakeBindings{
		binding: domain.SessionExecutionBinding{BackendType: domain.ExecutionBackendPaseo},
		found:   true,
	}

	remote, err := Remote(context.Background(), bindings, "mer-1", domain.SessionMetadata{})
	if err != nil {
		t.Fatalf("Remote: %v", err)
	}
	if !remote {
		t.Error("remote = false, want true for a session bound to a paseo host before launch")
	}
}

func TestRemoteIsFalseForLocalSessions(t *testing.T) {
	cases := []struct {
		name     string
		bindings any
		meta     domain.SessionMetadata
	}{
		{
			name:     "no binding row",
			bindings: &fakeBindings{},
			meta:     domain.SessionMetadata{RuntimeHandleID: "ao-mer-1"},
		},
		{
			name: "binding names the local backend",
			bindings: &fakeBindings{
				binding: domain.SessionExecutionBinding{BackendType: domain.ExecutionBackendLocal},
				found:   true,
			},
			meta: domain.SessionMetadata{RuntimeHandleID: "ao-mer-1"},
		},
		{
			name:     "dependency does not implement BindingSource",
			bindings: struct{}{},
			meta:     domain.SessionMetadata{RuntimeHandleID: "ao-mer-1"},
		},
		{
			name:     "nil dependency",
			bindings: nil,
			meta:     domain.SessionMetadata{RuntimeHandleID: "ao-mer-1"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			remote, err := Remote(context.Background(), tc.bindings, "mer-1", tc.meta)
			if err != nil {
				t.Fatalf("Remote: %v", err)
			}
			if remote {
				t.Error("remote = true, want false")
			}
		})
	}
}

// An unknown answer must surface as an error. Reporting "local" on a failed
// lookup would route the caller into the local path these refusals exist to
// block, which is the unsafe direction to fail in.
func TestRemoteReturnsBindingLookupFailure(t *testing.T) {
	sentinel := errors.New("database is locked")
	bindings := &fakeBindings{err: sentinel}

	remote, err := Remote(context.Background(), bindings, "mer-1", domain.SessionMetadata{})
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want it to wrap the lookup failure", err)
	}
	if remote {
		t.Error("remote = true alongside an error; callers must read the error, not the bool")
	}
}

func TestRefuseCarriesSentinelAndContext(t *testing.T) {
	err := Refuse("code review", "mer-1")

	if !errors.Is(err, ErrRemoteUnsupported) {
		t.Fatalf("errors.Is(err, ErrRemoteUnsupported) = false for %v", err)
	}
	if !strings.Contains(err.Error(), "code review") {
		t.Errorf("err = %q, want it to name the refused feature", err)
	}
	if !strings.Contains(err.Error(), "mer-1") {
		t.Errorf("err = %q, want it to name the session", err)
	}
}
