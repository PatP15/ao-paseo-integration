package execution

import (
	"context"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func storeWithSession(t *testing.T) *fakeStore {
	t.Helper()
	store := newFakeStore()
	store.sessions = map[domain.SessionID]domain.SessionRecord{
		"project-1": {ID: "project-1", ProjectID: "project"},
	}
	return store
}

func TestSendSessionMessageQueuesTheTextAndNamesTheSender(t *testing.T) {
	store := storeWithSession(t)
	service := newTestService(store)

	command, err := service.SendSessionMessage(context.Background(), SendMessageInput{
		SessionID: "project-1", Message: "  also update the changelog  ", SentBy: "operator",
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if command.Type != domain.ExecutionCommandSendMessage || command.State != domain.ExecutionCommandPending {
		t.Fatalf("command = %#v, want a pending send_message", command)
	}
	queued := store.enqueuedMessage
	if queued.Message != "also update the changelog" {
		t.Fatalf("queued message = %q, want it trimmed", queued.Message)
	}
	if queued.SentBy != "operator" {
		t.Fatalf("sentBy = %q, want the caller's name", queued.SentBy)
	}
	if queued.SessionID != "project-1" || queued.CommandID == "" || queued.EventID == "" {
		t.Fatalf("queued = %#v", queued)
	}
}

func TestSendSessionMessageDefaultsTheSenderToHuman(t *testing.T) {
	store := storeWithSession(t)
	if _, err := newTestService(store).SendSessionMessage(context.Background(), SendMessageInput{
		SessionID: "project-1", Message: "carry on",
	}); err != nil {
		t.Fatal(err)
	}
	if store.enqueuedMessage.SentBy != "human" {
		t.Fatalf("sentBy = %q, want human", store.enqueuedMessage.SentBy)
	}
}

func TestSendSessionMessageRefusesWhatTheHostCannotDeliver(t *testing.T) {
	tests := []struct {
		name    string
		in      SendMessageInput
		want    string
		prepare func(*fakeStore)
	}{
		{
			name: "no session id",
			in:   SendMessageInput{Message: "hi"},
			want: "SESSION_ID_REQUIRED",
		},
		{
			name: "blank message",
			in:   SendMessageInput{SessionID: "project-1", Message: "   "},
			want: "MESSAGE_REQUIRED",
		},
		{
			name: "over the length cap",
			in:   SendMessageInput{SessionID: "project-1", Message: strings.Repeat("a", MaxAnswerLen+1)},
			want: "MESSAGE_TOO_LONG",
		},
		{
			// A line break submits at the agent's prompt, so the remainder would
			// arrive as a second, meaningless turn.
			name: "multi-line message",
			in:   SendMessageInput{SessionID: "project-1", Message: "first line\nsecond line"},
			want: "MESSAGE_SINGLE_LINE",
		},
		{
			name: "unknown session",
			in:   SendMessageInput{SessionID: "project-9", Message: "hi"},
			want: "SESSION_NOT_FOUND",
		},
		{
			// The whole point of the endpoint: a local session has no remote agent,
			// and a command queued for it would never be drained.
			name:    "session with no execution binding",
			in:      SendMessageInput{SessionID: "project-1", Message: "hi"},
			want:    "SESSION_NOT_REMOTE",
			prepare: func(store *fakeStore) { store.enqueueErr = domain.ErrSessionNotRemote },
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := storeWithSession(t)
			if test.prepare != nil {
				test.prepare(store)
			}
			_, err := newTestService(store).SendSessionMessage(context.Background(), test.in)
			if got := errCode(t, err); got != test.want {
				t.Fatalf("code = %q, want %q", got, test.want)
			}
			if store.enqueuedMessage.CommandID != "" {
				t.Fatalf("refused message was still queued: %#v", store.enqueuedMessage)
			}
		})
	}
}
