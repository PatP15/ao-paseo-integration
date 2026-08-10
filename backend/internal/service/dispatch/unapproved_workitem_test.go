package dispatch

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestDispatchStillRefusesCreatedDraftWorkItem(t *testing.T) {
	now := time.Date(2026, 8, 8, 3, 0, 0, 0, time.UTC)
	store := newDispatchTestStore(t, now)
	if err := store.UpsertWorkItem(context.Background(), domain.WorkItem{
		ID: "work-1", ProjectID: "project", Title: "Draft work",
		ApprovalState: domain.WorkItemDraft, LifecycleFact: domain.WorkItemOpen,
		CreatedByType: "human", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	_, err := New(store).Dispatch(context.Background(), testDispatchRequest())
	if err == nil || !strings.Contains(err.Error(), "is not approved") {
		t.Fatalf("Dispatch error = %v, want unapproved refusal", err)
	}
	// The sentinel is what keeps this refusal a 409 with its reason instead of
	// an opaque 500, so it belongs in the assertion, not just in the message.
	if !errors.Is(err, domain.ErrDispatchRefused) {
		t.Fatalf("Dispatch error = %v, want it to wrap domain.ErrDispatchRefused", err)
	}
}

func TestDispatchRefusesRejectedWorkItem(t *testing.T) {
	now := time.Date(2026, 8, 8, 3, 0, 0, 0, time.UTC)
	store := newDispatchTestStore(t, now)
	if err := store.UpsertWorkItem(context.Background(), domain.WorkItem{
		ID: "work-1", ProjectID: "project", Title: "Rejected work",
		ApprovalState: domain.WorkItemRejected, LifecycleFact: domain.WorkItemOpen,
		CreatedByType: "human", ApprovedBy: "operator", ApprovedAt: now,
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	_, err := New(store).Dispatch(context.Background(), testDispatchRequest())
	if err == nil || !strings.Contains(err.Error(), "is not approved") {
		t.Fatalf("Dispatch error = %v, want rejected refusal", err)
	}
	if !errors.Is(err, domain.ErrDispatchRefused) {
		t.Fatalf("Dispatch error = %v, want it to wrap domain.ErrDispatchRefused", err)
	}
}
