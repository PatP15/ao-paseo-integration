package domain

import "time"

// WorkItemApproval records the durable human approval fact for a work item.
type WorkItemApproval string

// Approval states. Planner-created work enters WorkItemProposed and requires a
// human decision before it can be dispatched; nothing promotes itself.
const (
	WorkItemDraft    WorkItemApproval = "draft"
	WorkItemProposed WorkItemApproval = "proposed"
	WorkItemApproved WorkItemApproval = "approved"
	WorkItemRejected WorkItemApproval = "rejected"
)

// WorkItemLifecycle records durable lifecycle facts, not display status.
type WorkItemLifecycle string

// Lifecycle facts. These are the only lifecycle values persisted; board columns
// such as Running, Needs Input, or Review are DERIVED in the service layer and
// must never be stored (docs/architecture.md, load-bearing rule 1).
const (
	WorkItemOpen       WorkItemLifecycle = "open"
	WorkItemInProgress WorkItemLifecycle = "in_progress"
	WorkItemDone       WorkItemLifecycle = "done"
	WorkItemCancelled  WorkItemLifecycle = "cancelled"
)

// WorkItem is one durable node in AO's execution work graph.
type WorkItem struct {
	ID                 string
	ProjectID          ProjectID
	ParentWorkItemID   string
	Title              string
	Body               string
	AcceptanceCriteria []string
	AllowedScope       []string
	ExcludedScope      []string
	RiskLevel          string
	PolicyProfileID    string
	ApprovalState      WorkItemApproval
	LifecycleFact      WorkItemLifecycle
	Priority           int
	CreatedByType      string
	CreatedByID        string
	ApprovedBy         string
	ApprovedAt         time.Time
	// DecisionNote is the reason the human gave with the approval decision. It
	// matters most on a rejection: without it, "Rejected" is a dead end for
	// everyone except whoever decided.
	DecisionNote string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// WorkItemSessionRole describes how a session participates in a work item.
type WorkItemSessionRole string

// Session roles. At most one WorkItemRoleImplementer may be the active owner of
// a work item at a time; that is enforced by a partial unique index in migration
// 0910, not by application logic.
const (
	WorkItemRolePlanner     WorkItemSessionRole = "planner"
	WorkItemRoleImplementer WorkItemSessionRole = "implementer"
	WorkItemRoleReviewer    WorkItemSessionRole = "reviewer"
	WorkItemRoleVerifier    WorkItemSessionRole = "verifier"
)

// WorkItemSession is a durable work-item attempt/ownership fact.
type WorkItemSession struct {
	WorkItemID  string
	SessionID   SessionID
	Role        WorkItemSessionRole
	Attempt     int
	ActiveOwner bool
	CreatedAt   time.Time
	ReleasedAt  time.Time
}
