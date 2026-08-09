package paseoevent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// SchemaAgentEvent is the only agent-event schema AO accepts. A report naming
// anything else is dropped rather than guessed at.
const SchemaAgentEvent = "ao.agent-event.v1"

// Event is one agent-authored report.
//
// EventID, Seq, and LaunchID are all emitter-minted, because Paseo supplies no
// event id, no sequence, and no cursor on its transcript. EventID is the dedupe
// key, which is what makes full replay free. Seq is used for gap detection only:
// a hole means AO knows it missed something, never that it should try to
// reconstruct it. LaunchID scopes both, so reports from a superseded launch are
// discarded instead of applied to the current one.
type Event struct {
	Schema    string                     `json:"schema"`
	EventID   string                     `json:"eventId"`
	SessionID string                     `json:"sessionId"`
	LaunchID  string                     `json:"launchId"`
	Seq       int64                      `json:"seq"`
	Type      domain.ExecutionReportType `json:"type"`
	Payload   json.RawMessage            `json:"payload"`
}

// QuestionPayload is a question or a blocked report. Options are offered to a
// human; AO never picks one on the agent's behalf.
type QuestionPayload struct {
	Question       string   `json:"question"`
	Recommendation string   `json:"recommendation,omitempty"`
	Options        []string `json:"options,omitempty"`
	Blocking       bool     `json:"blocking,omitempty"`
}

// CheckpointPayload is mid-run progress evidence.
type CheckpointPayload struct {
	Summary        string   `json:"summary"`
	CompletedSteps []string `json:"completedSteps,omitempty"`
	RemainingSteps []string `json:"remainingSteps,omitempty"`
	TestEvidence   []string `json:"testEvidence,omitempty"`
	CommitSHA      string   `json:"commitSha,omitempty"`
	BranchPushed   bool     `json:"branchPushed,omitempty"`
}

// OutcomePayload is the agent's own account of finishing or failing. AO records
// it as evidence; whether the work item is done stays AO's decision.
type OutcomePayload struct {
	Summary  string   `json:"summary"`
	Evidence []string `json:"evidence,omitempty"`
}

// FollowUpPayload is proposed additional work. Proposed only.
type FollowUpPayload struct {
	Title     string `json:"title"`
	Rationale string `json:"rationale,omitempty"`
}

// DecodeEvent parses and fully validates one report payload.
//
// Parsing is strict — unknown fields, trailing JSON, an unknown type, or a
// payload that does not carry its type's required field are all rejected. A
// report AO cannot understand completely is not applied partially.
func DecodeEvent(payload []byte) (Event, error) {
	if len(payload) > MaxPayloadBytes {
		return Event{}, fmt.Errorf("report is %d bytes, over the %d byte cap", len(payload), MaxPayloadBytes)
	}
	event, err := decodeStrict[Event](payload)
	if err != nil {
		return Event{}, err
	}
	if event.Schema != SchemaAgentEvent {
		return Event{}, fmt.Errorf("report schema is %q, want %q", event.Schema, SchemaAgentEvent)
	}
	if strings.TrimSpace(event.EventID) == "" {
		return Event{}, fmt.Errorf("report has no event id")
	}
	if strings.TrimSpace(event.LaunchID) == "" {
		return Event{}, fmt.Errorf("report has no launch id")
	}
	if event.Seq < 1 {
		return Event{}, fmt.Errorf("report sequence %d is not positive", event.Seq)
	}
	if err := event.validatePayload(); err != nil {
		return Event{}, err
	}
	return event, nil
}

func (e Event) validatePayload() error {
	switch e.Type {
	case domain.ExecutionReportQuestion, domain.ExecutionReportBlocked:
		question, err := e.Question()
		if err != nil {
			return err
		}
		if strings.TrimSpace(question.Question) == "" {
			return fmt.Errorf("%s report has no question text", e.Type)
		}
	case domain.ExecutionReportCheckpoint:
		checkpoint, err := e.Checkpoint()
		if err != nil {
			return err
		}
		if strings.TrimSpace(checkpoint.Summary) == "" {
			return fmt.Errorf("checkpoint report has no summary")
		}
	case domain.ExecutionReportResult, domain.ExecutionReportFailure:
		outcome, err := e.Outcome()
		if err != nil {
			return err
		}
		if strings.TrimSpace(outcome.Summary) == "" {
			return fmt.Errorf("%s report has no summary", e.Type)
		}
	case domain.ExecutionReportFollowUp:
		followUp, err := e.FollowUp()
		if err != nil {
			return err
		}
		if strings.TrimSpace(followUp.Title) == "" {
			return fmt.Errorf("follow-up report has no title")
		}
	default:
		return fmt.Errorf("unknown report type %q", e.Type)
	}
	return nil
}

// Question decodes a question or blocked payload.
func (e Event) Question() (QuestionPayload, error) {
	return decodeStrict[QuestionPayload](e.Payload)
}

// Checkpoint decodes a checkpoint payload.
func (e Event) Checkpoint() (CheckpointPayload, error) {
	return decodeStrict[CheckpointPayload](e.Payload)
}

// Outcome decodes a result or failure payload.
func (e Event) Outcome() (OutcomePayload, error) {
	return decodeStrict[OutcomePayload](e.Payload)
}

// FollowUp decodes a follow-up proposal payload.
func (e Event) FollowUp() (FollowUpPayload, error) {
	return decodeStrict[FollowUpPayload](e.Payload)
}

func decodeStrict[T any](data []byte) (T, error) {
	var value T
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return value, fmt.Errorf("decode report: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return value, fmt.Errorf("decode report: trailing JSON")
		}
		return value, fmt.Errorf("decode report: %w", err)
	}
	return value, nil
}
