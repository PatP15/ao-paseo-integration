package ports

import (
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// NotificationIntent is the lifecycle-to-notification-producer contract. It is
// not an HTTP DTO; lifecycle fills it from facts it already has after the
// underlying session/PR state write succeeds.
type NotificationIntent struct {
	Type      domain.NotificationType
	SessionID domain.SessionID
	ProjectID domain.ProjectID
	PRURL     string
	CreatedAt time.Time

	// WorkItemID and QuestionID scope a needs_input notification to one
	// execution inbox question. QuestionID also changes dedupe: each open
	// question is its own notification.
	WorkItemID string
	QuestionID string

	// Enrichment hints. These avoid storage reads on the hot path.
	SessionDisplayName string
	PRNumber           int
	PRTitle            string
	PRSourceBranch     string
	PRTargetBranch     string
	Provider           string
	Repo               string
	// QuestionText, when set on a needs_input intent, becomes the body so the
	// dashboard shows what is being asked without another read.
	QuestionText string
}

// NotificationResolution is the lifecycle-to-notification-producer contract for
// closing notifications whose underlying issue went away. Lifecycle fills it
// after the durable fact that resolves the issue is written — the session left
// the needs-input family, the PR stopped waiting on a merge.
//
// QuestionID wins when set (answering one inbox question closes exactly its
// notification); then PRURL (a PR-scoped notification can outlive its
// session's activity churn); otherwise the resolution is scoped to SessionID.
type NotificationResolution struct {
	Type       domain.NotificationType
	SessionID  domain.SessionID
	PRURL      string
	QuestionID string
	ResolvedAt time.Time
}
