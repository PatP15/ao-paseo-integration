// Package workitem owns creation, approval, and reads for AO's durable work
// graph. It deliberately has no execution-backend dependency: approval is an
// AO control-plane fact that must exist before dispatch is allowed.
package workitem

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
)

const (
	defaultPriority  = 100
	defaultRiskLevel = "normal"
)

// Store is the durable work-item state used by the service.
type Store interface {
	UpsertWorkItem(context.Context, domain.WorkItem) error
	SetWorkItemApproval(context.Context, string, string, time.Time) (domain.WorkItem, bool, error)
	ListWorkItemsByProject(context.Context, domain.ProjectID) ([]domain.WorkItem, error)
	GetWorkItem(context.Context, string) (domain.WorkItem, bool, error)
}

// CreateInput contains the planner-authored fields of a work item. Approval
// and lifecycle are intentionally absent: creation always starts draft/open.
type CreateInput struct {
	ProjectID          domain.ProjectID `json:"projectId"`
	ParentWorkItemID   string           `json:"parentWorkItemId,omitempty"`
	Title              string           `json:"title"`
	Body               string           `json:"body,omitempty"`
	AcceptanceCriteria []string         `json:"acceptanceCriteria,omitempty"`
	AllowedScope       []string         `json:"allowedScope,omitempty"`
	ExcludedScope      []string         `json:"excludedScope,omitempty"`
	RiskLevel          string           `json:"riskLevel,omitempty"`
	PolicyProfileID    string           `json:"policyProfileId,omitempty"`
	Priority           int              `json:"priority,omitempty"`
	CreatedBy          string           `json:"createdBy,omitempty"`
}

// Service implements work-item control-plane use cases.
type Service struct {
	store Store
	now   func() time.Time
	newID func() string
}

// New constructs a work-item service with production time and IDs.
func New(store Store) *Service {
	return newService(store, time.Now, func() string { return "wi_" + uuid.NewString() })
}

func newService(store Store, now func() time.Time, newID func() string) *Service {
	return &Service{store: store, now: now, newID: newID}
}

// Create persists a new draft/open work item.
func (s *Service) Create(ctx context.Context, in CreateInput) (domain.WorkItem, error) {
	projectID := domain.ProjectID(strings.TrimSpace(string(in.ProjectID)))
	if projectID == "" {
		return domain.WorkItem{}, apierr.Invalid("PROJECT_ID_REQUIRED", "projectId is required", nil)
	}
	title := strings.TrimSpace(in.Title)
	if title == "" {
		return domain.WorkItem{}, apierr.Invalid("WORK_ITEM_TITLE_REQUIRED", "title is required", nil)
	}
	risk := strings.TrimSpace(in.RiskLevel)
	if risk == "" {
		risk = defaultRiskLevel
	}
	priority := in.Priority
	if priority == 0 {
		priority = defaultPriority
	}
	now := s.now().UTC()
	item := domain.WorkItem{
		ID:                 s.newID(),
		ProjectID:          projectID,
		ParentWorkItemID:   strings.TrimSpace(in.ParentWorkItemID),
		Title:              title,
		Body:               strings.TrimSpace(in.Body),
		AcceptanceCriteria: cleanStrings(in.AcceptanceCriteria),
		AllowedScope:       cleanStrings(in.AllowedScope),
		ExcludedScope:      cleanStrings(in.ExcludedScope),
		RiskLevel:          risk,
		PolicyProfileID:    strings.TrimSpace(in.PolicyProfileID),
		ApprovalState:      domain.WorkItemDraft,
		LifecycleFact:      domain.WorkItemOpen,
		Priority:           priority,
		CreatedByType:      "human",
		CreatedByID:        strings.TrimSpace(in.CreatedBy),
		CreatedAt:          now,
		UpdatedAt:          now,
	}
	if err := s.store.UpsertWorkItem(ctx, item); err != nil {
		return domain.WorkItem{}, fmt.Errorf("create work item: %w", err)
	}
	return item, nil
}

// Approve promotes a draft or proposed item and records who made the durable
// decision. Approval cannot be used to rewrite an existing decision.
func (s *Service) Approve(ctx context.Context, id, approver string) (domain.WorkItem, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return domain.WorkItem{}, apierr.Invalid("WORK_ITEM_ID_REQUIRED", "work item id is required", nil)
	}
	approver = strings.TrimSpace(approver)
	if approver == "" {
		return domain.WorkItem{}, apierr.Invalid("APPROVER_REQUIRED", "approver is required", nil)
	}
	item, changed, err := s.store.SetWorkItemApproval(ctx, id, approver, s.now().UTC())
	if err != nil {
		return domain.WorkItem{}, fmt.Errorf("approve work item: %w", err)
	}
	if changed {
		return item, nil
	}
	existing, found, err := s.store.GetWorkItem(ctx, id)
	if err != nil {
		return domain.WorkItem{}, fmt.Errorf("get work item after approval conflict: %w", err)
	}
	if !found {
		return domain.WorkItem{}, apierr.NotFound("WORK_ITEM_NOT_FOUND", "work item "+id+" was not found")
	}
	return domain.WorkItem{}, apierr.Conflict("WORK_ITEM_NOT_APPROVABLE",
		fmt.Sprintf("work item %s is %s and cannot be approved", id, existing.ApprovalState), nil)
}

// List returns all work items belonging to one project.
func (s *Service) List(ctx context.Context, projectID domain.ProjectID) ([]domain.WorkItem, error) {
	projectID = domain.ProjectID(strings.TrimSpace(string(projectID)))
	if projectID == "" {
		return nil, apierr.Invalid("PROJECT_ID_REQUIRED", "projectId is required", nil)
	}
	items, err := s.store.ListWorkItemsByProject(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("list work items: %w", err)
	}
	return items, nil
}

// Get returns one work item by ID.
func (s *Service) Get(ctx context.Context, id string) (domain.WorkItem, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return domain.WorkItem{}, apierr.Invalid("WORK_ITEM_ID_REQUIRED", "work item id is required", nil)
	}
	item, found, err := s.store.GetWorkItem(ctx, id)
	if err != nil {
		return domain.WorkItem{}, fmt.Errorf("get work item: %w", err)
	}
	if !found {
		return domain.WorkItem{}, apierr.NotFound("WORK_ITEM_NOT_FOUND", "work item "+id+" was not found")
	}
	return item, nil
}

func cleanStrings(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			out = append(out, value)
		}
	}
	return out
}
