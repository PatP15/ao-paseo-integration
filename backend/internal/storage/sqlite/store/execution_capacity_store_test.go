package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
)

// seedCapacitySession creates a session and binds it to the seeded worker host,
// returning the session id. terminated decides whether the session is already
// over, which is what the boot-reconcile query keys on.
func seedCapacitySession(t *testing.T, s *sqlite.Store, name string, terminated bool) domain.SessionID {
	t.Helper()
	ctx := context.Background()
	at := time.Now().UTC().Truncate(time.Second)
	rec, err := s.CreateSession(ctx, domain.SessionRecord{
		ProjectID: "project", Kind: domain.KindWorker, Harness: domain.HarnessClaudeCode,
		DisplayName: name, CreatedAt: at, UpdatedAt: at,
	})
	if err != nil {
		t.Fatalf("create session %s: %v", name, err)
	}
	if terminated {
		rec.IsTerminated = true
		rec.Activity = domain.Activity{State: domain.ActivityExited, LastActivityAt: at}
		if err := s.UpdateSession(ctx, rec); err != nil {
			t.Fatalf("terminate session %s: %v", name, err)
		}
	}
	if err := s.UpsertSessionExecutionBinding(ctx, domain.SessionExecutionBinding{
		SessionID: rec.ID, BackendType: domain.ExecutionBackendPaseo, HostID: "worker-1",
		WorkspaceTitle: name, ExternalAgentID: domain.ExecutionAgentID("agent-" + name), CreatedAt: at,
	}); err != nil {
		t.Fatalf("bind session %s: %v", name, err)
	}
	return rec.ID
}

// TestArchiveSessionExecutionBindingFreesCapacityWithoutLosingHistory is the F-B
// store contract. Archiving is what gives a computer its slot back, and it must
// do that without erasing where the session ran: the board keeps a remote badge
// on an ended session, and its remote pane keeps rendering.
func TestArchiveSessionExecutionBindingFreesCapacityWithoutLosingHistory(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "project")
	seedObservedHost(t, s, "server-1")
	live := seedCapacitySession(t, s, "live", false)
	dead := seedCapacitySession(t, s, "dead", true)

	active, err := s.ListActiveSessionExecutionBindingsByHost(ctx, "worker-1")
	if err != nil {
		t.Fatalf("list active: %v", err)
	}
	if len(active) != 2 {
		t.Fatalf("active bindings before the archive = %d, want 2", len(active))
	}

	at := time.Now().UTC().Truncate(time.Second)
	archived, err := s.ArchiveSessionExecutionBinding(ctx, dead, at)
	if err != nil {
		t.Fatalf("archive: %v", err)
	}
	if !archived {
		t.Fatal("the first archive of a live binding must report that it did the work")
	}
	// Idempotent: a terminate racing the boot reconcile must not double-count or
	// rewrite the archive time.
	again, err := s.ArchiveSessionExecutionBinding(ctx, dead, at.Add(time.Hour))
	if err != nil {
		t.Fatalf("re-archive: %v", err)
	}
	if again {
		t.Fatal("re-archiving an archived binding must report no work done")
	}

	active, err = s.ListActiveSessionExecutionBindingsByHost(ctx, "worker-1")
	if err != nil {
		t.Fatalf("list active after archive: %v", err)
	}
	if len(active) != 1 || active[0].SessionID != live {
		t.Fatalf("active bindings after the archive = %+v, want only %s", active, live)
	}

	// History legs: the display read and the per-session read both still answer.
	all, err := s.ListSessionExecutionBindings(ctx)
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("display bindings = %d, want both the live and the ended session", len(all))
	}
	binding, found, err := s.GetSessionExecutionBinding(ctx, dead)
	if err != nil || !found {
		t.Fatalf("get archived binding: found=%v err=%v", found, err)
	}
	if !binding.ArchivedAt.Equal(at) || binding.HostID != "worker-1" {
		t.Fatalf("archived binding = %#v, want the archive time and its host kept", binding)
	}
}

// TestListActiveSessionExecutionBindingsForTerminatedSessionsFindsOnlyLeaks
// pins the boot-reconcile set: exactly the live bindings whose session already
// ended. A live session's binding must never appear, or a boot would archive a
// working remote agent's slot and let a second agent be routed onto it.
func TestListActiveSessionExecutionBindingsForTerminatedSessionsFindsOnlyLeaks(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "project")
	seedObservedHost(t, s, "server-1")
	seedCapacitySession(t, s, "live", false)
	dead := seedCapacitySession(t, s, "dead", true)
	alreadyArchived := seedCapacitySession(t, s, "already", true)
	if _, err := s.ArchiveSessionExecutionBinding(ctx, alreadyArchived, time.Now().UTC()); err != nil {
		t.Fatalf("pre-archive: %v", err)
	}

	leaked, err := s.ListActiveSessionExecutionBindingsForTerminatedSessions(ctx)
	if err != nil {
		t.Fatalf("list leaked: %v", err)
	}
	if len(leaked) != 1 || leaked[0].SessionID != dead {
		t.Fatalf("leaked bindings = %+v, want only %s", leaked, dead)
	}
}
