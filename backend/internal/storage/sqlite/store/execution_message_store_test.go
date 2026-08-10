package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestEnqueueSessionMessageCommitsCommandAndTimelineEventTogether(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	at := time.Date(2026, time.August, 10, 14, 0, 0, 0, time.UTC)
	sessionID := seedBoundSession(t, s, at)

	command, err := s.EnqueueExecutionSessionMessage(ctx, domain.ExecutionSessionMessage{
		CommandID: "message-1", EventID: "event-1", SessionID: sessionID,
		Message: "check the flaky test first", SentBy: "operator", SentAt: at,
	})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if command.Type != domain.ExecutionCommandSendMessage || command.State != domain.ExecutionCommandPending {
		t.Fatalf("command = %#v, want a pending send_message", command)
	}
	if command.HostID != "worker-1" {
		t.Fatalf("host = %q, want the session's bound host", command.HostID)
	}
	// The message queues behind the start_agent the dispatch committed.
	if command.Sequence < 2 {
		t.Fatalf("sequence = %d, want it after the launch command", command.Sequence)
	}
	var payload domain.ExecutionMessagePayload
	if err := json.Unmarshal([]byte(command.PayloadJSON), &payload); err != nil {
		t.Fatalf("decode payload %q: %v", command.PayloadJSON, err)
	}
	if payload.Message != "check the flaky test first" || payload.QuestionID != "" {
		t.Fatalf("payload = %#v, want the bare message", payload)
	}

	events, err := s.ListSessionExecutionEvents(ctx, sessionID, "", 50)
	if err != nil {
		t.Fatal(err)
	}
	var found *domain.ExecutionEventRecord
	for i := range events {
		if events[i].ID == "event-1" {
			found = &events[i]
		}
	}
	if found == nil {
		t.Fatalf("timeline has no event for the queued message: %#v", events)
	}
	if found.EventType != domain.ExecutionSessionMessageSent || found.Transport != domain.ExecutionEventOutbox {
		t.Fatalf("event = %#v, want a session_message_sent on the outbox transport", *found)
	}
	var shown domain.ExecutionSessionMessageEvent
	if err := json.Unmarshal([]byte(found.PayloadJSON), &shown); err != nil {
		t.Fatalf("decode event payload %q: %v", found.PayloadJSON, err)
	}
	if shown.Message != "check the flaky test first" || shown.SentBy != "operator" || shown.CommandID != "message-1" {
		t.Fatalf("event payload = %#v", shown)
	}
}

func TestEnqueueSessionMessageRefusesASessionWithNoExecutionBinding(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	at := time.Date(2026, time.August, 10, 15, 0, 0, 0, time.UTC)
	seedProject(t, s, "project")
	local, err := s.CreateSession(ctx, domain.SessionRecord{
		ProjectID: "project", Kind: domain.KindWorker, Harness: domain.HarnessClaudeCode,
		DisplayName: "Local work", CreatedAt: at, UpdatedAt: at,
	})
	if err != nil {
		t.Fatalf("create local session: %v", err)
	}

	_, err = s.EnqueueExecutionSessionMessage(ctx, domain.ExecutionSessionMessage{
		CommandID: "message-1", EventID: "event-1", SessionID: local.ID,
		Message: "hello", SentBy: "operator", SentAt: at,
	})
	if !errors.Is(err, domain.ErrSessionNotRemote) {
		t.Fatalf("err = %v, want ErrSessionNotRemote", err)
	}
	events, err := s.ListSessionExecutionEvents(ctx, local.ID, "", 50)
	if err != nil || len(events) != 0 {
		t.Fatalf("refused message left a timeline event: (%#v, %v)", events, err)
	}
}

func TestEnqueueSessionMessageIsIdempotentOnTheCommandID(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	at := time.Date(2026, time.August, 10, 16, 0, 0, 0, time.UTC)
	sessionID := seedBoundSession(t, s, at)
	message := domain.ExecutionSessionMessage{
		CommandID: "message-1", EventID: "event-1", SessionID: sessionID,
		Message: "one send only", SentBy: "operator", SentAt: at,
	}
	if _, err := s.EnqueueExecutionSessionMessage(ctx, message); err != nil {
		t.Fatal(err)
	}
	// A retried POST must collide rather than send the agent the same text twice.
	if _, err := s.EnqueueExecutionSessionMessage(ctx, message); err == nil {
		t.Fatal("replayed message enqueued a second time")
	}
	commands, err := s.ListExecutionCommandsBySession(ctx, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	sends := 0
	for _, command := range commands {
		if command.Type == domain.ExecutionCommandSendMessage {
			sends++
		}
	}
	if sends != 1 {
		t.Fatalf("send_message commands = %d, want 1", sends)
	}
}
