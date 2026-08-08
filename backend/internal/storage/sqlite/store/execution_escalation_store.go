package store

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/gen"
)

// EscalateExecutionAttempt atomically abandons an ambiguous, unbound workspace
// create and rewrites its start command onto a fresh attempt identity. The
// delivery count is retained: escalation changes the remote idempotency scope,
// not the fact that command delivery was attempted.
func (s *Store) EscalateExecutionAttempt(ctx context.Context, sessionID domain.SessionID) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	return s.inTx(ctx, "escalate execution attempt", func(q *gen.Queries) error {
		binding, err := q.GetSessionExecutionBinding(ctx, string(sessionID))
		if err != nil {
			return fmt.Errorf("get execution binding for session %s: %w", sessionID, err)
		}
		commands, err := q.ListExecutionCommandsBySession(ctx, string(sessionID))
		if err != nil {
			return fmt.Errorf("list execution commands for session %s: %w", sessionID, err)
		}
		var start *gen.ExecutionCommand
		for i := range commands {
			command := &commands[i]
			if command.CommandType != string(domain.ExecutionCommandStartAgent) {
				continue
			}
			if start != nil {
				return fmt.Errorf("session %s has multiple start_agent commands", sessionID)
			}
			start = command
		}
		if start == nil {
			return fmt.Errorf("session %s has no start_agent command", sessionID)
		}

		var payload domain.ExecutionStartPayload
		if err := json.Unmarshal([]byte(start.PayloadJson), &payload); err != nil {
			return fmt.Errorf("decode start_agent command %s: %w", start.ID, err)
		}
		if payload.Attempt != int(binding.Attempt) || string(payload.IntentID) != binding.IntentID {
			return fmt.Errorf(
				"session %s attempt identity drift: binding=%d/%s command=%d/%s",
				sessionID, binding.Attempt, binding.IntentID, payload.Attempt, payload.IntentID,
			)
		}

		newAttempt := int(binding.Attempt) + 1
		newIntent := uuid.NewString()
		newLaunchID := uuid.NewString()
		workspaceTitle := fmt.Sprintf("ao:%s:%d", sessionID, newAttempt)
		payload.Attempt = newAttempt
		payload.IntentID = domain.ExecutionIntentID(newIntent)
		payload.LaunchID = newLaunchID
		payloadJSON, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("encode escalated start_agent command %s: %w", start.ID, err)
		}

		bindingRows, err := q.EscalateSessionExecutionBinding(ctx, gen.EscalateSessionExecutionBindingParams{
			WorkspaceTitle: workspaceTitle,
			IntentID:       newIntent,
			LaunchID:       newLaunchID,
			NewAttempt:     int64(newAttempt),
			SessionID:      string(sessionID),
			PriorAttempt:   binding.Attempt,
		})
		if err != nil {
			return fmt.Errorf("advance execution binding for session %s: %w", sessionID, err)
		}
		if bindingRows != 1 {
			return fmt.Errorf("advance execution binding for session %s: stale or already bound", sessionID)
		}
		commandRows, err := q.RewriteExecutionStartAttempt(ctx, gen.RewriteExecutionStartAttemptParams{
			PayloadJson:    string(payloadJSON),
			IdempotencyKey: fmt.Sprintf("%s:%d:%s", sessionID, newAttempt, domain.ExecutionCommandStartAgent),
			LastError:      fmt.Sprintf("provision outcome unknown; escalated to attempt %d", newAttempt),
			ID:             start.ID,
			SessionID:      string(sessionID),
		})
		if err != nil {
			return fmt.Errorf("rewrite start_agent command %s: %w", start.ID, err)
		}
		if commandRows != 1 {
			return fmt.Errorf("rewrite start_agent command %s: not pending or delivering", start.ID)
		}
		return nil
	})
}
