package activity

import (
	"context"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/codex"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// A remotely executed session reuses a local harness value on purpose — the
// sessions.harness CHECK must not be widened — so the harness resolves to an
// adapter whose TerminalActivityDetector is a tmux-pane regex. Pointed at a
// remote agent's transcript it will match sooner or later and write a bogus
// ActivityIdle over a session that is actively working. The observer must not
// read the handle at all.
func TestPollSkipsRemotelyExecutedSession(t *testing.T) {
	now := time.Unix(500, 0).UTC()
	session := activeSession(now, domain.HarnessCodex)
	session.Metadata.RuntimeHandleID = "paseo:host-1/agt_abc"

	sink := &fakeSink{}
	// Output that the codex detector reads as idle, so a session that reaches
	// the probe would definitely be marked idle.
	runtime := &fakeRuntime{output: "› Write tests for @filename\n\ngpt-5.6-sol low · ~/project\n"}
	observer := New(
		fakeSessions{rows: []domain.SessionRecord{session}},
		sink,
		runtime,
		fakeAgents{domain.HarnessCodex: codex.New()},
		Config{Clock: func() time.Time { return now }, Logger: testLogger()},
	)

	if err := observer.Poll(context.Background()); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if runtime.calls != 0 {
		t.Errorf("GetOutput calls = %d, want 0 for a remote handle", runtime.calls)
	}
	if len(sink.signals) != 0 {
		t.Errorf("signals = %+v, want none: local terminal heuristics say nothing about a remote agent", sink.signals)
	}
}

// The local path is untouched: the same stale session with an ordinary handle
// still reconciles to idle.
func TestPollStillReconcilesLocalSession(t *testing.T) {
	now := time.Unix(500, 0).UTC()
	session := activeSession(now, domain.HarnessCodex)

	sink := &fakeSink{}
	runtime := &fakeRuntime{output: "› Write tests for @filename\n\ngpt-5.6-sol low · ~/project\n"}
	observer := New(
		fakeSessions{rows: []domain.SessionRecord{session}},
		sink,
		runtime,
		fakeAgents{domain.HarnessCodex: codex.New()},
		Config{Clock: func() time.Time { return now }, Logger: testLogger()},
	)

	if err := observer.Poll(context.Background()); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if runtime.calls != 1 {
		t.Errorf("GetOutput calls = %d, want 1", runtime.calls)
	}
	if len(sink.signals) != 1 || sink.signals[0].State != domain.ActivityIdle {
		t.Fatalf("signals = %+v, want one idle signal", sink.signals)
	}
}
