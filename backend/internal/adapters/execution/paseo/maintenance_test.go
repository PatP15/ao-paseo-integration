package paseo

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/paseoevent"
)

func b64(content []byte) string { return base64.StdEncoding.EncodeToString(content) }

func asMaintenanceRefused(err error, target **MaintenanceRefusedError) bool {
	return errors.As(err, target)
}

const maintenanceTestNonce = "00aabbccddee"

// fakeMaintenanceClient scripts the terminal round trip: the capture returns
// pre-framed worker output once the command has been sent.
type fakeMaintenanceClient struct {
	*fakeExecutionClient
	workspaceID  string
	created      []string
	archived     []string
	killed       []string
	sentCommands []string
	captureLines []string
}

func (c *fakeMaintenanceClient) CreateLocalWorkspace(_ context.Context, title string) (Workspace, error) {
	c.created = append(c.created, title)
	return Workspace{WorkspaceID: c.workspaceID, Name: title, Isolation: "local", Cwd: "/home/worker"}, nil
}

func (c *fakeMaintenanceClient) ArchiveWorkspace(_ context.Context, workspaceID string) error {
	c.archived = append(c.archived, workspaceID)
	return nil
}

func (c *fakeMaintenanceClient) CreateTerminal(_ context.Context, req TerminalCreateRequest) (Terminal, error) {
	return Terminal{ID: "term-1", Name: req.Name, Cwd: req.Cwd}, nil
}

func (c *fakeMaintenanceClient) SendTerminalKeys(_ context.Context, terminalID string, keys ...string) error {
	c.sentCommands = append(c.sentCommands, strings.Join(keys, " "))
	return nil
}

func (c *fakeMaintenanceClient) CaptureTerminal(_ context.Context, terminalID string, start, end int) (TerminalCapture, error) {
	return TerminalCapture{TerminalID: terminalID, Lines: c.captureLines, TotalLines: len(c.captureLines)}, nil
}

func (c *fakeMaintenanceClient) KillTerminal(_ context.Context, terminalID string) error {
	c.killed = append(c.killed, terminalID)
	return nil
}

func withFixedNonce(t *testing.T) {
	t.Helper()
	previous := newMaintenanceNonce
	newMaintenanceNonce = func() (string, error) { return maintenanceTestNonce, nil }
	t.Cleanup(func() { newMaintenanceNonce = previous })
}

func framedLines(t *testing.T, emit func(out *bytes.Buffer) error) []string {
	t.Helper()
	var out bytes.Buffer
	if err := emit(&out); err != nil {
		t.Fatal(err)
	}
	// Interleave shell noise around the frames the way a real capture does.
	lines := []string{"$ ao-paseo-reporter maintain ...", "some shell noise"}
	lines = append(lines, strings.Split(strings.TrimRight(out.String(), "\n"), "\n")...)
	return append(lines, "$")
}

func TestHostInventoryDrivesTheChannelAndArchivesTheWorkspace(t *testing.T) {
	withFixedNonce(t)
	client := &fakeMaintenanceClient{
		fakeExecutionClient: newFakeExecutionClient(nil),
		workspaceID:         "wks-maint",
		captureLines: framedLines(t, func(out *bytes.Buffer) error {
			if err := paseoevent.WriteMaintenanceEvent(out, maintenanceTestNonce, 1, paseoevent.MaintenanceSkill,
				paseoevent.MaintenanceSkillPayload{Name: "deploy", Description: "Deploy safely"}); err != nil {
				return err
			}
			return paseoevent.WriteMaintenanceEvent(out, maintenanceTestNonce, 2, paseoevent.MaintenanceDone,
				paseoevent.MaintenanceDonePayload{Count: 1})
		}),
	}
	backend := newBackend(client, newMemoryExecutionStore(nil), func() time.Time { return backendTestNow })

	skills, err := backend.HostInventory(context.Background(), "host-1")
	if err != nil || len(skills) != 1 || skills[0].Name != "deploy" || skills[0].Description != "Deploy safely" {
		t.Fatalf("HostInventory = (%#v, %v)", skills, err)
	}
	if len(client.archived) != 1 || client.archived[0] != "wks-maint" {
		t.Fatalf("workspace not archived: %v", client.archived)
	}
	if len(client.sentCommands) != 1 || !strings.Contains(client.sentCommands[0], "maintain inventory") ||
		!strings.Contains(client.sentCommands[0], maintenanceTestNonce) {
		t.Fatalf("sent command = %v", client.sentCommands)
	}
}

func TestHostPrefsAssemblesChunksAndVerifiesTheHash(t *testing.T) {
	withFixedNonce(t)
	content := []byte(`{"providers":{"impl":"codex/gpt-5.4"},"preferences":["` + strings.Repeat("x", 1500) + `"]}`)
	client := &fakeMaintenanceClient{
		fakeExecutionClient: newFakeExecutionClient(nil),
		workspaceID:         "wks-maint",
		captureLines: framedLines(t, func(out *bytes.Buffer) error {
			// Reuse the worker's own emitter so the test cannot drift from it.
			return emitTestPrefs(out, maintenanceTestNonce, content)
		}),
	}
	backend := newBackend(client, newMemoryExecutionStore(nil), func() time.Time { return backendTestNow })

	prefs, err := backend.HostPrefs(context.Background(), "host-1")
	if err != nil {
		t.Fatal(err)
	}
	if prefs.Content != string(content) || !prefs.Exists || prefs.SHA256 != sha256HexOf(content) {
		t.Fatalf("prefs = %+v", prefs)
	}
}

func TestMaintenanceRefusalSurfacesTheWorkerMessage(t *testing.T) {
	withFixedNonce(t)
	client := &fakeMaintenanceClient{
		fakeExecutionClient: newFakeExecutionClient(nil),
		workspaceID:         "wks-maint",
		captureLines: framedLines(t, func(out *bytes.Buffer) error {
			return paseoevent.WriteMaintenanceEvent(out, maintenanceTestNonce, 1, paseoevent.MaintenanceError,
				paseoevent.MaintenanceErrorPayload{Message: "drift: re-read before writing"})
		}),
	}
	backend := newBackend(client, newMemoryExecutionStore(nil), func() time.Time { return backendTestNow })

	_, err := backend.WriteHostPrefs(context.Background(), "host-1", []byte(`{}`), sha256HexOf(nil))
	var refused *MaintenanceRefusedError
	if !asMaintenanceRefused(err, &refused) || !strings.Contains(refused.Message, "drift") {
		t.Fatalf("err = %v", err)
	}
	if len(client.archived) != 1 {
		t.Fatalf("workspace not archived after refusal: %v", client.archived)
	}
}

// emitTestPrefs mirrors the worker's prefs emission using the shared codec.
func emitTestPrefs(out *bytes.Buffer, nonce string, content []byte) error {
	seq := 0
	chunks := paseoevent.SplitPrefsChunks(content)
	for index, chunk := range chunks {
		seq++
		if err := paseoevent.WriteMaintenanceEvent(out, nonce, seq, paseoevent.MaintenancePrefs,
			paseoevent.MaintenancePrefsPayload{Part: index + 1, ContentB64: b64(chunk)}); err != nil {
			return err
		}
	}
	seq++
	return paseoevent.WriteMaintenanceEvent(out, nonce, seq, paseoevent.MaintenanceDone,
		paseoevent.MaintenanceDonePayload{Parts: len(chunks), SHA256: sha256HexOf(content), Exists: true})
}
