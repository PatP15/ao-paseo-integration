package domain

import "time"

// ExecutionBackendType identifies which execution substrate owns a session.
type ExecutionBackendType string

const (
	// ExecutionBackendLocal is AO's existing local runtime/worktree path.
	ExecutionBackendLocal ExecutionBackendType = "local"
	// ExecutionBackendPaseo is the remote execution backend used by this fork.
	ExecutionBackendPaseo ExecutionBackendType = "paseo"
)

type (
	// ExecutionHostID identifies one registered execution host.
	ExecutionHostID string
	// ExecutionWorkspaceID identifies a workspace owned by an execution host.
	ExecutionWorkspaceID string
	// ExecutionAgentID identifies an agent owned by an execution host.
	ExecutionAgentID string
	// ExecutionIntentID correlates a launch with later reconciliation facts.
	ExecutionIntentID string
)

// ExecutionAgentStatus is the provider-neutral remote status AO observes.
type ExecutionAgentStatus string

// Execution agent status values.
const (
	ExecutionAgentInitializing ExecutionAgentStatus = "initializing"
	ExecutionAgentIdle         ExecutionAgentStatus = "idle"
	ExecutionAgentRunning      ExecutionAgentStatus = "running"
	ExecutionAgentError        ExecutionAgentStatus = "error"
	ExecutionAgentClosed       ExecutionAgentStatus = "closed"
)

// ExecutionWorkspace is the remote workspace materialized for a session.
type ExecutionWorkspace struct {
	HostID      ExecutionHostID
	WorkspaceID ExecutionWorkspaceID
	Title       string
	RepoPath    string
	Branch      string
	Provider    string
	Model       string
	Mode        string
	CreatedAt   time.Time
}

// ExecutionAgent is the launched remote agent AO records and later controls.
type ExecutionAgent struct {
	HostID        ExecutionHostID
	AgentID       ExecutionAgentID
	ParentAgentID ExecutionAgentID
	WorkspaceID   ExecutionWorkspaceID
	Branch        string
	Cwd           string
	Provider      string
	Model         string
	Mode          string
	LaunchedAt    time.Time
}

// ExecutionPermission is one remote permission request awaiting an AO decision.
type ExecutionPermission struct {
	ID       string
	ToolName string
	Reason   string
}

// ExecutionAgentObservation is the summary fact set from a list/read-model call.
type ExecutionAgentObservation struct {
	HostID        ExecutionHostID
	AgentID       ExecutionAgentID
	ParentAgentID ExecutionAgentID
	WorkspaceID   ExecutionWorkspaceID
	Status        ExecutionAgentStatus
	Worktree      string
	Cwd           string
	Archived      bool
	CreatedAt     time.Time
}

// ExecutionAgentDetail is the full fact set AO uses to reconcile one agent.
type ExecutionAgentDetail struct {
	ExecutionAgentObservation
	PendingPermissions []ExecutionPermission
}
