package workitem

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
)

type fakeStore struct {
	items map[string]domain.WorkItem
	err   error
}

func (f *fakeStore) UpsertWorkItem(_ context.Context, item domain.WorkItem) error {
	if f.err != nil {
		return f.err
	}
	f.items[item.ID] = item
	return nil
}

func (f *fakeStore) SetWorkItemApproval(_ context.Context, id, approver string, at time.Time) (domain.WorkItem, bool, error) {
	if f.err != nil {
		return domain.WorkItem{}, false, f.err
	}
	item, ok := f.items[id]
	if !ok || (item.ApprovalState != domain.WorkItemDraft && item.ApprovalState != domain.WorkItemProposed) {
		return domain.WorkItem{}, false, nil
	}
	item.ApprovalState = domain.WorkItemApproved
	item.ApprovedBy = approver
	item.ApprovedAt = at
	item.UpdatedAt = at
	f.items[id] = item
	return item, true, nil
}

func (f *fakeStore) ListWorkItemsByProject(_ context.Context, projectID domain.ProjectID) ([]domain.WorkItem, error) {
	if f.err != nil {
		return nil, f.err
	}
	var out []domain.WorkItem
	for _, item := range f.items {
		if item.ProjectID == projectID {
			out = append(out, item)
		}
	}
	return out, nil
}

func (f *fakeStore) GetWorkItem(_ context.Context, id string) (domain.WorkItem, bool, error) {
	if f.err != nil {
		return domain.WorkItem{}, false, f.err
	}
	item, ok := f.items[id]
	return item, ok, nil
}

func TestCreateDefaultsToDraftOpenAndGeneratesID(t *testing.T) {
	now := time.Date(2026, 8, 8, 12, 0, 0, 0, time.FixedZone("KST", 9*60*60))
	store := &fakeStore{items: map[string]domain.WorkItem{}}
	svc := newService(store, func() time.Time { return now }, func() string { return "wi_test" })

	item, err := svc.Create(context.Background(), CreateInput{
		ProjectID: " project ", Title: " Ship G1 ", Body: " usable dispatch ",
		AcceptanceCriteria: []string{" CLI works ", ""},
	})
	if err != nil {
		t.Fatal(err)
	}
	if item.ID != "wi_test" || item.ProjectID != "project" || item.Title != "Ship G1" {
		t.Fatalf("created item = %#v", item)
	}
	if item.ApprovalState != domain.WorkItemDraft || item.LifecycleFact != domain.WorkItemOpen {
		t.Fatalf("created state = %s/%s, want draft/open", item.ApprovalState, item.LifecycleFact)
	}
	if item.Priority != 100 || item.RiskLevel != "normal" || item.CreatedByType != "human" {
		t.Fatalf("created defaults = %#v", item)
	}
	if !reflect.DeepEqual(item.AcceptanceCriteria, []string{"CLI works"}) {
		t.Fatalf("acceptance criteria = %#v", item.AcceptanceCriteria)
	}
	if !item.CreatedAt.Equal(now.UTC()) || !item.UpdatedAt.Equal(now.UTC()) {
		t.Fatalf("timestamps = %s/%s", item.CreatedAt, item.UpdatedAt)
	}
}

func TestApproveStampsHumanFactAndCannotRestamp(t *testing.T) {
	now := time.Date(2026, 8, 8, 3, 0, 0, 0, time.UTC)
	store := &fakeStore{items: map[string]domain.WorkItem{
		"wi_1": {ID: "wi_1", ApprovalState: domain.WorkItemProposed},
	}}
	svc := newService(store, func() time.Time { return now }, func() string { return "unused" })

	item, err := svc.Approve(context.Background(), "wi_1", " operator ")
	if err != nil {
		t.Fatal(err)
	}
	if item.ApprovalState != domain.WorkItemApproved || item.ApprovedBy != "operator" || !item.ApprovedAt.Equal(now) {
		t.Fatalf("approved item = %#v", item)
	}
	_, err = svc.Approve(context.Background(), "wi_1", "someone-else")
	var apiError *apierr.Error
	if !errors.As(err, &apiError) || apiError.Code != "WORK_ITEM_NOT_APPROVABLE" || apiError.Kind != apierr.KindConflict {
		t.Fatalf("second approval error = %#v", err)
	}
}

func TestGetMissingWorkItemIsNotFound(t *testing.T) {
	svc := newService(&fakeStore{items: map[string]domain.WorkItem{}}, time.Now, func() string { return "unused" })
	_, err := svc.Get(context.Background(), "missing")
	var apiError *apierr.Error
	if !errors.As(err, &apiError) || apiError.Code != "WORK_ITEM_NOT_FOUND" || apiError.Kind != apierr.KindNotFound {
		t.Fatalf("Get error = %#v", err)
	}
}
