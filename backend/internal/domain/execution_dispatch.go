package domain

import "time"

// ExecutionCommandType identifies one durable operation for an execution
// backend. Commands are ordered per session and delivered from the outbox.
type ExecutionCommandType string

// Command types an execution backend can be asked to perform. These are
// durable outbox values, so renaming one is a migration, not a refactor.
const (
	ExecutionCommandPrepareWorkspace ExecutionCommandType = "prepare_workspace"
	ExecutionCommandStartAgent       ExecutionCommandType = "start_agent"
	ExecutionCommandSendMessage      ExecutionCommandType = "send_message"
	ExecutionCommandAnswerPermission ExecutionCommandType = "answer_permission"
	ExecutionCommandDenyPermission   ExecutionCommandType = "deny_permission"
	ExecutionCommandCheckpoint       ExecutionCommandType = "checkpoint"
	ExecutionCommandStopAgent        ExecutionCommandType = "stop_agent"
	ExecutionCommandArchiveWorkspace ExecutionCommandType = "archive_workspace"
)

// ExecutionCommandState is the durable delivery state of an outbox row.
type ExecutionCommandState string

// Outbox delivery states. A command is committed as pending BEFORE any remote
// call; acknowledged is written only after the backend confirms.
const (
	ExecutionCommandPending      ExecutionCommandState = "pending"
	ExecutionCommandDelivering   ExecutionCommandState = "delivering"
	ExecutionCommandAcknowledged ExecutionCommandState = "acknowledged"
	ExecutionCommandFailed       ExecutionCommandState = "failed"
)

// ExecutionCommand is one durable, retryable backend operation.
type ExecutionCommand struct {
	ID             string
	SessionID      SessionID
	HostID         ExecutionHostID
	Type           ExecutionCommandType
	PayloadJSON    string
	IdempotencyKey string
	Sequence       int
	State          ExecutionCommandState
	AttemptCount   int
	NextAttemptAt  time.Time
	LastError      string
	CreatedAt      time.Time
	AcknowledgedAt time.Time
}

// ExecutionStartPayload is the provider-neutral payload for the single
// start_agent command created by an approved dispatch. Provision and Launch
// consume it in order; their remote identifiers are persisted by the backend.
type ExecutionStartPayload struct {
	ProjectID  ProjectID `json:"projectId"`
	RepoPath   string    `json:"repoPath"`
	BaseBranch string    `json:"baseBranch"`
	Branch     string    `json:"branch"`
	Provider   string    `json:"provider"`
	Model      string    `json:"model,omitempty"`
	Mode       string    `json:"mode,omitempty"`
	// ThinkingOptionID is only ever a value discovery reported for the payload's
	// provider and model; dispatch refuses anything else before it is committed.
	ThinkingOptionID string            `json:"thinkingOptionId,omitempty"`
	Prompt           string            `json:"prompt"`
	IntentID         ExecutionIntentID `json:"intentId"`
	Attempt          int               `json:"attempt"`
	LaunchID         string            `json:"launchId"`
}

// ExecutionDispatchSeed contains everything the store commits atomically for
// one approved work item. The store assigns the conventional project-N
// session ID and derives the workspace title and idempotency key from it.
type ExecutionDispatchSeed struct {
	WorkItemID           string
	Session              SessionRecord
	HostID               ExecutionHostID
	BoundServerID        string
	RequestedTrustZone   ExecutionTrustZone
	RequiredCapabilities []string
	HostRepoPath         string
	BaseBranch           string
	Branch               string
	Provider             string
	Model                string
	Mode                 string
	ThinkingOptionID     string
	// SkillPolicyOverrides are recorded as audit events in the dispatch
	// transaction; they never change what launches. Actor is the identity the
	// audit rows carry.
	SkillPolicyOverrides []string
	Actor                string
	Prompt               string
	IntentID             ExecutionIntentID
	Attempt              int
	DispatchGeneration   int
	LaunchID             string
	CommandID            string
	CreatedAt            time.Time
}

// ExecutionDispatch is the atomically committed session, binding, and first
// outbox command returned to the dispatch service.
type ExecutionDispatch struct {
	Session SessionRecord
	Binding SessionExecutionBinding
	Command ExecutionCommand
}
