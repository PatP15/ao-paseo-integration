package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/gen"
)

// SaveSessionBrief commits one immutable brief version.
//
// There is no update path. The unique (session_id, version) index is what makes
// a concurrent second writer fail rather than silently replace the contract an
// agent may already have been launched under.
func (s *Store) SaveSessionBrief(ctx context.Context, brief domain.SessionBrief) error {
	if brief.ID == "" || brief.SessionID == "" || brief.BriefJSON == "" || brief.ReportNonce == "" {
		return fmt.Errorf("invalid session brief: required field is empty")
	}
	if brief.Version < 1 {
		return fmt.Errorf("invalid session brief: version must be positive")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if err := s.qw.InsertSessionBrief(ctx, gen.InsertSessionBriefParams{
		ID: brief.ID, SessionID: string(brief.SessionID), Version: int64(brief.Version),
		SchemaVersion: brief.SchemaVersion, BriefJson: brief.BriefJSON, BriefSha256: brief.BriefSHA256,
		ReportNonce: brief.ReportNonce, CreatedAt: encodeExecutionTime(brief.CreatedAt),
		SupersedesBriefID: nullableString(brief.SupersedesBriefID),
	}); err != nil {
		return fmt.Errorf("insert brief for session %s: %w", brief.SessionID, err)
	}
	return nil
}

// GetLatestSessionBrief returns the current contract for a session.
func (s *Store) GetLatestSessionBrief(ctx context.Context, sessionID domain.SessionID) (domain.SessionBrief, bool, error) {
	row, err := s.qr.GetLatestSessionBrief(ctx, string(sessionID))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.SessionBrief{}, false, nil
	}
	if err != nil {
		return domain.SessionBrief{}, false, fmt.Errorf("get latest brief for session %s: %w", sessionID, err)
	}
	created, err := decodeExecutionTime(row.CreatedAt)
	if err != nil {
		return domain.SessionBrief{}, false, fmt.Errorf("decode brief %s created time: %w", row.ID, err)
	}
	return domain.SessionBrief{
		ID: row.ID, SessionID: domain.SessionID(row.SessionID), Version: int(row.Version),
		SchemaVersion: row.SchemaVersion, BriefJSON: row.BriefJson, BriefSHA256: row.BriefSha256,
		ReportNonce: row.ReportNonce, CreatedAt: created,
		SupersedesBriefID: row.SupersedesBriefID.String,
	}, true, nil
}

// RecordExecutionReport stores one agent-authored report and reports whether the
// caller still owes it an apply.
//
// The raw line is stored with the payload, before the report is applied, so what
// AO acted on stays auditable and a crash between recording and applying
// replays: an already-recorded report that was never applied answers true again.
func (s *Store) RecordExecutionReport(ctx context.Context, event domain.ExecutionReportEvent) (bool, error) {
	if event.SessionID == "" || event.EventID == "" || event.Type == "" ||
		event.Transport == "" || event.PayloadJSON == "" {
		return false, fmt.Errorf("invalid execution report: required field is empty")
	}
	at := encodeExecutionTime(event.ObservedAt)
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	rows, err := s.qw.InsertExecutionEvent(ctx, gen.InsertExecutionEventParams{
		// The emitter-minted id is the identity, so a report replayed from a
		// full transcript re-read collides with itself rather than duplicating.
		ID:        hashHex("execution_report", string(event.SessionID), event.EventID),
		SessionID: string(event.SessionID), HostID: string(event.HostID), LaunchID: event.LaunchID,
		ProtocolEventID: event.EventID, ProtocolSeq: sql.NullInt64{Int64: event.Seq, Valid: event.Seq > 0},
		EventType: string(event.Type), Transport: string(event.Transport),
		PayloadJson: event.PayloadJSON, PayloadSha256: hashHex(event.PayloadJSON),
		RawLine: event.RawLine, ObservedAt: at, IngestedAt: at,
	})
	if err != nil {
		return false, fmt.Errorf("insert report %s for session %s: %w", event.EventID, event.SessionID, err)
	}
	if rows > 0 {
		return true, nil
	}
	applied, err := s.qw.GetExecutionReportApplied(ctx, gen.GetExecutionReportAppliedParams{
		SessionID: string(event.SessionID), ProtocolEventID: event.EventID,
	})
	if err != nil {
		return false, fmt.Errorf("read applied state of report %s: %w", event.EventID, err)
	}
	return applied == 0, nil
}

// MarkExecutionReportApplied records that a report's effects have been written.
func (s *Store) MarkExecutionReportApplied(ctx context.Context, sessionID domain.SessionID, eventID string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.qw.MarkExecutionReportApplied(ctx, gen.MarkExecutionReportAppliedParams{
		SessionID: string(sessionID), ProtocolEventID: eventID,
	}); err != nil {
		return fmt.Errorf("mark report %s applied: %w", eventID, err)
	}
	return nil
}

// OpenExecutionAgentQuestion files an agent's question in the human inbox and
// reports whether it was newly opened.
//
// The source is agent_event rather than paseo_permission, and the difference is
// load-bearing: this one is answerable with a message, where a host-side
// permission request needs an explicit decision carrying the host's full request
// id. The row id is derived from the report's event id, so a replayed report
// does not file the same question twice.
func (s *Store) OpenExecutionAgentQuestion(ctx context.Context, question domain.ExecutionAgentQuestion) (bool, error) {
	if question.SessionID == "" || question.EventID == "" || question.Question == "" {
		return false, fmt.Errorf("invalid agent question: required field is empty")
	}
	options, err := json.Marshal(nonNilStrings(question.Options))
	if err != nil {
		return false, fmt.Errorf("marshal agent question options: %w", err)
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	rows, err := s.qw.InsertHumanQuestion(ctx, gen.InsertHumanQuestionParams{
		ID:        hashHex("agent_event", string(question.SessionID), question.EventID),
		SessionID: string(question.SessionID), WorkItemID: nullableString(question.WorkItemID),
		Source: "agent_event", ExternalQuestionID: question.EventID, Question: question.Question,
		Recommendation: question.Recommendation, OptionsJson: string(options), State: "open",
		CreatedAt: encodeExecutionTime(question.CreatedAt),
	})
	if err != nil {
		return false, fmt.Errorf("open agent question for session %s: %w", question.SessionID, err)
	}
	return rows > 0, nil
}

// RecordSessionCheckpoint stores mid-run progress evidence and reports whether
// it was new. A checkpoint is evidence only: it never marks work complete.
func (s *Store) RecordSessionCheckpoint(ctx context.Context, checkpoint domain.SessionCheckpoint) (bool, error) {
	if checkpoint.SessionID == "" || checkpoint.Summary == "" {
		return false, fmt.Errorf("invalid session checkpoint: required field is empty")
	}
	if checkpoint.Sequence < 1 {
		return false, fmt.Errorf("invalid session checkpoint: sequence must be positive")
	}
	completed, err := json.Marshal(nonNilStrings(checkpoint.CompletedSteps))
	if err != nil {
		return false, fmt.Errorf("marshal checkpoint completed steps: %w", err)
	}
	remaining, err := json.Marshal(nonNilStrings(checkpoint.RemainingSteps))
	if err != nil {
		return false, fmt.Errorf("marshal checkpoint remaining steps: %w", err)
	}
	evidence, err := json.Marshal(nonNilStrings(checkpoint.TestEvidence))
	if err != nil {
		return false, fmt.Errorf("marshal checkpoint test evidence: %w", err)
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	rows, err := s.qw.InsertSessionCheckpoint(ctx, gen.InsertSessionCheckpointParams{
		ID:        hashHex("session_checkpoint", string(checkpoint.SessionID), fmt.Sprint(checkpoint.Sequence)),
		SessionID: string(checkpoint.SessionID), Sequence: checkpoint.Sequence, Summary: checkpoint.Summary,
		CompletedStepsJson: string(completed), RemainingStepsJson: string(remaining),
		TestEvidenceJson: string(evidence), CommitSha: checkpoint.CommitSHA,
		BranchPushed: executionBoolInt(checkpoint.BranchPushed),
		CreatedAt:    encodeExecutionTime(checkpoint.CreatedAt),
	})
	if err != nil {
		return false, fmt.Errorf("record checkpoint for session %s: %w", checkpoint.SessionID, err)
	}
	return rows > 0, nil
}

// AdvanceExecutionEventCursor moves a session's report cursor forward on the
// terminal it was read from. A cursor for a different terminal, or one already
// past this position, is left alone.
func (s *Store) AdvanceExecutionEventCursor(
	ctx context.Context,
	sessionID domain.SessionID,
	terminalID string,
	consumed int64,
) error {
	if terminalID == "" {
		return fmt.Errorf("invalid report cursor: terminal id is empty")
	}
	if consumed < 0 {
		return fmt.Errorf("invalid report cursor: consumed lines must not be negative")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.qw.AdvanceExecutionEventCursor(ctx, gen.AdvanceExecutionEventCursorParams{
		Consumed: consumed, SessionID: string(sessionID), TerminalID: terminalID,
	}); err != nil {
		return fmt.Errorf("advance report cursor for session %s: %w", sessionID, err)
	}
	return nil
}

func nonNilStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}
