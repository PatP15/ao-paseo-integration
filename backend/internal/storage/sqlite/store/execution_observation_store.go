package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/gen"
)

// RecordExecutionHostProbe persists the outcome of one host status probe.
//
// A probe never rewrites a host's recorded server id once it has one. Doing so
// would erase the very evidence that the identity changed, and that fact
// invalidates every agent id AO holds for the host.
func (s *Store) RecordExecutionHostProbe(ctx context.Context, probe domain.ExecutionHostProbe) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.inTx(ctx, "record execution host probe", func(q *gen.Queries) error {
		row, err := q.GetExecutionHost(ctx, string(probe.HostID))
		if err != nil {
			return fmt.Errorf("get execution host %s: %w", probe.HostID, err)
		}
		host, err := executionHostFromGen(row)
		if err != nil {
			return err
		}
		at := probe.ObservedAt.UTC()
		if probe.Reachable {
			host.LastSuccessfulProbeAt = at
			host.LastProbeError = ""
			if host.ServerID == "" {
				host.ServerID = probe.ServerID
			}
			if probe.Version != "" {
				host.PaseoVersion = probe.Version
			}
		} else {
			host.LastFailedProbeAt = at
			host.LastProbeError = probe.Error
		}
		host.UpdatedAt = at
		return q.UpsertExecutionHost(ctx, executionHostParams(host))
	})
}

// RecordExecutionObservation stores one remote fact and reports whether it was
// new. The row id and content hash are derived from the payload, so the same
// observation seen on every tick collapses to one durable row while a genuine
// change inserts a new one.
func (s *Store) RecordExecutionObservation(ctx context.Context, event domain.ExecutionObservationEvent) (bool, error) {
	if event.SessionID == "" || event.Type == "" || event.Transport == "" || event.PayloadJSON == "" {
		return false, fmt.Errorf("invalid execution observation: required field is empty")
	}
	payloadSum := hashHex(event.PayloadJSON)
	at := encodeExecutionTime(event.ObservedAt)
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	rows, err := s.qw.InsertExecutionEvent(ctx, gen.InsertExecutionEventParams{
		ID:        hashHex(string(event.SessionID), event.Type, payloadSum),
		SessionID: string(event.SessionID), HostID: string(event.HostID), LaunchID: event.LaunchID,
		ProtocolSeq: sql.NullInt64{}, EventType: event.Type, Transport: string(event.Transport),
		PayloadJson: event.PayloadJSON, PayloadSha256: payloadSum,
		ObservedAt: at, IngestedAt: at,
	})
	if err != nil {
		return false, fmt.Errorf("insert execution observation for session %s: %w", event.SessionID, err)
	}
	return rows > 0, nil
}

// OpenExecutionPermissionQuestion files a remote permission request in the
// human inbox and reports whether it was newly opened. Re-observing the same
// unanswered request is a no-op.
//
// The returned id is the inbox question id — the one the decision endpoint
// takes — whether or not this call inserted the row.
func (s *Store) OpenExecutionPermissionQuestion(ctx context.Context, question domain.ExecutionPermissionQuestion) (string, bool, error) {
	if question.SessionID == "" || question.ExternalID == "" || question.Question == "" {
		return "", false, fmt.Errorf("invalid execution permission question: required field is empty")
	}
	id := hashHex("paseo_permission", string(question.SessionID), question.ExternalID)
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	rows, err := s.qw.InsertHumanQuestion(ctx, gen.InsertHumanQuestionParams{
		ID:        id,
		SessionID: string(question.SessionID), WorkItemID: nullableString(question.WorkItemID),
		Source: "paseo_permission", ExternalQuestionID: question.ExternalID,
		Question: question.Question, OptionsJson: "[]", State: "open",
		CreatedAt: encodeExecutionTime(question.CreatedAt),
	})
	if err != nil {
		return "", false, fmt.Errorf("open permission question for session %s: %w", question.SessionID, err)
	}
	return id, rows > 0, nil
}

// ListOpenExecutionPermissionQuestions returns the unanswered remote permission
// requests AO owes a decision on. ToolName is not returned: it is folded into
// the question text rather than stored as its own fact.
func (s *Store) ListOpenExecutionPermissionQuestions(ctx context.Context) ([]domain.ExecutionPermissionQuestion, error) {
	rows, err := s.qr.ListOpenHumanQuestions(ctx)
	if err != nil {
		return nil, fmt.Errorf("list open human questions: %w", err)
	}
	questions := make([]domain.ExecutionPermissionQuestion, 0, len(rows))
	for _, row := range rows {
		if row.Source != "paseo_permission" {
			continue
		}
		created, err := decodeExecutionTime(row.CreatedAt)
		if err != nil {
			return nil, fmt.Errorf("decode human question %s created time: %w", row.ID, err)
		}
		questions = append(questions, domain.ExecutionPermissionQuestion{
			SessionID: domain.SessionID(row.SessionID), WorkItemID: row.WorkItemID.String,
			ExternalID: row.ExternalQuestionID, Question: row.Question, CreatedAt: created,
		})
	}
	return questions, nil
}

// RecordExecutionOrphan writes an audit finding about a remote resource AO's
// state does not account for, and reports whether it was new. The id is derived
// from the finding's identity so a sweep that re-sees it every five minutes
// does not fill the audit log.
func (s *Store) RecordExecutionOrphan(ctx context.Context, orphan domain.ExecutionOrphan) (bool, error) {
	if orphan.Kind == "" || orphan.HostID == "" {
		return false, fmt.Errorf("invalid execution orphan: required field is empty")
	}
	detail, err := json.Marshal(map[string]string{
		"kind": string(orphan.Kind), "host": string(orphan.HostID), "session": string(orphan.SessionID),
		"agent": string(orphan.AgentID), "workspacePath": orphan.WorkspacePath, "detail": orphan.Detail,
	})
	if err != nil {
		return false, fmt.Errorf("marshal execution orphan detail: %w", err)
	}
	subjectID := string(orphan.AgentID)
	if subjectID == "" {
		subjectID = string(orphan.HostID)
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	rows, err := s.qw.InsertAuditEvent(ctx, gen.InsertAuditEventParams{
		ID:        hashHex("execution_orphan", string(orphan.Kind), string(orphan.HostID), string(orphan.SessionID), string(orphan.AgentID)),
		EventType: "execution.orphan_detected", ActorType: "observer", ActorID: "paseo",
		SubjectType: "execution_agent", SubjectID: subjectID, DetailJson: string(detail),
		CreatedAt: encodeExecutionTime(orphan.ObservedAt),
	})
	if err != nil {
		return false, fmt.Errorf("record execution orphan on host %s: %w", orphan.HostID, err)
	}
	return rows > 0, nil
}

// MarkSessionExecutionObserved advances a live binding's observation clock.
// An archived binding is left alone.
func (s *Store) MarkSessionExecutionObserved(ctx context.Context, sessionID domain.SessionID, at time.Time) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.qw.TouchSessionExecutionBinding(ctx, gen.TouchSessionExecutionBindingParams{
		LastObservedAt: encodeExecutionTime(at), SessionID: string(sessionID),
	})
	if err != nil {
		return fmt.Errorf("mark session %s observed: %w", sessionID, err)
	}
	return nil
}

// hashHex derives a stable identifier from parts. The separator is a byte that
// cannot occur in any part, so ("a","bc") and ("ab","c") cannot collide.
func hashHex(parts ...string) string {
	digest := sha256.New()
	for _, part := range parts {
		digest.Write([]byte(part))
		digest.Write([]byte{0})
	}
	return hex.EncodeToString(digest.Sum(nil))
}
