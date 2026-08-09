package domain

import (
	"errors"
	"time"
)

// SessionBrief is the immutable instruction package AO commits before a remote
// launch. Nothing overwrites one: a correction is a new version that names the
// brief it supersedes, so the exact contract an agent was launched under stays
// recoverable after the fact.
//
// ReportNonce is per launch. It is what stops AO ingesting its own instructions:
// the brief necessarily teaches the report format, so without a nonce the
// teaching example is byte-identical to a real report.
type SessionBrief struct {
	ID                string
	SessionID         SessionID
	Version           int
	SchemaVersion     string
	BriefJSON         string
	BriefSHA256       string
	ReportNonce       string
	CreatedAt         time.Time
	SupersedesBriefID string
}

// ExecutionReportType names one kind of agent-authored report. The set is
// closed: an unknown type is dropped rather than stored, because a type AO does
// not understand cannot be applied safely.
type ExecutionReportType string

// Agent-authored report types.
const (
	// ExecutionReportCheckpoint is progress evidence mid-run. It is never
	// completion.
	ExecutionReportCheckpoint ExecutionReportType = "checkpoint"
	// ExecutionReportQuestion is a question for a human. It maps to
	// ActivityWaitingInput, not ActivityBlocked: AO answers it with a message,
	// where a Paseo permission needs an explicit decision instead.
	ExecutionReportQuestion ExecutionReportType = "question"
	// ExecutionReportBlocked is the agent reporting it cannot proceed.
	ExecutionReportBlocked ExecutionReportType = "blocked"
	// ExecutionReportResult is the agent's own claim that it finished. It is
	// evidence for a human or an AO rule, never the completion fact itself.
	ExecutionReportResult ExecutionReportType = "result"
	// ExecutionReportFailure is the agent's own claim that it failed. Retry
	// scheduling stays AO's decision.
	ExecutionReportFailure ExecutionReportType = "failure"
	// ExecutionReportFollowUp is proposed additional work. Proposed only; it
	// never creates or dispatches anything.
	ExecutionReportFollowUp ExecutionReportType = "follow_up_proposal"
)

// ExecutionReportGap is the event type AO writes when a launch's report sequence
// skips. It records that something was missed so a human or a later message can
// ask for re-emission; a gap is never used to reconstruct the missing report.
const ExecutionReportGap = "report_gap"

// ExecutionReportEvent is one agent-authored report as it arrived. Identity and
// ordering are emitter-minted because Paseo supplies neither: EventID is the
// dedupe key and Seq is monotonic per launch.
//
// RawLine is the on-wire evidence, stored before the report is applied, so a
// crash between recording and applying replays instead of losing it.
type ExecutionReportEvent struct {
	SessionID   SessionID
	HostID      ExecutionHostID
	LaunchID    string
	EventID     string
	Seq         int64
	Type        ExecutionReportType
	Transport   ExecutionEventTransport
	PayloadJSON string
	RawLine     string
	ObservedAt  time.Time
}

// ErrExecutionEventCursorUnknown reports an `after` cursor naming an event the
// session does not have; the caller's cursor is stale or fabricated.
var ErrExecutionEventCursorUnknown = errors.New("execution event cursor does not name a stored event")

// ExecutionEventRecord is one durable ingested row as the read API serves it:
// the fact as it arrived, its transport, and when AO saw and stored it. It is
// a projection of the execution_events table and is never written through.
type ExecutionEventRecord struct {
	ID          string
	SessionID   SessionID
	HostID      ExecutionHostID
	LaunchID    string
	EventType   string
	Transport   ExecutionEventTransport
	PayloadJSON string
	ObservedAt  time.Time
	IngestedAt  time.Time
	Applied     bool
}

// ExecutionAgentQuestion is a question an agent asked AO to put to a human. It
// is distinct from ExecutionPermissionQuestion: this one is answerable with
// text, carries no host-side request id, and is forgeable by anything that can
// write to the transcript, so it may never authorize an action on its own.
type ExecutionAgentQuestion struct {
	SessionID      SessionID
	WorkItemID     string
	EventID        string
	Question       string
	Recommendation string
	Options        []string
	CreatedAt      time.Time
}

// SessionCheckpoint is durable mid-run progress evidence. Sequence is the
// report sequence it arrived with, so a replayed report lands on the same row.
type SessionCheckpoint struct {
	SessionID      SessionID
	Sequence       int64
	Summary        string
	CompletedSteps []string
	RemainingSteps []string
	TestEvidence   []string
	CommitSHA      string
	BranchPushed   bool
	CreatedAt      time.Time
}

// ExecutionEventWindow is a cursored slice of a remote terminal.
//
// Lines are *screen* lines: the remote surface is a PTY that hard-wraps at its
// column width, so a logical line longer than that arrives split across
// several entries. TotalLines is monotonic and is the only real cursor the
// Paseo CLI exposes.
type ExecutionEventWindow struct {
	TerminalID string
	Lines      []string
	TotalLines int64
}
