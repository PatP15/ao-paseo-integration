package session

import (
	"context"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestSessionReadModelSuppressesNamespacedTerminalHandle(t *testing.T) {
	store := newFakeStore()
	store.sessions["remote-1"] = domain.SessionRecord{
		ID: "remote-1", ProjectID: "project-1",
		Metadata: domain.SessionMetadata{RuntimeHandleID: "paseo:worker-1/agent-1"},
	}
	store.sessions["local-1"] = domain.SessionRecord{
		ID: "local-1", ProjectID: "project-1",
		Metadata: domain.SessionMetadata{RuntimeHandleID: "local-terminal-1"},
	}

	sessions, err := (&Service{store: store}).List(context.Background(), ListFilter{ProjectID: "project-1"})
	if err != nil {
		t.Fatal(err)
	}
	byID := make(map[domain.SessionID]domain.Session, len(sessions))
	for _, session := range sessions {
		byID[session.ID] = session
	}
	if got := byID["remote-1"].TerminalHandleID; got != "" {
		t.Fatalf("remote terminal handle = %q, want suppressed", got)
	}
	if got := byID["local-1"].TerminalHandleID; got != "local-terminal-1" {
		t.Fatalf("local terminal handle = %q, want unchanged", got)
	}
}
