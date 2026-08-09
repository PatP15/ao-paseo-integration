package ports

import (
	"context"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// ExecutionBackend is a remote agent execution substrate: it owns workspace
// materialization and process launch on another host, so AO records handles
// instead of creating a local worktree and a local runtime.
type ExecutionBackend interface {
	// Provision materializes the remote workspace for a session that already
	// has an id. Idempotency is best-effort on req.WorkspaceTitle; callers must
	// treat a duplicate as possible and reconcile after errors.
	Provision(ctx context.Context, req ExecutionProvisionRequest) (domain.ExecutionWorkspace, error)

	// Launch starts the agent in an already-provisioned workspace, tagging it
	// with req.IntentID for post-hoc reconciliation.
	Launch(ctx context.Context, req ExecutionLaunchRequest) (domain.ExecutionAgent, error)
}

// ExecutionRuntime is the post-launch control surface, consumed by the runtime
// router rather than by the session manager.
type ExecutionRuntime interface {
	Stop(ctx context.Context, handle RuntimeHandle) error

	// Alive must return a non-nil error when the host is unreachable.
	// Returning (false, nil) is read as death by the reaper and would terminate
	// a live remote session.
	Alive(ctx context.Context, handle RuntimeHandle) (bool, error)

	Output(ctx context.Context, handle RuntimeHandle, lines int) (string, error)
	SendMessage(ctx context.Context, handle RuntimeHandle, message string) error
}

// ExecutionProviderDiscovery reads what one host's daemon can launch:
// providers, their models, and each model's thinking-option vocabulary. The
// result is the sole validation source for dispatch settings — an id discovery
// did not return is refused, never forwarded.
type ExecutionProviderDiscovery interface {
	Providers(ctx context.Context, hostID domain.ExecutionHostID) ([]domain.ExecutionHostProvider, error)
}

// ExecutionObserver reads remote host and agent state without mutating it.
type ExecutionObserver interface {
	Status(ctx context.Context, hostID domain.ExecutionHostID) (domain.ExecutionHostStatus, error)
	ListOwned(ctx context.Context, hostID domain.ExecutionHostID) ([]domain.ExecutionAgentObservation, error)
	Inspect(ctx context.Context, hostID domain.ExecutionHostID, agentID domain.ExecutionAgentID) (domain.ExecutionAgentDetail, error)
}

// ExecutionTerminalReader is the cursored read surface agent-authored reports
// arrive on. start and end are line addresses into a monotonic cursor, so a
// caller reads forward without re-parsing what it already consumed.
//
// Implementations must be read-only and must return an error rather than an
// empty window when the host cannot be reached: an empty window is
// indistinguishable from a quiet agent.
type ExecutionTerminalReader interface {
	CaptureTerminal(ctx context.Context, hostID domain.ExecutionHostID, terminalID string, start, end int64) (domain.ExecutionEventWindow, error)
}

// ExecutionTranscriptReader is the uncursored fallback: the whole rendered
// transcript, every call.
//
// There is no cursor and no server-side filter to ask for, which is what makes
// full replay safe — a caller cannot miss a report that is still on the
// transcript. Implementations must not narrow the read (no filter, no tail, no
// follow): each of those drops or reorders entries and would silently discard
// reports AO has not seen.
type ExecutionTranscriptReader interface {
	Transcript(ctx context.Context, hostID domain.ExecutionHostID, agentID domain.ExecutionAgentID) (string, error)
}

// ExecutionProvisionRequest is AO's request to materialize one remote workspace.
type ExecutionProvisionRequest struct {
	SessionID      domain.SessionID
	ProjectID      domain.ProjectID
	HostID         domain.ExecutionHostID
	WorkspaceTitle string
	RepoPath       string
	Branch         string
	BaseBranch     string
	Provider       string
	Model          string
	Mode           string
}

// ExecutionLaunchRequest is AO's request to launch one remote agent.
type ExecutionLaunchRequest struct {
	SessionID     domain.SessionID
	HostID        domain.ExecutionHostID
	WorkspaceID   domain.ExecutionWorkspaceID
	IntentID      domain.ExecutionIntentID
	Prompt        string
	Labels        map[string]string
	ParentAgentID domain.ExecutionAgentID
	Provider      string
	Model         string
	Mode          string
	// ThinkingOptionID was validated against provider discovery at dispatch
	// time; adapters forward it verbatim.
	ThinkingOptionID string
}
