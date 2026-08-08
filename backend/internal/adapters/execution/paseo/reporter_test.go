package paseo

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/paseoevent"
)

type fakeReportClient struct {
	*fakeExecutionClient
	terminal       Terminal
	createRequests []TerminalCreateRequest
	sends          [][]string
	onSend         func()
}

func newFakeReportClient() *fakeReportClient {
	return &fakeReportClient{
		fakeExecutionClient: newFakeExecutionClient(nil),
		terminal: Terminal{
			ID: "terminal-1", Name: reporterTerminalName("launch-1"), Cwd: "/remote/worktree",
		},
	}
}

func (c *fakeReportClient) CreateTerminal(_ context.Context, req TerminalCreateRequest) (Terminal, error) {
	c.record("create-terminal")
	c.createRequests = append(c.createRequests, req)
	return c.terminal, nil
}

func (c *fakeReportClient) SendTerminalKeys(_ context.Context, id string, keys ...string) error {
	c.record("send-terminal:" + id)
	c.sends = append(c.sends, append([]string(nil), keys...))
	if c.onSend != nil {
		c.onSend()
	}
	return nil
}

func provisionedReportBackend(t *testing.T, client *fakeReportClient, store *memoryExecutionStore) *Backend {
	t.Helper()
	backend := newBackend(client, store, func() time.Time { return backendTestNow })
	if _, err := backend.Provision(context.Background(), provisionRequest()); err != nil {
		t.Fatalf("provision: %v", err)
	}
	return backend
}

func TestPrepareReportTransportPersistsTerminalBeforeStartingReporter(t *testing.T) {
	store := newMemoryExecutionStore(nil)
	client := newFakeReportClient()
	backend := provisionedReportBackend(t, client, store)
	client.onSend = func() {
		if got := store.bindings["session-1"].TerminalID; got != "terminal-1" {
			t.Fatalf("send-keys began before terminal id was durable: %q", got)
		}
	}

	err := backend.PrepareReportTransport(context.Background(), "session-1", "wks-1", "launch-1", "a1b2c3d4e5f6")
	if err != nil {
		t.Fatal(err)
	}
	if len(client.createRequests) != 1 {
		t.Fatalf("create requests = %v", client.createRequests)
	}
	request := client.createRequests[0]
	if request.WorkspaceID != "wks-1" || request.Cwd != "/remote/worktree" || request.Name != reporterTerminalName("launch-1") {
		t.Fatalf("create request = %#v", request)
	}
	if len(client.sends) != 1 || len(client.sends[0]) != 3 || client.sends[0][0] != "C-c" || client.sends[0][2] != "Enter" {
		t.Fatalf("send keys = %#v", client.sends)
	}
	command := client.sends[0][1]
	for _, want := range []string{paseoevent.ReporterBinary, "serve", "session-1", "launch-1", "a1b2c3d4e5f6"} {
		if !strings.Contains(command, want) {
			t.Fatalf("reporter command %q is missing %q", command, want)
		}
	}
	if strings.ContainsAny(command, "\r\n") {
		t.Fatalf("reporter command contains a line break: %q", command)
	}
}

func TestPrepareReportTransportReusesTheDurableTerminal(t *testing.T) {
	store := newMemoryExecutionStore(nil)
	client := newFakeReportClient()
	backend := provisionedReportBackend(t, client, store)
	ctx := context.Background()

	for range 2 {
		if err := backend.PrepareReportTransport(ctx, "session-1", "wks-1", "launch-1", "a1b2c3d4e5f6"); err != nil {
			t.Fatal(err)
		}
	}
	if len(client.createRequests) != 1 || len(client.sends) != 2 {
		t.Fatalf("creates=%d sends=%d, want one terminal and a restart on replay", len(client.createRequests), len(client.sends))
	}
}

func TestReporterTerminalCommandsPinHostWorkspaceAndLiteralKeys(t *testing.T) {
	create, err := terminalCreateArgs("worker:6767", TerminalCreateRequest{
		WorkspaceID: "wks-1", Cwd: "/remote/worktree", Name: "ao-reporter-123",
	})
	if err != nil {
		t.Fatal(err)
	}
	wantCreate := []string{
		"terminal", "create", "--host", "worker:6767", "--workspace", "wks-1",
		"--cwd", "/remote/worktree", "--name", "ao-reporter-123", "--json",
	}
	if !reflect.DeepEqual(create, wantCreate) {
		t.Fatalf("create args = %v, want %v", create, wantCreate)
	}
	send, err := terminalSendKeysArgs("worker:6767", "terminal-1", []string{"C-c", "reporter command", "Enter"})
	if err != nil {
		t.Fatal(err)
	}
	wantSend := []string{
		"terminal", "send-keys", "--host", "worker:6767", "terminal-1", "C-c", "reporter command", "Enter",
	}
	if !reflect.DeepEqual(send, wantSend) {
		t.Fatalf("send args = %v, want %v", send, wantSend)
	}
}

func TestTerminalCreateFixtureMatchesThePinnedPaseoShape(t *testing.T) {
	terminal, err := decodeStrict[Terminal](fixture(t, "s1f-terminal-create.json"))
	if err != nil {
		t.Fatal(err)
	}
	if terminal.ID == "" || terminal.Name != "ao-spike" || terminal.Cwd == "" {
		t.Fatalf("terminal = %#v", terminal)
	}
}
