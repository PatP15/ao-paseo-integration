package daemon

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
)

// TestReconcileExecutionCapacityOnBootReleasesOnlyEndedSessions is the boot leg
// of F-B. MarkTerminated releases a remote slot as a session ends, but a daemon
// killed between the durable terminate and that hook — or upgraded from a build
// with no hook at all — leaves the binding live and the computer permanently one
// slot smaller. Boot repairs exactly those, and never a running session's slot.
func TestReconcileExecutionCapacityOnBootReleasesOnlyEndedSessions(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	at := time.Now().UTC().Truncate(time.Second)
	if err := store.UpsertProject(ctx, domain.ProjectRecord{
		ID: "project", Path: t.TempDir(), RegisteredAt: at,
	}); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	if err := store.UpsertExecutionHost(ctx, domain.ExecutionHost{
		ID: "worker", Name: "Worker", BackendType: domain.ExecutionBackendPaseo,
		Transport: domain.ExecutionTransportLAN, Endpoint: "127.0.0.1:1",
		TrustZone: domain.ExecutionTrustZoneHobby, Enabled: true, MaxConcurrentSessions: 2,
		CreatedAt: at, UpdatedAt: at,
	}, nil); err != nil {
		t.Fatalf("seed host: %v", err)
	}

	bind := func(name string, terminated bool) domain.SessionID {
		rec, err := store.CreateSession(ctx, domain.SessionRecord{
			ProjectID: "project", Kind: domain.KindWorker, Harness: domain.HarnessClaudeCode,
			DisplayName: name, CreatedAt: at, UpdatedAt: at,
		})
		if err != nil {
			t.Fatalf("create session %s: %v", name, err)
		}
		if terminated {
			rec.IsTerminated = true
			rec.Activity = domain.Activity{State: domain.ActivityExited, LastActivityAt: at}
			if err := store.UpdateSession(ctx, rec); err != nil {
				t.Fatalf("terminate session %s: %v", name, err)
			}
		}
		if err := store.UpsertSessionExecutionBinding(ctx, domain.SessionExecutionBinding{
			SessionID: rec.ID, BackendType: domain.ExecutionBackendPaseo, HostID: "worker",
			WorkspaceTitle: name, CreatedAt: at,
		}); err != nil {
			t.Fatalf("bind session %s: %v", name, err)
		}
		return rec.ID
	}
	live := bind("live", false)
	bind("leaked", true)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := reconcileExecutionCapacityOnBoot(ctx, store, logger); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	active, err := store.ListActiveSessionExecutionBindingsByHost(ctx, "worker")
	if err != nil {
		t.Fatalf("list active: %v", err)
	}
	if len(active) != 1 || active[0].SessionID != live {
		t.Fatalf("active bindings after boot = %+v, want only the live session %s", active, live)
	}

	// Idempotent across boots: a second pass finds nothing left to do and must
	// not disturb the live binding.
	if err := reconcileExecutionCapacityOnBoot(ctx, store, logger); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	active, err = store.ListActiveSessionExecutionBindingsByHost(ctx, "worker")
	if err != nil {
		t.Fatalf("list active after second pass: %v", err)
	}
	if len(active) != 1 || active[0].SessionID != live {
		t.Fatalf("active bindings after the second boot = %+v, want only %s", active, live)
	}
}
