package domain

import "time"

// ProjectHostBinding maps a project clone to one execution host.
type ProjectHostBinding struct {
	ProjectID    ProjectID
	HostID       ExecutionHostID
	HostRepoPath string
	BaseBranch   string
	Priority     int
	Enabled      bool
	SetupProfile string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// SessionExecutionBinding is AO's durable link from a session to host-owned
// workspace and agent identifiers.
type SessionExecutionBinding struct {
	SessionID              SessionID
	WorkItemID             string
	BackendType            ExecutionBackendType
	HostID                 ExecutionHostID
	ExternalWorkspaceID    ExecutionWorkspaceID
	ExternalAgentID        ExecutionAgentID
	ExternalParentAgentID  ExecutionAgentID
	BoundServerID          string
	WorkspaceTitle         string
	IntentID               ExecutionIntentID
	Attempt                int
	LabelsWritten          map[string]string
	BranchName             string
	HostWorkspacePath      string
	Provider               string
	Model                  string
	Mode                   string
	DispatchGeneration     int
	LaunchID               string
	TranscriptBytes        int64
	TranscriptPrefixSHA256 string
	TerminalID             string
	TerminalLinesConsumed  int64
	LastObservedAt         time.Time
	CreatedAt              time.Time
	ArchivedAt             time.Time
}
