package paseo

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestCaptureTerminalArgvIsTheCursoredReadAndNothingElse(t *testing.T) {
	args, err := terminalCaptureArgs("worker:6767", "term-1", 0, 200)
	if err != nil {
		t.Fatalf("build capture args: %v", err)
	}
	want := []string{"terminal", "capture", "--host", "worker:6767", "term-1", "--start", "0", "--end", "200", "--json"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("args = %v, want %v", args, want)
	}
}

func TestIngestArgvNeverNarrowsTheRead(t *testing.T) {
	// Each of these loses data rather than reducing it: a filter renumbers
	// entries so previously separate messages get spliced together, a tail drops
	// the oldest entries instead of the newest, and follow mode throws away all
	// history when its two-second history fetch times out. A read that narrows is
	// a read that can silently miss a report AO has never seen.
	banned := map[string]bool{"--filter": true, "--tail": true, "-f": true, "--follow": true, "--since": true}

	logs, err := logsArgs("worker:6767", "agent-1")
	if err != nil {
		t.Fatalf("build logs args: %v", err)
	}
	if !reflect.DeepEqual(logs, []string{"logs", "--host", "worker:6767", "agent-1"}) {
		t.Fatalf("logs args = %v, want a flagless full read", logs)
	}
	capture, err := terminalCaptureArgs("worker:6767", "term-1", 10, 20)
	if err != nil {
		t.Fatalf("build capture args: %v", err)
	}
	for _, args := range [][]string{logs, capture} {
		for _, arg := range args {
			if banned[arg] {
				t.Fatalf("ingest argv %v carries %q", args, arg)
			}
		}
	}
}

func TestCaptureTerminalRefusesAnUnusableRangeOrTerminal(t *testing.T) {
	store := newMemoryExecutionStore(nil)
	backend := newBackend(newFakeExecutionClient(nil), store, func() time.Time { return backendTestNow })
	ctx := context.Background()

	for name, run := range map[string]func() error{
		"no terminal": func() error {
			_, err := backend.CaptureTerminal(ctx, "host-1", "", 0, 10)
			return err
		},
		"empty range": func() error {
			_, err := backend.CaptureTerminal(ctx, "host-1", "term-1", 10, 10)
			return err
		},
		"negative start": func() error {
			_, err := backend.CaptureTerminal(ctx, "host-1", "term-1", -1, 10)
			return err
		},
		"unregistered host": func() error {
			_, err := backend.CaptureTerminal(ctx, "host-9", "term-1", 0, 10)
			return err
		},
	} {
		if err := run(); err == nil {
			t.Fatalf("%s: want an error", name)
		}
	}
}

func TestCaptureTerminalRefusesAnotherTerminalsBytes(t *testing.T) {
	store := newMemoryExecutionStore(nil)
	client := newFakeExecutionClient(nil)
	client.capture = TerminalCapture{TerminalID: "term-other", Lines: []string{"x"}, TotalLines: 1}
	backend := newBackend(client, store, func() time.Time { return backendTestNow })

	// Accepting it would advance this session's cursor over lines it never owned.
	if _, err := backend.CaptureTerminal(context.Background(), "host-1", "term-1", 0, 10); err == nil {
		t.Fatal("want an error when the capture names a different terminal")
	}
}

func TestCaptureTerminalReturnsScreenLinesAndTheCursor(t *testing.T) {
	// Shaped after spike/fixtures/s1f-terminal-capture.json: hard-wrapped screen
	// lines and a monotonic totalLines.
	store := newMemoryExecutionStore(nil)
	client := newFakeExecutionClient(nil)
	client.capture = TerminalCapture{
		TerminalID: "term-1",
		Lines: []string{
			"", "The default interactive shell is now zsh.",
			strings.Repeat("x", 80), strings.Repeat("x", 73),
		},
		TotalLines: 30,
	}
	backend := newBackend(client, store, func() time.Time { return backendTestNow })

	window, err := backend.CaptureTerminal(context.Background(), "host-1", "term-1", 0, 200)
	if err != nil {
		t.Fatalf("capture: %v", err)
	}
	if window.TerminalID != "term-1" || window.TotalLines != 30 || len(window.Lines) != 4 {
		t.Fatalf("window = %#v", window)
	}
	if got := client.calls[len(client.calls)-1]; got != "capture:term-1:0:200" {
		t.Fatalf("last call = %q", got)
	}
}

func TestCaptureTerminalReportsAHostFailureAsAnError(t *testing.T) {
	store := newMemoryExecutionStore(nil)
	client := newFakeExecutionClient(nil)
	client.captureErr = errors.New("connection refused")
	backend := newBackend(client, store, func() time.Time { return backendTestNow })

	// An empty window is indistinguishable from a quiet agent, so an unreachable
	// host has to error rather than read as silence.
	if _, err := backend.CaptureTerminal(context.Background(), "host-1", "term-1", 0, 10); err == nil {
		t.Fatal("want the host failure surfaced")
	}
}

func TestTranscriptReadsTheWholeRenderedTimeline(t *testing.T) {
	store := newMemoryExecutionStore(nil)
	client := newFakeExecutionClient(nil)
	client.logs = "[User] do the work\nAO_EVENT_a1b2c3d4e5f6 001/001 00000000 e30=;\n"
	backend := newBackend(client, store, func() time.Time { return backendTestNow })
	ctx := context.Background()

	transcript, err := backend.Transcript(ctx, "host-1", "agent-1")
	if err != nil {
		t.Fatalf("transcript: %v", err)
	}
	if transcript != client.logs {
		t.Fatalf("transcript = %q, want the untrimmed full read", transcript)
	}
	if got := client.calls[len(client.calls)-1]; got != "logs:agent-1" {
		t.Fatalf("last call = %q", got)
	}
	if _, err := backend.Transcript(ctx, "host-1", ""); err == nil {
		t.Fatal("want an error without an agent id")
	}
	if _, err := backend.Transcript(ctx, "host-9", "agent-1"); err == nil {
		t.Fatal("want an error for an unregistered host")
	}
}
