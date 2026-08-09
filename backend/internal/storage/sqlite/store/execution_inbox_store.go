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

// GetExecutionQuestion returns one human-inbox item by id.
func (s *Store) GetExecutionQuestion(ctx context.Context, id string) (domain.ExecutionInboxQuestion, bool, error) {
	row, err := s.qr.GetHumanQuestion(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ExecutionInboxQuestion{}, false, nil
	}
	if err != nil {
		return domain.ExecutionInboxQuestion{}, false, fmt.Errorf("get human question %s: %w", id, err)
	}
	question, err := executionInboxQuestionFromGen(row)
	if err != nil {
		return domain.ExecutionInboxQuestion{}, false, err
	}
	return question, true, nil
}

// ListOpenExecutionQuestions returns every unanswered human-inbox item, both
// agent-authored questions and host-side permission requests.
//
// Both sources come back from one call because they are one queue to the human
// looking at them; the Source field is what tells the caller which of the two
// answer paths is legal for a given row.
func (s *Store) ListOpenExecutionQuestions(ctx context.Context) ([]domain.ExecutionInboxQuestion, error) {
	rows, err := s.qr.ListOpenHumanQuestions(ctx)
	if err != nil {
		return nil, fmt.Errorf("list open human questions: %w", err)
	}
	questions := make([]domain.ExecutionInboxQuestion, 0, len(rows))
	for _, row := range rows {
		question, err := executionInboxQuestionFromGen(row)
		if err != nil {
			return nil, err
		}
		questions = append(questions, question)
	}
	return questions, nil
}

// ResolveExecutionQuestion commits a human decision: it records the answer,
// appends the delivering command to the session's FIFO, and writes the audit
// entry, all in one transaction.
//
// Nothing remote happens here. The command is durable before any host is
// contacted, which is what makes a crash mid-decision replay rather than lose
// the human's answer.
func (s *Store) ResolveExecutionQuestion(
	ctx context.Context,
	resolution domain.ExecutionQuestionResolution,
) (domain.ExecutionCommand, error) {
	if resolution.QuestionID == "" || resolution.CommandID == "" || resolution.CommandType == "" {
		return domain.ExecutionCommand{}, fmt.Errorf("invalid question resolution: required field is empty")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	var command domain.ExecutionCommand
	err := s.inTx(ctx, "resolve execution question", func(q *gen.Queries) error {
		row, err := q.GetHumanQuestion(ctx, resolution.QuestionID)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("question %s: %w", resolution.QuestionID, sql.ErrNoRows)
		}
		if err != nil {
			return err
		}
		if row.State != "open" {
			return fmt.Errorf("question %s: %w", resolution.QuestionID, domain.ErrExecutionQuestionNotOpen)
		}
		binding, err := q.GetSessionExecutionBinding(ctx, row.SessionID)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("session %s: %w", row.SessionID, domain.ErrSessionNotRemote)
		}
		if err != nil {
			return err
		}
		sequence, err := q.NextExecutionCommandSequence(ctx, row.SessionID)
		if err != nil {
			return err
		}
		command = domain.ExecutionCommand{
			ID:          resolution.CommandID,
			SessionID:   domain.SessionID(row.SessionID),
			HostID:      domain.ExecutionHostID(binding.HostID),
			Type:        resolution.CommandType,
			PayloadJSON: resolution.PayloadJSON,
			// The question id is the idempotency key: one inbox item can only ever
			// produce one delivery, so a retried API call collides here instead of
			// sending a second answer to the agent.
			IdempotencyKey: fmt.Sprintf("%s:%s", resolution.CommandType, resolution.QuestionID),
			Sequence:       int(sequence),
			State:          domain.ExecutionCommandPending,
			CreatedAt:      resolution.DecidedAt.UTC(),
		}
		if err := q.InsertExecutionCommand(ctx, executionCommandParams(command)); err != nil {
			return fmt.Errorf("insert delivery command: %w", err)
		}
		rows, err := q.AnswerHumanQuestion(ctx, gen.AnswerHumanQuestionParams{
			Answer: resolution.Answer, AnsweredBy: resolution.AnsweredBy,
			AnsweredAt:        encodeExecutionTime(resolution.DecidedAt),
			DeliveryCommandID: nullableString(command.ID), ID: resolution.QuestionID,
		})
		if err != nil {
			return err
		}
		if rows == 0 {
			return fmt.Errorf("question %s: %w", resolution.QuestionID, domain.ErrExecutionQuestionNotOpen)
		}
		detail, err := json.Marshal(map[string]string{
			"question":   resolution.QuestionID,
			"source":     row.Source,
			"external":   row.ExternalQuestionID,
			"session":    row.SessionID,
			"host":       binding.HostID,
			"command":    command.ID,
			"answeredBy": resolution.AnsweredBy,
		})
		if err != nil {
			return fmt.Errorf("marshal audit detail: %w", err)
		}
		auditID := resolution.AuditID
		if auditID == "" {
			auditID = hashHex("question_resolution", resolution.QuestionID, command.ID)
		}
		auditType := resolution.AuditType
		if auditType == "" {
			auditType = "execution.question_answered"
		}
		if _, err := q.InsertAuditEvent(ctx, gen.InsertAuditEventParams{
			ID: auditID, EventType: auditType, ActorType: "human", ActorID: resolution.AnsweredBy,
			SubjectType: "human_question", SubjectID: resolution.QuestionID,
			DetailJson: string(detail), CreatedAt: encodeExecutionTime(resolution.DecidedAt),
		}); err != nil {
			return fmt.Errorf("insert audit event: %w", err)
		}
		return nil
	})
	if err != nil {
		return domain.ExecutionCommand{}, err
	}
	return command, nil
}

func executionInboxQuestionFromGen(row gen.HumanQuestion) (domain.ExecutionInboxQuestion, error) {
	created, err := decodeExecutionTime(row.CreatedAt)
	if err != nil {
		return domain.ExecutionInboxQuestion{}, fmt.Errorf("decode human question %s created time: %w", row.ID, err)
	}
	options := []string{}
	if row.OptionsJson != "" {
		if err := json.Unmarshal([]byte(row.OptionsJson), &options); err != nil {
			return domain.ExecutionInboxQuestion{}, fmt.Errorf("decode human question %s options: %w", row.ID, err)
		}
	}
	return domain.ExecutionInboxQuestion{
		ID:        row.ID,
		SessionID: domain.SessionID(row.SessionID),
		// A question filed against a work item that was later deleted keeps its
		// text and its answerability; only the association is dropped.
		WorkItemID:     row.WorkItemID.String,
		Source:         domain.ExecutionQuestionSource(row.Source),
		ExternalID:     row.ExternalQuestionID,
		Question:       row.Question,
		Recommendation: row.Recommendation,
		Options:        nonNilStrings(options),
		CreatedAt:      created,
	}, nil
}
