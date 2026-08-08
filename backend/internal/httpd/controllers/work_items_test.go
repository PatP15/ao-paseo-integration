package controllers_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/config"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/controllers"
	workitemsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/workitem"
)

type fakeWorkItemService struct {
	created  workitemsvc.CreateInput
	approved struct{ id, by string }
	items    []domain.WorkItem
}

var _ controllers.WorkItemService = (*fakeWorkItemService)(nil)

func (f *fakeWorkItemService) Create(_ context.Context, in workitemsvc.CreateInput) (domain.WorkItem, error) {
	f.created = in
	return domain.WorkItem{
		ID: "wi_1", ProjectID: in.ProjectID, Title: in.Title,
		ApprovalState: domain.WorkItemDraft, LifecycleFact: domain.WorkItemOpen,
		Priority: 100, CreatedByType: "human", CreatedAt: time.Unix(100, 0).UTC(), UpdatedAt: time.Unix(100, 0).UTC(),
	}, nil
}

func (f *fakeWorkItemService) Approve(_ context.Context, id, by string) (domain.WorkItem, error) {
	f.approved.id, f.approved.by = id, by
	at := time.Unix(200, 0).UTC()
	return domain.WorkItem{
		ID: id, ProjectID: "project", Title: "Ship G1", ApprovalState: domain.WorkItemApproved,
		LifecycleFact: domain.WorkItemOpen, Priority: 100, CreatedByType: "human",
		ApprovedBy: by, ApprovedAt: at, CreatedAt: time.Unix(100, 0).UTC(), UpdatedAt: at,
	}, nil
}

func (f *fakeWorkItemService) List(_ context.Context, projectID domain.ProjectID) ([]domain.WorkItem, error) {
	if projectID != "project" {
		return nil, nil
	}
	return f.items, nil
}

func workItemRouter(t *testing.T, svc controllers.WorkItemService) http.Handler {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return httpd.NewRouterWithControl(config.Config{}, log, nil, httpd.APIDeps{WorkItems: svc}, httpd.ControlDeps{})
}

func TestWorkItemRoutesCreateApproveAndList(t *testing.T) {
	svc := &fakeWorkItemService{items: []domain.WorkItem{{
		ID: "wi_1", ProjectID: "project", Title: "Ship G1", ApprovalState: domain.WorkItemApproved,
		LifecycleFact: domain.WorkItemOpen, Priority: 100, CreatedByType: "human",
		CreatedAt: time.Unix(100, 0).UTC(), UpdatedAt: time.Unix(200, 0).UTC(),
	}}}
	router := workItemRouter(t, svc)

	create := httptest.NewRequest(http.MethodPost, "/api/v1/work-items", jsonBody(`{"projectId":"project","title":"Ship G1"}`))
	create.Header.Set("Content-Type", "application/json")
	created := httptest.NewRecorder()
	router.ServeHTTP(created, create)
	if created.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
	}
	var createOut controllers.WorkItemEnvelope
	if err := json.Unmarshal(created.Body.Bytes(), &createOut); err != nil {
		t.Fatal(err)
	}
	if createOut.WorkItem.ID != "wi_1" || createOut.WorkItem.ApprovalState != domain.WorkItemDraft || createOut.WorkItem.ApprovedAt != nil {
		t.Fatalf("create response = %#v", createOut)
	}

	approve := httptest.NewRequest(http.MethodPost, "/api/v1/work-items/wi_1/approval", jsonBody(`{"approver":"operator"}`))
	approve.Header.Set("Content-Type", "application/json")
	approved := httptest.NewRecorder()
	router.ServeHTTP(approved, approve)
	if approved.Code != http.StatusOK || svc.approved.id != "wi_1" || svc.approved.by != "operator" {
		t.Fatalf("approve status=%d call=%#v body=%s", approved.Code, svc.approved, approved.Body.String())
	}

	list := httptest.NewRecorder()
	router.ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/api/v1/work-items?projectId=project", nil))
	if list.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", list.Code, list.Body.String())
	}
	var listOut controllers.ListWorkItemsResponse
	if err := json.Unmarshal(list.Body.Bytes(), &listOut); err != nil {
		t.Fatal(err)
	}
	if len(listOut.WorkItems) != 1 || listOut.WorkItems[0].ID != "wi_1" {
		t.Fatalf("list response = %#v", listOut)
	}
}

func TestWorkItemRoutesRemainDiscoverableWithoutService(t *testing.T) {
	router := workItemRouter(t, nil)
	for _, req := range []*http.Request{
		httptest.NewRequest(http.MethodPost, "/api/v1/work-items", jsonBody(`{}`)),
		httptest.NewRequest(http.MethodPost, "/api/v1/work-items/wi_1/approval", jsonBody(`{}`)),
		httptest.NewRequest(http.MethodGet, "/api/v1/work-items?projectId=project", nil),
	} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotImplemented {
			t.Errorf("%s %s = %d, want 501", req.Method, req.URL.Path, rec.Code)
		}
	}
}

func jsonBody(value string) io.Reader { return strings.NewReader(value) }
