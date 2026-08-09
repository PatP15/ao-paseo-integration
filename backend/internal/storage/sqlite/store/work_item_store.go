package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/gen"
)

// UpsertWorkItem writes one durable work-graph node.
func (s *Store) UpsertWorkItem(ctx context.Context, item domain.WorkItem) error {
	acceptance, err := marshalStringSlice(item.AcceptanceCriteria)
	if err != nil {
		return fmt.Errorf("marshal work item acceptance criteria: %w", err)
	}
	allowed, err := marshalStringSlice(item.AllowedScope)
	if err != nil {
		return fmt.Errorf("marshal work item allowed scope: %w", err)
	}
	excluded, err := marshalStringSlice(item.ExcludedScope)
	if err != nil {
		return fmt.Errorf("marshal work item excluded scope: %w", err)
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.qw.UpsertWorkItem(ctx, gen.UpsertWorkItemParams{
		ID: item.ID, ProjectID: string(item.ProjectID), ParentWorkItemID: nullableString(item.ParentWorkItemID),
		Title: item.Title, Body: item.Body, AcceptanceCriteriaJson: string(acceptance),
		AllowedScopeJson: string(allowed), ExcludedScopeJson: string(excluded), RiskLevel: item.RiskLevel,
		PolicyProfileID: item.PolicyProfileID, ApprovalState: string(item.ApprovalState),
		LifecycleFact: string(item.LifecycleFact), Priority: int64(item.Priority), CreatedByType: item.CreatedByType,
		CreatedByID: item.CreatedByID, ApprovedBy: item.ApprovedBy, ApprovedAt: encodeExecutionTime(item.ApprovedAt),
		CreatedAt: encodeExecutionTime(item.CreatedAt), UpdatedAt: encodeExecutionTime(item.UpdatedAt),
	})
}

// GetWorkItem returns one work-graph node by ID.
func (s *Store) GetWorkItem(ctx context.Context, id string) (domain.WorkItem, bool, error) {
	row, err := s.qr.GetWorkItem(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.WorkItem{}, false, nil
	}
	if err != nil {
		return domain.WorkItem{}, false, fmt.Errorf("get work item %s: %w", id, err)
	}
	item, err := workItemFromGen(row)
	if err != nil {
		return domain.WorkItem{}, false, err
	}
	return item, true, nil
}

// SetWorkItemApproval atomically decides a draft or proposed work item as
// approved or rejected. A false result means either the item does not exist or
// its approval state no longer permits a decision; callers can GetWorkItem to
// distinguish the two.
func (s *Store) SetWorkItemApproval(ctx context.Context, id, approver string, decision domain.WorkItemApproval, at time.Time) (domain.WorkItem, bool, error) {
	if decision != domain.WorkItemApproved && decision != domain.WorkItemRejected {
		return domain.WorkItem{}, false, fmt.Errorf("approval decision must be approved or rejected, got %q", decision)
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	row, err := s.qw.SetWorkItemApproval(ctx, gen.SetWorkItemApprovalParams{
		ApprovalState: string(decision),
		ApprovedBy:    approver,
		ApprovedAt:    encodeExecutionTime(at),
		UpdatedAt:     encodeExecutionTime(at),
		ID:            id,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return domain.WorkItem{}, false, nil
	}
	if err != nil {
		return domain.WorkItem{}, false, fmt.Errorf("approve work item %s: %w", id, err)
	}
	item, err := workItemFromGen(row)
	if err != nil {
		return domain.WorkItem{}, false, err
	}
	return item, true, nil
}

// ListWorkItemsByProject returns one project's durable work graph ordered by
// priority and creation time.
func (s *Store) ListWorkItemsByProject(ctx context.Context, projectID domain.ProjectID) ([]domain.WorkItem, error) {
	rows, err := s.qr.ListWorkItemsByProject(ctx, string(projectID))
	if err != nil {
		return nil, fmt.Errorf("list work items for project %s: %w", projectID, err)
	}
	items := make([]domain.WorkItem, 0, len(rows))
	for _, row := range rows {
		item, err := workItemFromGen(row)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}

// ClaimWorkItemSession records an active session owner. The database's partial
// unique index rejects a second active implementer for the same work item.
func (s *Store) ClaimWorkItemSession(ctx context.Context, claim domain.WorkItemSession) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if err := s.qw.ClaimWorkItemSession(ctx, gen.ClaimWorkItemSessionParams{
		WorkItemID: claim.WorkItemID, SessionID: string(claim.SessionID), Role: string(claim.Role),
		AttemptNumber: int64(claim.Attempt), CreatedAt: encodeExecutionTime(claim.CreatedAt),
	}); err != nil {
		return fmt.Errorf("claim work item %s for session %s: %w", claim.WorkItemID, claim.SessionID, err)
	}
	return nil
}

// ReleaseWorkItemSession clears a held ownership claim.
func (s *Store) ReleaseWorkItemSession(ctx context.Context, workItemID string, sessionID domain.SessionID, releasedAt string) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	rows, err := s.qw.ReleaseWorkItemSession(ctx, gen.ReleaseWorkItemSessionParams{
		ReleasedAt: releasedAt, WorkItemID: workItemID, SessionID: string(sessionID),
	})
	if err != nil {
		return false, fmt.Errorf("release work item %s session %s: %w", workItemID, sessionID, err)
	}
	return rows > 0, nil
}

func workItemFromGen(row gen.WorkItem) (domain.WorkItem, error) {
	var acceptance, allowed, excluded []string
	for _, value := range []struct {
		name string
		raw  string
		out  *[]string
	}{
		{name: "acceptance criteria", raw: row.AcceptanceCriteriaJson, out: &acceptance},
		{name: "allowed scope", raw: row.AllowedScopeJson, out: &allowed},
		{name: "excluded scope", raw: row.ExcludedScopeJson, out: &excluded},
	} {
		if err := json.Unmarshal([]byte(value.raw), value.out); err != nil {
			return domain.WorkItem{}, fmt.Errorf("decode work item %s %s: %w", row.ID, value.name, err)
		}
	}
	approved, err := decodeExecutionTime(row.ApprovedAt)
	if err != nil {
		return domain.WorkItem{}, fmt.Errorf("decode work item %s approved time: %w", row.ID, err)
	}
	created, err := decodeExecutionTime(row.CreatedAt)
	if err != nil {
		return domain.WorkItem{}, fmt.Errorf("decode work item %s created time: %w", row.ID, err)
	}
	updated, err := decodeExecutionTime(row.UpdatedAt)
	if err != nil {
		return domain.WorkItem{}, fmt.Errorf("decode work item %s updated time: %w", row.ID, err)
	}
	return domain.WorkItem{
		ID: row.ID, ProjectID: domain.ProjectID(row.ProjectID), ParentWorkItemID: row.ParentWorkItemID.String,
		Title: row.Title, Body: row.Body, AcceptanceCriteria: acceptance, AllowedScope: allowed,
		ExcludedScope: excluded, RiskLevel: row.RiskLevel, PolicyProfileID: row.PolicyProfileID,
		ApprovalState: domain.WorkItemApproval(row.ApprovalState), LifecycleFact: domain.WorkItemLifecycle(row.LifecycleFact),
		Priority: int(row.Priority), CreatedByType: row.CreatedByType, CreatedByID: row.CreatedByID,
		ApprovedBy: row.ApprovedBy, ApprovedAt: approved, CreatedAt: created, UpdatedAt: updated,
	}, nil
}

func marshalStringSlice(value []string) ([]byte, error) {
	if value == nil {
		value = []string{}
	}
	return json.Marshal(value)
}
