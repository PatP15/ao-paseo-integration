package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/gen"
)

// CreateExecutionDispatch commits the AO session, active work-item claim,
// remote binding, and first outbox command in one transaction. No execution
// backend call is made by this method.
func (s *Store) CreateExecutionDispatch(ctx context.Context, seed domain.ExecutionDispatchSeed) (domain.ExecutionDispatch, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	var result domain.ExecutionDispatch
	err := s.inTx(ctx, "create execution dispatch", func(q *gen.Queries) error {
		item, err := q.GetWorkItem(ctx, seed.WorkItemID)
		if err != nil {
			return fmt.Errorf("get work item %s: %w", seed.WorkItemID, err)
		}
		if item.ApprovalState != string(domain.WorkItemApproved) {
			return fmt.Errorf("work item %s is not approved", seed.WorkItemID)
		}
		if item.LifecycleFact != string(domain.WorkItemOpen) && item.LifecycleFact != string(domain.WorkItemInProgress) {
			return fmt.Errorf("work item %s is not dispatchable", seed.WorkItemID)
		}
		if seed.Session.ProjectID != domain.ProjectID(item.ProjectID) {
			return fmt.Errorf("work item %s belongs to project %s, not %s", seed.WorkItemID, item.ProjectID, seed.Session.ProjectID)
		}

		host, err := q.GetExecutionHost(ctx, string(seed.HostID))
		if err != nil {
			return fmt.Errorf("get execution host %s: %w", seed.HostID, err)
		}
		if host.Enabled == 0 || host.BackendType != string(domain.ExecutionBackendPaseo) || host.MaxConcurrentSessions <= 0 {
			return fmt.Errorf("execution host %s is not dispatchable", seed.HostID)
		}
		bindingRow, err := q.GetProjectHostBinding(ctx, gen.GetProjectHostBindingParams{
			ProjectID: item.ProjectID, HostID: string(seed.HostID),
		})
		if err != nil {
			return fmt.Errorf("get project host binding: %w", err)
		}
		if bindingRow.Enabled == 0 || bindingRow.HostRepoPath == "" || bindingRow.HostRepoPath != seed.HostRepoPath {
			return fmt.Errorf("project %s is not enabled at execution host %s", item.ProjectID, seed.HostID)
		}
		active, err := q.CountActiveSessionExecutionBindingsByHost(ctx, string(seed.HostID))
		if err != nil {
			return fmt.Errorf("count active host sessions: %w", err)
		}
		if active >= host.MaxConcurrentSessions {
			return fmt.Errorf("execution host %s is at capacity", seed.HostID)
		}

		num, err := q.NextSessionNum(ctx, seed.Session.ProjectID)
		if err != nil {
			return fmt.Errorf("next session num: %w", err)
		}
		now := seed.CreatedAt.UTC()
		if now.IsZero() {
			now = time.Now().UTC()
		}
		rec := seed.Session
		rec.ID = domain.SessionID(fmt.Sprintf("%s-%d", rec.ProjectID, num))
		if rec.Kind == "" {
			rec.Kind = domain.KindWorker
		}
		rec.CreatedAt, rec.UpdatedAt = now, now
		rec.Activity = domain.Activity{State: domain.ActivityIdle, LastActivityAt: now}
		rec.Metadata.Branch = seed.Branch
		rec.Metadata.Prompt = seed.Prompt
		if err := q.InsertSession(ctx, recordToInsert(rec, num)); err != nil {
			return fmt.Errorf("insert session %s: %w", rec.ID, err)
		}
		if err := q.ClaimWorkItemSession(ctx, gen.ClaimWorkItemSessionParams{
			WorkItemID: seed.WorkItemID, SessionID: string(rec.ID), Role: string(domain.WorkItemRoleImplementer),
			AttemptNumber: int64(seed.Attempt), CreatedAt: encodeExecutionTime(now),
		}); err != nil {
			return fmt.Errorf("claim work item %s: %w", seed.WorkItemID, err)
		}

		workspaceTitle := fmt.Sprintf("ao:%s:%d", rec.ID, seed.Attempt)
		binding := domain.SessionExecutionBinding{
			SessionID: rec.ID, WorkItemID: seed.WorkItemID, BackendType: domain.ExecutionBackendPaseo,
			HostID: seed.HostID, BoundServerID: seed.BoundServerID, WorkspaceTitle: workspaceTitle,
			IntentID: seed.IntentID, Attempt: seed.Attempt, LabelsWritten: map[string]string{},
			BranchName: seed.Branch, Provider: seed.Provider, Model: seed.Model, Mode: seed.Mode,
			DispatchGeneration: seed.DispatchGeneration, LaunchID: seed.LaunchID, CreatedAt: now,
		}
		bindingParams, err := sessionExecutionBindingParams(binding)
		if err != nil {
			return err
		}
		if err := q.UpsertSessionExecutionBinding(ctx, bindingParams); err != nil {
			return fmt.Errorf("insert execution binding: %w", err)
		}

		payload, err := json.Marshal(domain.ExecutionStartPayload{
			ProjectID: rec.ProjectID, RepoPath: seed.HostRepoPath, BaseBranch: seed.BaseBranch,
			Branch: seed.Branch, Provider: seed.Provider, Model: seed.Model, Mode: seed.Mode,
			ThinkingOptionID: seed.ThinkingOptionID,
			Prompt:           seed.Prompt, IntentID: seed.IntentID, Attempt: seed.Attempt, LaunchID: seed.LaunchID,
		})
		if err != nil {
			return fmt.Errorf("marshal start command: %w", err)
		}
		command := domain.ExecutionCommand{
			ID: seed.CommandID, SessionID: rec.ID, HostID: seed.HostID,
			Type: domain.ExecutionCommandStartAgent, PayloadJSON: string(payload),
			IdempotencyKey: fmt.Sprintf("%s:%d:%s", rec.ID, seed.Attempt, domain.ExecutionCommandStartAgent),
			Sequence:       1, State: domain.ExecutionCommandPending, CreatedAt: now,
		}
		if err := q.InsertExecutionCommand(ctx, executionCommandParams(command)); err != nil {
			return fmt.Errorf("insert execution command: %w", err)
		}
		// Policy-gated skill overrides are durable audit facts committed with
		// the dispatch itself: queryable later, and gone if the dispatch rolls
		// back. They alter nothing about the launch.
		actor := seed.Actor
		if actor == "" {
			actor = "human"
		}
		for _, skill := range seed.SkillPolicyOverrides {
			detail, err := json.Marshal(map[string]string{
				"skill": skill, "hostId": string(seed.HostID), "sessionId": string(rec.ID),
			})
			if err != nil {
				return fmt.Errorf("marshal skill override audit: %w", err)
			}
			if _, err := q.InsertAuditEvent(ctx, gen.InsertAuditEventParams{
				ID: fmt.Sprintf("%s:skill-override:%s", seed.CommandID, skill),
				EventType: "execution.skill_policy_override", ActorType: "human", ActorID: actor,
				SubjectType: "work_item", SubjectID: seed.WorkItemID,
				DetailJson: string(detail), CreatedAt: encodeExecutionTime(now),
			}); err != nil {
				return fmt.Errorf("insert skill override audit: %w", err)
			}
		}
		result = domain.ExecutionDispatch{Session: rec, Binding: binding, Command: command}
		return nil
	})
	if err != nil {
		return domain.ExecutionDispatch{}, err
	}
	return result, nil
}

// EnqueueExecutionCommand appends a command to its session FIFO. A zero
// sequence is assigned atomically from the current maximum.
func (s *Store) EnqueueExecutionCommand(ctx context.Context, command domain.ExecutionCommand) (domain.ExecutionCommand, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if command.CreatedAt.IsZero() {
		command.CreatedAt = time.Now().UTC()
	}
	if command.State == "" {
		command.State = domain.ExecutionCommandPending
	}
	err := s.inTx(ctx, "enqueue execution command", func(q *gen.Queries) error {
		if command.Sequence == 0 {
			sequence, err := q.NextExecutionCommandSequence(ctx, string(command.SessionID))
			if err != nil {
				return err
			}
			command.Sequence = int(sequence)
		}
		return q.InsertExecutionCommand(ctx, executionCommandParams(command))
	})
	return command, err
}

// ClaimNextExecutionCommand leases the oldest due command whose predecessors
// are acknowledged. Delivering rows become eligible again after their lease.
func (s *Store) ClaimNextExecutionCommand(ctx context.Context, now, leaseUntil time.Time) (domain.ExecutionCommand, bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var claimed domain.ExecutionCommand
	found := false
	err := s.inTx(ctx, "claim execution command", func(q *gen.Queries) error {
		rows, err := q.ListDueExecutionCommands(ctx, gen.ListDueExecutionCommandsParams{
			NextAttemptAt: encodeExecutionTime(now), Limit: 16,
		})
		if err != nil {
			return err
		}
		for _, row := range rows {
			count, err := q.ClaimExecutionCommand(ctx, gen.ClaimExecutionCommandParams{
				NextAttemptAt: encodeExecutionTime(leaseUntil), ID: row.ID,
				NextAttemptAt_2: encodeExecutionTime(now),
			})
			if err != nil {
				return err
			}
			if count == 0 {
				continue
			}
			updated, err := q.GetExecutionCommand(ctx, row.ID)
			if err != nil {
				return err
			}
			claimed, err = executionCommandFromGen(updated)
			if err != nil {
				return err
			}
			found = true
			break
		}
		return nil
	})
	if err != nil {
		return domain.ExecutionCommand{}, false, err
	}
	return claimed, found, nil
}

// RetryExecutionCommand returns a delivering command to the pending queue.
func (s *Store) RetryExecutionCommand(ctx context.Context, command domain.ExecutionCommand, next time.Time, deliveryErr error) error {
	lastError := ""
	if deliveryErr != nil {
		lastError = deliveryErr.Error()
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	rows, err := s.qw.UpdateExecutionCommandDelivery(ctx, gen.UpdateExecutionCommandDeliveryParams{
		State: string(domain.ExecutionCommandPending), AttemptCount: int64(command.AttemptCount),
		NextAttemptAt: encodeExecutionTime(next), LastError: lastError,
		AcknowledgedAt: "", ID: command.ID,
	})
	if err != nil {
		return fmt.Errorf("retry execution command %s: %w", command.ID, err)
	}
	if rows == 0 {
		return fmt.Errorf("retry execution command %s: not found", command.ID)
	}
	return nil
}

// FailExecutionCommand exhausts a command without allowing later commands in
// the same session to overtake it.
func (s *Store) FailExecutionCommand(ctx context.Context, command domain.ExecutionCommand, deliveryErr error) error {
	lastError := ""
	if deliveryErr != nil {
		lastError = deliveryErr.Error()
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.qw.UpdateExecutionCommandDelivery(ctx, gen.UpdateExecutionCommandDeliveryParams{
		State: string(domain.ExecutionCommandFailed), AttemptCount: int64(command.AttemptCount),
		NextAttemptAt: "", LastError: lastError, AcknowledgedAt: "", ID: command.ID,
	})
	return err
}

// AcknowledgeExecutionStart publishes the remote runtime handle and marks the
// start command complete atomically. A crash before this commit safely leaves
// the command lease available for replay.
func (s *Store) AcknowledgeExecutionStart(ctx context.Context, commandID string, sessionID domain.SessionID, runtimeHandle, launchID string, at time.Time) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.inTx(ctx, "acknowledge execution start", func(q *gen.Queries) error {
		command, err := q.GetExecutionCommand(ctx, commandID)
		if err != nil {
			return err
		}
		if command.SessionID != string(sessionID) || command.CommandType != string(domain.ExecutionCommandStartAgent) {
			return fmt.Errorf("command %s does not start session %s", commandID, sessionID)
		}
		recRow, err := q.GetSession(ctx, sessionID)
		if err != nil {
			return err
		}
		rec := rowToRecord(recRow)
		rec.Metadata.RuntimeHandleID = runtimeHandle
		rec.Metadata.RuntimeLaunchID = launchID
		rec.Activity = domain.Activity{State: domain.ActivityActive, LastActivityAt: at}
		rec.UpdatedAt = at
		if err := q.UpdateSession(ctx, recordToUpdate(rec)); err != nil {
			return err
		}
		rows, err := q.AcknowledgeExecutionCommand(ctx, gen.AcknowledgeExecutionCommandParams{
			AcknowledgedAt: encodeExecutionTime(at), ID: commandID,
		})
		if err != nil {
			return err
		}
		if rows == 0 && command.State != string(domain.ExecutionCommandAcknowledged) {
			return fmt.Errorf("command %s is not delivering", commandID)
		}
		binding, err := q.GetSessionExecutionBinding(ctx, string(sessionID))
		if err != nil {
			return err
		}
		if binding.WorkItemID.Valid {
			if _, err := q.MarkWorkItemInProgress(ctx, gen.MarkWorkItemInProgressParams{
				UpdatedAt: encodeExecutionTime(at), ID: binding.WorkItemID.String,
			}); err != nil {
				return err
			}
		}
		return nil
	})
}

// GetExecutionCommand returns one durable outbox row.
func (s *Store) GetExecutionCommand(ctx context.Context, id string) (domain.ExecutionCommand, bool, error) {
	row, err := s.qr.GetExecutionCommand(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ExecutionCommand{}, false, nil
	}
	if err != nil {
		return domain.ExecutionCommand{}, false, err
	}
	command, err := executionCommandFromGen(row)
	return command, err == nil, err
}

// ListExecutionCommandsBySession returns a session's FIFO in sequence order.
func (s *Store) ListExecutionCommandsBySession(ctx context.Context, sessionID domain.SessionID) ([]domain.ExecutionCommand, error) {
	rows, err := s.qr.ListExecutionCommandsBySession(ctx, string(sessionID))
	if err != nil {
		return nil, err
	}
	commands := make([]domain.ExecutionCommand, 0, len(rows))
	for _, row := range rows {
		command, err := executionCommandFromGen(row)
		if err != nil {
			return nil, err
		}
		commands = append(commands, command)
	}
	return commands, nil
}

func executionCommandParams(command domain.ExecutionCommand) gen.InsertExecutionCommandParams {
	return gen.InsertExecutionCommandParams{
		ID: command.ID, SessionID: string(command.SessionID), HostID: string(command.HostID),
		CommandType: string(command.Type), PayloadJson: command.PayloadJSON,
		IdempotencyKey: command.IdempotencyKey, Sequence: int64(command.Sequence), State: string(command.State),
		AttemptCount: int64(command.AttemptCount), NextAttemptAt: encodeExecutionTime(command.NextAttemptAt),
		LastError: command.LastError, CreatedAt: encodeExecutionTime(command.CreatedAt),
		AcknowledgedAt: encodeExecutionTime(command.AcknowledgedAt),
	}
}

func executionCommandFromGen(row gen.ExecutionCommand) (domain.ExecutionCommand, error) {
	next, err := decodeExecutionTime(row.NextAttemptAt)
	if err != nil {
		return domain.ExecutionCommand{}, fmt.Errorf("decode execution command %s next attempt: %w", row.ID, err)
	}
	created, err := decodeExecutionTime(row.CreatedAt)
	if err != nil {
		return domain.ExecutionCommand{}, fmt.Errorf("decode execution command %s created time: %w", row.ID, err)
	}
	acknowledged, err := decodeExecutionTime(row.AcknowledgedAt)
	if err != nil {
		return domain.ExecutionCommand{}, fmt.Errorf("decode execution command %s acknowledged time: %w", row.ID, err)
	}
	return domain.ExecutionCommand{
		ID: row.ID, SessionID: domain.SessionID(row.SessionID), HostID: domain.ExecutionHostID(row.HostID),
		Type: domain.ExecutionCommandType(row.CommandType), PayloadJSON: row.PayloadJson,
		IdempotencyKey: row.IdempotencyKey, Sequence: int(row.Sequence), State: domain.ExecutionCommandState(row.State),
		AttemptCount: int(row.AttemptCount), NextAttemptAt: next, LastError: row.LastError,
		CreatedAt: created, AcknowledgedAt: acknowledged,
	}, nil
}
