package paseoreporter

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/paseoevent"
)

const reporterTestNonce = "a1b2c3d4e5f6"

func reporterTestEvent(eventType string, seq int) []byte {
	payload := `{"summary":"done"}`
	return []byte(fmt.Sprintf(
		`{"schema":"ao.agent-event.v1","eventId":"event-%d","sessionId":"session-1","launchId":"launch-1","seq":%d,"type":%q,"payload":%s}`,
		seq, seq, eventType, payload,
	))
}

func TestEmitAndServeProduceDecodableTerminalFrames(t *testing.T) {
	dataDir := t.TempDir()
	for seq, eventType := range []string{"checkpoint", "result"} {
		if err := Emit(dataDir, reporterTestEvent(eventType, seq+1)); err != nil {
			t.Fatalf("emit %s: %v", eventType, err)
		}
	}
	path, err := spoolPath(dataDir, "launch-1")
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	offset, pending, err := readAvailable(path, 0, nil, func(line []byte) error {
		return writeEvent(line, "session-1", "launch-1", reporterTestNonce, &output)
	})
	if err != nil {
		t.Fatalf("forward spool: %v", err)
	}
	if offset == 0 || len(pending) != 0 {
		t.Fatalf("cursor=(%d, %q), want the complete spool consumed", offset, pending)
	}
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	for _, line := range lines {
		if len(line) > 76 {
			t.Fatalf("frame is %d columns: %q", len(line), line)
		}
	}
	decoded := paseoevent.Decode(reporterTestNonce, lines)
	if decoded.Malformed != 0 || decoded.Incomplete != 0 || len(decoded.Payloads) != 2 {
		t.Fatalf("decoded = %#v", decoded)
	}
	for index, payload := range decoded.Payloads {
		event, err := paseoevent.DecodeEvent(payload.Data)
		if err != nil {
			t.Fatalf("decode event %d: %v", index, err)
		}
		if event.Seq != int64(index+1) || string(event.Type) != []string{"checkpoint", "result"}[index] {
			t.Fatalf("event %d = %#v", index, event)
		}
	}
}

func TestEmitRejectsInvalidEventsWithoutCreatingASpool(t *testing.T) {
	dataDir := t.TempDir()
	if err := Emit(dataDir, []byte(`{"schema":"wrong"}`)); err == nil {
		t.Fatal("invalid event was appended")
	}
	entries, err := newDirectoryEntries(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("invalid event created state: %v", entries)
	}
}

func TestServeRejectsAnotherLaunchBeforeWriting(t *testing.T) {
	var output bytes.Buffer
	err := writeEvent(reporterTestEvent("checkpoint", 1), "session-1", "launch-other", reporterTestNonce, &output)
	if err == nil || output.Len() != 0 {
		t.Fatalf("writeEvent = (%q, %v), want refusal before output", output.String(), err)
	}
}

func TestServeHonorsCancellationWhileTheSpoolIsQuiet(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := Serve(ctx, t.TempDir(), "session-1", "launch-1", reporterTestNonce, &bytes.Buffer{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Serve error = %v, want context cancellation", err)
	}
}

func newDirectoryEntries(path string) ([]string, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	result := make([]string, 0, len(entries))
	for _, entry := range entries {
		result = append(result, entry.Name())
	}
	return result, nil
}
