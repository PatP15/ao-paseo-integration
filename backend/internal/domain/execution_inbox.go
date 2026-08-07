package domain

import (
	"errors"
	"time"
)

var (
	// ErrExecutionQuestionNotOpen reports that an inbox item was already answered
	// or cancelled. Resolving one is a refusal rather than a silent success, so a
	// human who double-clicks does not send the host a second decision.
	ErrExecutionQuestionNotOpen = errors.New("domain: execution question is not open")
	// ErrSessionNotRemote reports that a session has no execution binding, so
	// there is no host to deliver a decision to.
	ErrSessionNotRemote = errors.New("domain: session has no execution binding")
)

// ExecutionQuestionSource says which surface produced a human-inbox item, and it
// determines how that item may be answered. The values match the
// human_questions.source CHECK.
//
// The distinction is not cosmetic. An agent-authored question is forgeable by
// anything that can write to a transcript, so it may only ever be answered with
// text. A host-side permission request carries real authority and can only be
// discharged by an explicit decision quoting the host's full request id.
type ExecutionQuestionSource string

// Human-inbox sources.
const (
	// ExecutionQuestionAgentEvent is a question an agent asked a human.
	ExecutionQuestionAgentEvent ExecutionQuestionSource = "agent_event"
	// ExecutionQuestionPaseoPermission is a host-side permission request.
	ExecutionQuestionPaseoPermission ExecutionQuestionSource = "paseo_permission"
)

// ExecutionInboxQuestion is one open item a human owes an answer on.
//
// ExternalID is the source's own identifier: the report event id for an agent
// question, and the host's **full** permission request id for a permission. It
// is never shortened in storage or on the wire, because a truncated id is
// rejected by the host and an omitted one approves everything.
type ExecutionInboxQuestion struct {
	ID             string
	SessionID      SessionID
	WorkItemID     string
	Source         ExecutionQuestionSource
	ExternalID     string
	Question       string
	Recommendation string
	Options        []string
	CreatedAt      time.Time
}

// ExecutionPermissionDecision is the closed set of decisions AO can record for a
// host-side permission request.
//
// There are exactly two, deliberately: the host enforces a single pending
// request at a time and offers no durable "always allow this tool" scope. A
// broader decision would be a promise AO cannot keep, so the vocabulary stops
// where the host's enforcement stops.
type ExecutionPermissionDecision string

// Permission decisions.
const (
	ExecutionPermissionAllow ExecutionPermissionDecision = "allow"
	ExecutionPermissionDeny  ExecutionPermissionDecision = "deny"
)

// CommandType returns the outbox command that delivers this decision.
func (d ExecutionPermissionDecision) CommandType() (ExecutionCommandType, bool) {
	switch d {
	case ExecutionPermissionAllow:
		return ExecutionCommandAnswerPermission, true
	case ExecutionPermissionDeny:
		return ExecutionCommandDenyPermission, true
	default:
		return "", false
	}
}

// ExecutionQuestionResolution is everything AO commits when a human decides one
// inbox item: the recorded answer, the outbox command that will carry it to the
// host, and the audit entry naming who decided.
//
// All three land in one transaction. Otherwise a crash could leave a question
// marked answered with nothing on the way to the host, or a decision in flight
// with no record of who authorized it.
type ExecutionQuestionResolution struct {
	QuestionID  string
	Answer      string
	AnsweredBy  string
	CommandID   string
	CommandType ExecutionCommandType
	PayloadJSON string
	AuditID     string
	AuditType   string
	DecidedAt   time.Time
}

// ExecutionAnswerPayload is the send_message payload for a human's answer to an
// agent-authored question. It names the question so a redelivery is traceable to
// the item it discharges.
type ExecutionAnswerPayload struct {
	QuestionID string `json:"questionId"`
	Message    string `json:"message"`
}

// ExecutionPermissionPayload is the payload for a permission decision.
//
// RequestID is the host's full request id, copied from what AO observed. The
// adapter passes it verbatim: the host rejects a truncated id, and a decision
// sent with no id at all approves every pending request on the agent.
type ExecutionPermissionPayload struct {
	QuestionID string                      `json:"questionId"`
	RequestID  string                      `json:"requestId"`
	Decision   ExecutionPermissionDecision `json:"decision"`
	Note       string                      `json:"note,omitempty"`
}
