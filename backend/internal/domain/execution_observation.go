package domain

import "time"

// ExecutionEventTransport identifies which remote surface produced an ingested
// fact. The values match the execution_events CHECK constraint.
type ExecutionEventTransport string

// Execution event transport values.
const (
	// ExecutionEventInspect is the polled read model: the only reconciliation-
	// grade source in Paseo 0.2.5.
	ExecutionEventInspect ExecutionEventTransport = "inspect"
	// ExecutionEventTerminal is the cursored terminal capture channel.
	ExecutionEventTerminal ExecutionEventTransport = "terminal"
	// ExecutionEventSentinel is the advisory in-band sentinel channel.
	ExecutionEventSentinel ExecutionEventTransport = "sentinel"
	// ExecutionEventOutputSchema is a provider-structured output channel.
	ExecutionEventOutputSchema ExecutionEventTransport = "output_schema"
)

// Execution observation event types. They name the remote fact AO ingested,
// never a conclusion about the work item: a remote agent that stopped running
// has not thereby finished its task. There is deliberately no "terminated"
// type — observation cannot produce that fact.
const (
	ExecutionObservedRunning  = "agent_running"
	ExecutionObservedIdle     = "agent_idle"
	ExecutionObservedBlocked  = "agent_permission_pending"
	ExecutionObservedFailed   = "agent_error"
	ExecutionObservedClosed   = "agent_closed"
	ExecutionObservedArchived = "agent_archived"
)

// ExecutionObservationEvent is one durable remote fact. Callers supply the
// payload; the store derives the content hash and row id from it so repeated
// observations of an unchanged fact collapse to a single row.
type ExecutionObservationEvent struct {
	SessionID   SessionID
	HostID      ExecutionHostID
	LaunchID    string
	Type        string
	Transport   ExecutionEventTransport
	PayloadJSON string
	ObservedAt  time.Time
}

// ExecutionHostProbe is the outcome of one host status probe. A failed probe is
// a recorded fact about the host, never a conclusion about its sessions.
type ExecutionHostProbe struct {
	HostID     ExecutionHostID
	ServerID   string
	Version    string
	Reachable  bool
	Error      string
	ObservedAt time.Time
}

// ExecutionPermissionQuestion is a remote permission request that AO must
// decide. ExternalID is the full request id as reported by the host: Paseo
// rejects a truncated id, so a display-shortened one cannot be replayed.
type ExecutionPermissionQuestion struct {
	SessionID  SessionID
	WorkItemID string
	ExternalID string
	ToolName   string
	Question   string
	CreatedAt  time.Time
}

// ExecutionOrphanKind classifies a remote resource that AO's durable state does
// not account for.
type ExecutionOrphanKind string

// Execution orphan classifications.
const (
	// ExecutionOrphanAgent is a live agent inside an AO-owned workspace that no
	// active binding claims.
	ExecutionOrphanAgent ExecutionOrphanKind = "agent_without_binding"
	// ExecutionOrphanMissingAgent is an active binding whose agent no longer
	// exists on the host, leaving the remote workspace unattended.
	ExecutionOrphanMissingAgent ExecutionOrphanKind = "workspace_without_agent"
	// ExecutionOrphanServerIdentity is a host whose server id changed, which
	// invalidates every agent id AO holds for it.
	ExecutionOrphanServerIdentity ExecutionOrphanKind = "server_identity_changed"
)

// ExecutionOrphan is a surfaced mismatch between AO's durable state and a
// host's. It is a report for a human, never an instruction to delete anything.
type ExecutionOrphan struct {
	Kind          ExecutionOrphanKind
	HostID        ExecutionHostID
	SessionID     SessionID
	AgentID       ExecutionAgentID
	WorkspacePath string
	Detail        string
	ObservedAt    time.Time
}
