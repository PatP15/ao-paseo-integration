package dispatch

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
)

var errHostUnreachable = errors.New("host unreachable")

// messagingBackend is an execution backend that also implements the post-launch
// runtime surface, which is what the outbox needs to deliver a message.
type messagingBackend struct {
	*idempotentBackend
	mu       sync.Mutex
	handles  []string
	messages []string
	sendErr  error
}

func newMessagingBackend() *messagingBackend {
	return &messagingBackend{idempotentBackend: newIdempotentBackend()}
}

func (b *messagingBackend) SendMessage(_ context.Context, handle ports.RuntimeHandle, message string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.sendErr != nil {
		return b.sendErr
	}
	b.handles = append(b.handles, handle.ID)
	b.messages = append(b.messages, message)
	return nil
}

func (b *messagingBackend) Stop(context.Context, ports.RuntimeHandle) error { return nil }

func (b *messagingBackend) Alive(context.Context, ports.RuntimeHandle) (bool, error) {
	return true, nil
}

func (b *messagingBackend) Output(context.Context, ports.RuntimeHandle, int) (string, error) {
	return "", nil
}

func (b *messagingBackend) sent() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.messages...)
}

// dispatchAndLaunch runs one approved work item all the way to an acknowledged
// start_agent, which is the only state in which a message has an agent to go to.
func dispatchAndLaunch(t *testing.T, store *sqlite.Store, backend *messagingBackend, now time.Time) domain.SessionID {
	t.Helper()
	ctx := context.Background()
	dispatched, err := New(store).Dispatch(ctx, testDispatchRequest())
	if err != nil {
		t.Fatal(err)
	}
	worker := newTestWorker(store, backend, now)
	if delivered, err := worker.DeliverOne(ctx); err != nil || !delivered {
		t.Fatalf("start delivery = (%v, %v)", delivered, err)
	}
	return dispatched.Session.ID
}

func newTestWorker(store *sqlite.Store, backend ports.ExecutionBackend, now time.Time) *Worker {
	worker := NewWorker(store, BackendResolverFunc(func(domain.ExecutionHostID) (ports.ExecutionBackend, bool) {
		return backend, true
	}))
	worker.now = func() time.Time { return now }
	return worker
}

func enqueueMessage(t *testing.T, store *sqlite.Store, sessionID domain.SessionID, id, payload string, now time.Time) domain.ExecutionCommand {
	t.Helper()
	command, err := store.EnqueueExecutionCommand(context.Background(), domain.ExecutionCommand{
		ID: id, SessionID: sessionID, HostID: "host", Type: domain.ExecutionCommandSendMessage,
		PayloadJSON: payload, IdempotencyKey: "send_message:" + id, CreatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	return command
}

func TestOutboxDeliversAFreeFormMessageToTheSessionsAgent(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, time.August, 10, 9, 0, 0, 0, time.UTC)
	store := newDispatchTestStore(t, now)
	backend := newMessagingBackend()
	sessionID := dispatchAndLaunch(t, store, backend, now)

	enqueueMessage(t, store, sessionID, "message-1", `{"message":"tighten the retry test"}`, now)
	worker := newTestWorker(store, backend, now)
	if delivered, err := worker.DeliverOne(ctx); err != nil || !delivered {
		t.Fatalf("message delivery = (%v, %v)", delivered, err)
	}

	sent := backend.sent()
	if len(sent) != 1 || sent[0] != "tighten the retry test" {
		t.Fatalf("delivered messages = %v", sent)
	}
	session, _, err := store.GetSession(ctx, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(backend.handles) != 1 || backend.handles[0] != session.Metadata.RuntimeHandleID {
		t.Fatalf("handles = %v, want the session's runtime handle %q", backend.handles, session.Metadata.RuntimeHandleID)
	}
	command, found, err := store.GetExecutionCommand(ctx, "message-1")
	if err != nil || !found || command.State != domain.ExecutionCommandAcknowledged {
		t.Fatalf("command = (%+v, %v, %v), want acknowledged", command, found, err)
	}
}

func TestOutboxDeliversAnAnswerPayloadThroughTheSameDecoder(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, time.August, 10, 9, 30, 0, 0, time.UTC)
	store := newDispatchTestStore(t, now)
	backend := newMessagingBackend()
	sessionID := dispatchAndLaunch(t, store, backend, now)

	// The human-inbox answer path writes questionId alongside the message. One
	// command type must keep exactly one decoder, so that shape delivers too.
	enqueueMessage(t, store, sessionID, "answer-1", `{"questionId":"q-1","message":"yes, proceed"}`, now)
	if delivered, err := newTestWorker(store, backend, now).DeliverOne(ctx); err != nil || !delivered {
		t.Fatalf("answer delivery = (%v, %v)", delivered, err)
	}
	if sent := backend.sent(); len(sent) != 1 || sent[0] != "yes, proceed" {
		t.Fatalf("delivered messages = %v", sent)
	}
}

func TestOutboxRetriesAMessageWhenTheHostRefusesIt(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, time.August, 10, 10, 0, 0, 0, time.UTC)
	store := newDispatchTestStore(t, now)
	backend := newMessagingBackend()
	sessionID := dispatchAndLaunch(t, store, backend, now)

	enqueueMessage(t, store, sessionID, "message-1", `{"message":"still there?"}`, now)
	backend.sendErr = errHostUnreachable
	delivered, err := newTestWorker(store, backend, now).DeliverOne(ctx)
	if !delivered || !errors.Is(err, errHostUnreachable) {
		t.Fatalf("delivery = (%v, %v), want the host failure surfaced", delivered, err)
	}
	command, found, err := store.GetExecutionCommand(ctx, "message-1")
	if err != nil || !found || command.State != domain.ExecutionCommandPending {
		t.Fatalf("command = (%+v, %v, %v), want pending for retry", command, found, err)
	}
	if command.LastError == "" {
		t.Fatal("retried command recorded no last error")
	}

	// The same row must still deliver once the host comes back: nothing about a
	// refused message is consumed by the failed attempt.
	backend.sendErr = nil
	later := now.Add(time.Hour)
	if delivered, err := newTestWorker(store, backend, later).DeliverOne(ctx); err != nil || !delivered {
		t.Fatalf("retry delivery = (%v, %v)", delivered, err)
	}
	if sent := backend.sent(); len(sent) != 1 || sent[0] != "still there?" {
		t.Fatalf("delivered messages after retry = %v", sent)
	}
}

func TestOutboxFailsAMessageItCannotDecode(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, time.August, 10, 11, 0, 0, 0, time.UTC)
	tests := []struct {
		name    string
		payload string
	}{
		{name: "empty message", payload: `{"message":""}`},
		{name: "unknown field", payload: `{"message":"hi","prompt":"hi"}`},
		{name: "trailing json", payload: `{"message":"hi"} {"message":"again"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := newDispatchTestStore(t, now)
			backend := newMessagingBackend()
			sessionID := dispatchAndLaunch(t, store, backend, now)
			enqueueMessage(t, store, sessionID, "message-1", test.payload, now)

			delivered, err := newTestWorker(store, backend, now).DeliverOne(ctx)
			if !delivered || err == nil {
				t.Fatalf("delivery = (%v, %v), want a decode failure", delivered, err)
			}
			if sent := backend.sent(); len(sent) != 0 {
				t.Fatalf("undecodable payload reached the host: %v", sent)
			}
			command, found, err := store.GetExecutionCommand(ctx, "message-1")
			if err != nil || !found || command.State != domain.ExecutionCommandFailed {
				t.Fatalf("command = (%+v, %v, %v), want failed", command, found, err)
			}
		})
	}
}

func TestOutboxHoldsAMessageUntilTheAgentHasARuntimeHandle(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, time.August, 10, 12, 0, 0, 0, time.UTC)
	store := newDispatchTestStore(t, now)
	backend := newMessagingBackend()
	dispatched, err := New(store).Dispatch(ctx, testDispatchRequest())
	if err != nil {
		t.Fatal(err)
	}
	// No start_agent has been delivered, so the session carries no handle yet.
	// The queue orders the message behind the launch, so this is only reachable
	// by delivering the row directly — which is exactly the race being asserted.
	queued := enqueueMessage(t, store, dispatched.Session.ID, "message-1", `{"message":"early"}`, now)

	delivered, err := newTestWorker(store, backend, now).deliverMessage(ctx, queued)
	if !delivered || err == nil {
		t.Fatalf("delivery = (%v, %v), want the missing handle surfaced", delivered, err)
	}
	if sent := backend.sent(); len(sent) != 0 {
		t.Fatalf("message reached the host before launch: %v", sent)
	}
	command, found, err := store.GetExecutionCommand(ctx, "message-1")
	if err != nil || !found || command.State != domain.ExecutionCommandPending {
		t.Fatalf("command = (%+v, %v, %v), want pending so the launch can win the race", command, found, err)
	}
}
