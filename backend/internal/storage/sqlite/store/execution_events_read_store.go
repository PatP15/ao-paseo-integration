package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/gen"
)

// ListSessionExecutionEvents pages one session's ingested execution events in
// (ingested_at, id) order. afterID names the last event the caller already
// holds; empty starts from the beginning. An afterID the session does not have
// returns domain.ErrExecutionEventCursorUnknown rather than restarting from
// the top, which would silently re-deliver everything.
func (s *Store) ListSessionExecutionEvents(
	ctx context.Context, sessionID domain.SessionID, afterID string, limit int,
) ([]domain.ExecutionEventRecord, error) {
	if sessionID == "" {
		return nil, fmt.Errorf("list execution events: session id is required")
	}
	if limit < 1 {
		return nil, fmt.Errorf("list execution events: limit must be positive")
	}
	afterIngestedAt := ""
	if afterID != "" {
		cursor, err := s.qr.GetExecutionEventCursor(ctx, gen.GetExecutionEventCursorParams{
			SessionID: string(sessionID), ID: afterID,
		})
		if errors.Is(err, sql.ErrNoRows) {
			return nil, domain.ErrExecutionEventCursorUnknown
		}
		if err != nil {
			return nil, fmt.Errorf("resolve execution event cursor %s: %w", afterID, err)
		}
		afterIngestedAt, afterID = cursor.IngestedAt, cursor.ID
	}
	rows, err := s.qr.ListExecutionEventsBySessionAfter(ctx, gen.ListExecutionEventsBySessionAfterParams{
		SessionID: string(sessionID), AfterIngestedAt: afterIngestedAt, AfterID: afterID,
		RowLimit: int64(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("list execution events for session %s: %w", sessionID, err)
	}
	events := make([]domain.ExecutionEventRecord, 0, len(rows))
	for _, row := range rows {
		observedAt, err := decodeExecutionTime(row.ObservedAt)
		if err != nil {
			return nil, fmt.Errorf("decode execution event %s observed_at: %w", row.ID, err)
		}
		ingestedAt, err := decodeExecutionTime(row.IngestedAt)
		if err != nil {
			return nil, fmt.Errorf("decode execution event %s ingested_at: %w", row.ID, err)
		}
		events = append(events, domain.ExecutionEventRecord{
			ID: row.ID, SessionID: domain.SessionID(row.SessionID), HostID: domain.ExecutionHostID(row.HostID),
			LaunchID: row.LaunchID, EventType: row.EventType,
			Transport: domain.ExecutionEventTransport(row.Transport), PayloadJSON: row.PayloadJson,
			ObservedAt: observedAt, IngestedAt: ingestedAt, Applied: row.Applied != 0,
		})
	}
	return events, nil
}
