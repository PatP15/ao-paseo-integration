package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestWorkItemApprovalAndProjectList(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "project")
	now := time.Date(2026, 8, 8, 3, 0, 0, 0, time.UTC)
	for _, item := range []domain.WorkItem{
		{ID: "later", ProjectID: "project", Title: "Later", ApprovalState: domain.WorkItemDraft, LifecycleFact: domain.WorkItemOpen, Priority: 20, CreatedByType: "human", CreatedAt: now, UpdatedAt: now},
		{ID: "first", ProjectID: "project", Title: "First", ApprovalState: domain.WorkItemProposed, LifecycleFact: domain.WorkItemOpen, Priority: 10, CreatedByType: "human", CreatedAt: now, UpdatedAt: now},
	} {
		if err := s.UpsertWorkItem(ctx, item); err != nil {
			t.Fatal(err)
		}
	}

	approvedAt := now.Add(time.Minute)
	approved, changed, err := s.SetWorkItemApproval(ctx, "first", "operator", "", domain.WorkItemApproved, approvedAt)
	if err != nil || !changed {
		t.Fatalf("approve = (%#v, %v, %v)", approved, changed, err)
	}
	if approved.ApprovalState != domain.WorkItemApproved || approved.ApprovedBy != "operator" || !approved.ApprovedAt.Equal(approvedAt) {
		t.Fatalf("approved item = %#v", approved)
	}
	if _, changed, err := s.SetWorkItemApproval(ctx, "first", "other", "", domain.WorkItemApproved, approvedAt.Add(time.Minute)); err != nil || changed {
		t.Fatalf("second approval changed=%v err=%v", changed, err)
	}

	rejected, changed, err := s.SetWorkItemApproval(ctx, "later", "operator", "scope belongs to G2", domain.WorkItemRejected, approvedAt)
	if err != nil || !changed {
		t.Fatalf("reject = (%#v, %v, %v)", rejected, changed, err)
	}
	if rejected.ApprovalState != domain.WorkItemRejected || rejected.ApprovedBy != "operator" || !rejected.ApprovedAt.Equal(approvedAt) {
		t.Fatalf("rejected item = %#v", rejected)
	}
	// The reason is the whole content of a rejection for anyone who did not make
	// it, so it has to survive the round trip.
	if rejected.DecisionNote != "scope belongs to G2" {
		t.Fatalf("decision note = %q, want the reason the decider gave", rejected.DecisionNote)
	}
	reread, found, err := s.GetWorkItem(ctx, "later")
	if err != nil || !found || reread.DecisionNote != "scope belongs to G2" {
		t.Fatalf("re-read rejected item = (%#v, %v, %v)", reread, found, err)
	}
	if _, changed, err := s.SetWorkItemApproval(ctx, "later", "other", "", domain.WorkItemApproved, approvedAt.Add(time.Minute)); err != nil || changed {
		t.Fatalf("approval after rejection changed=%v err=%v", changed, err)
	}
	if _, _, err := s.SetWorkItemApproval(ctx, "later", "other", "", domain.WorkItemDraft, approvedAt); err == nil {
		t.Fatal("draft decision should be refused")
	}

	items, err := s.ListWorkItemsByProject(ctx, "project")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].ID != "first" || items[1].ID != "later" {
		t.Fatalf("listed items = %#v", items)
	}
}
