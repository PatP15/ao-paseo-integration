package controllers

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apispec"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
	workitemsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/workitem"
)

// WorkItemService is the controller-facing work-graph surface.
type WorkItemService interface {
	Create(context.Context, workitemsvc.CreateInput) (domain.WorkItem, error)
	Decide(context.Context, string, string, string, domain.WorkItemApproval) (domain.WorkItem, error)
	List(context.Context, domain.ProjectID) ([]domain.WorkItem, error)
	Get(context.Context, string) (domain.WorkItem, error)
}

// WorkItemSessionClaimReader is the optional read that lets a work item point at
// the sessions which have worked it. Optional and advisory, on the same terms as
// the sessions list's execution annotation: a failed read returns the items
// unannotated rather than failing the list, because a work-item list without
// session links beats no work-item list.
type WorkItemSessionClaimReader interface {
	ListWorkItemSessionsByProject(context.Context, domain.ProjectID) ([]domain.WorkItemSession, error)
}

// WorkItemsController owns the work-item create, approval, and list routes.
type WorkItemsController struct {
	Svc WorkItemService
	// Claims annotates listed items with their sessions.
	Claims WorkItemSessionClaimReader
}

// Register mounts the work-item routes.
func (c *WorkItemsController) Register(r chi.Router) {
	r.Post("/work-items", c.create)
	r.Post("/work-items/{id}/approval", c.approve)
	r.Get("/work-items", c.list)
	r.Get("/work-items/{id}", c.get)
}

func (c *WorkItemsController) create(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, http.MethodPost, "/api/v1/work-items")
		return
	}
	var in workitemsvc.CreateInput
	if err := decodeJSONStrict(r, &in); err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_JSON", "Invalid JSON body", nil)
		return
	}
	item, err := c.Svc.Create(r.Context(), in)
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusCreated, WorkItemEnvelope{WorkItem: workItemResponse(item)})
}

func (c *WorkItemsController) approve(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, http.MethodPost, "/api/v1/work-items/{id}/approval")
		return
	}
	var in ApproveWorkItemRequest
	if err := decodeJSONStrict(r, &in); err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_JSON", "Invalid JSON body", nil)
		return
	}
	decision := domain.WorkItemApproval(strings.TrimSpace(in.Decision))
	if decision == "" {
		decision = domain.WorkItemApproved
	}
	item, err := c.Svc.Decide(r.Context(), chi.URLParam(r, "id"), in.Approver, in.Note, decision)
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, WorkItemEnvelope{WorkItem: workItemResponse(item)})
}

func (c *WorkItemsController) get(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, http.MethodGet, "/api/v1/work-items/{id}")
		return
	}
	item, err := c.Svc.Get(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, WorkItemEnvelope{WorkItem: workItemResponse(item)})
}

func (c *WorkItemsController) list(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, http.MethodGet, "/api/v1/work-items")
		return
	}
	items, err := c.Svc.List(r.Context(), domain.ProjectID(r.URL.Query().Get("projectId")))
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	sessions := c.sessionsByWorkItem(r.Context(), domain.ProjectID(r.URL.Query().Get("projectId")))
	out := make([]WorkItemResponse, 0, len(items))
	for _, item := range items {
		view := workItemResponse(item)
		if ids, ok := sessions[item.ID]; ok {
			view.SessionIDs = ids
		}
		out = append(out, view)
	}
	envelope.WriteJSON(w, http.StatusOK, ListWorkItemsResponse{WorkItems: out})
}

// sessionsByWorkItem groups one project's session claims by work item, in claim
// order, so the newest attempt is last. An unavailable reader or a failed read
// answers an empty map: the annotation is display data, never a reason to fail
// the list.
func (c *WorkItemsController) sessionsByWorkItem(ctx context.Context, projectID domain.ProjectID) map[string][]string {
	if c.Claims == nil {
		return map[string][]string{}
	}
	claims, err := c.Claims.ListWorkItemSessionsByProject(ctx, projectID)
	if err != nil {
		return map[string][]string{}
	}
	grouped := make(map[string][]string, len(claims))
	for _, claim := range claims {
		grouped[claim.WorkItemID] = append(grouped[claim.WorkItemID], string(claim.SessionID))
	}
	return grouped
}

// WorkItemIDParam identifies the work item whose approval is changing.
type WorkItemIDParam struct {
	ID string `path:"id" description:"Work-item identifier."`
}

// ListWorkItemsQuery selects one project's work graph.
type ListWorkItemsQuery struct {
	ProjectID domain.ProjectID `query:"projectId" description:"Project whose work items should be returned."`
}

// ApproveWorkItemRequest records the human responsible for the approval
// decision. Decision defaults to approved when absent so existing callers
// keep working; an absent approver is recorded as the OS user running the
// daemon.
type ApproveWorkItemRequest struct {
	Approver string `json:"approver,omitempty" description:"Identity recorded on the decision. Defaults to the OS user running the daemon."`
	Decision string `json:"decision,omitempty" enum:"approved,rejected" description:"Approval decision; defaults to approved when omitted."`
	Note     string `json:"note,omitempty" description:"Reason recorded with the decision. Optional, and most useful on a rejection: it is the only explanation anyone but the decider will see."`
}

// WorkItemResponse is one durable work-graph node.
type WorkItemResponse struct {
	ID                 string                   `json:"id"`
	ProjectID          domain.ProjectID         `json:"projectId"`
	ParentWorkItemID   string                   `json:"parentWorkItemId,omitempty"`
	Title              string                   `json:"title"`
	Body               string                   `json:"body"`
	AcceptanceCriteria []string                 `json:"acceptanceCriteria"`
	AllowedScope       []string                 `json:"allowedScope"`
	ExcludedScope      []string                 `json:"excludedScope"`
	RiskLevel          string                   `json:"riskLevel"`
	PolicyProfileID    string                   `json:"policyProfileId,omitempty"`
	ApprovalState      domain.WorkItemApproval  `json:"approvalState" enum:"draft,proposed,approved,rejected"`
	LifecycleFact      domain.WorkItemLifecycle `json:"lifecycleFact" enum:"open,in_progress,done,cancelled"`
	Priority           int                      `json:"priority"`
	CreatedByType      string                   `json:"createdByType"`
	CreatedByID        string                   `json:"createdById,omitempty"`
	ApprovedBy         string                   `json:"approvedBy,omitempty"`
	ApprovedAt         *time.Time               `json:"approvedAt,omitempty"`
	DecisionNote       string                   `json:"decisionNote,omitempty" description:"Reason the decider recorded with the approval decision."`
	SessionIDs         []string                 `json:"sessionIds" description:"Sessions that have worked this item, oldest attempt first. The last one is where the work is now."`
	CreatedAt          time.Time                `json:"createdAt"`
	UpdatedAt          time.Time                `json:"updatedAt"`
}

// WorkItemEnvelope wraps one created or approved work item.
type WorkItemEnvelope struct {
	WorkItem WorkItemResponse `json:"workItem"`
}

// ListWorkItemsResponse is the body of GET /api/v1/work-items.
type ListWorkItemsResponse struct {
	WorkItems []WorkItemResponse `json:"workItems"`
}

func workItemResponse(item domain.WorkItem) WorkItemResponse {
	acceptance := append([]string(nil), item.AcceptanceCriteria...)
	allowed := append([]string(nil), item.AllowedScope...)
	excluded := append([]string(nil), item.ExcludedScope...)
	if acceptance == nil {
		acceptance = []string{}
	}
	if allowed == nil {
		allowed = []string{}
	}
	if excluded == nil {
		excluded = []string{}
	}
	var approvedAt *time.Time
	if !item.ApprovedAt.IsZero() {
		at := item.ApprovedAt
		approvedAt = &at
	}
	return WorkItemResponse{
		ID: item.ID, ProjectID: item.ProjectID, ParentWorkItemID: item.ParentWorkItemID,
		Title: item.Title, Body: item.Body, AcceptanceCriteria: acceptance, AllowedScope: allowed,
		ExcludedScope: excluded, RiskLevel: item.RiskLevel, PolicyProfileID: item.PolicyProfileID,
		ApprovalState: item.ApprovalState, LifecycleFact: item.LifecycleFact, Priority: item.Priority,
		CreatedByType: strings.TrimSpace(item.CreatedByType), CreatedByID: item.CreatedByID,
		ApprovedBy: item.ApprovedBy, ApprovedAt: approvedAt, DecisionNote: item.DecisionNote,
		SessionIDs: []string{}, CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt,
	}
}
