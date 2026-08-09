package paseo

import (
	"context"
	"fmt"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// Schedules lists one host's recurring schedules as the daemon reports them.
//
// Heartbeats are the documented blind spot: the pinned CLI has no heartbeat
// listing, so an empty result here proves nothing about heartbeats.
func (b *Backend) Schedules(ctx context.Context, hostID domain.ExecutionHostID) ([]domain.ExecutionHostSchedule, error) {
	if _, err := b.registeredHost(ctx, hostID); err != nil {
		return nil, err
	}
	listed, err := b.client.ListSchedules(ctx)
	if err != nil {
		return nil, fmt.Errorf("list schedules on execution host %s: %w", hostID, err)
	}
	schedules := make([]domain.ExecutionHostSchedule, 0, len(listed))
	for _, entry := range listed {
		if entry.ID == "" {
			return nil, fmt.Errorf("paseo schedule list omitted a schedule id")
		}
		schedules = append(schedules, domain.ExecutionHostSchedule{
			HostID: hostID, ID: entry.ID, Name: entry.Name, Cadence: entry.Cadence,
			Target: entry.Target, Status: entry.Status,
			NextRunAt: timeOrZero(entry.NextRunAt), LastRunAt: timeOrZero(entry.LastRunAt),
		})
	}
	return schedules, nil
}

// DeleteSchedule deletes one schedule on one host, confirming the daemon
// reported the deletion for the id that was asked.
func (b *Backend) DeleteSchedule(ctx context.Context, hostID domain.ExecutionHostID, scheduleID string) error {
	if _, err := b.registeredHost(ctx, hostID); err != nil {
		return err
	}
	result, err := b.client.DeleteSchedule(ctx, scheduleID)
	if err != nil {
		return fmt.Errorf("delete schedule %s on execution host %s: %w", scheduleID, hostID, err)
	}
	if result.ID != scheduleID {
		return fmt.Errorf("paseo deleted schedule %q, not the requested %s", result.ID, scheduleID)
	}
	return nil
}

func timeOrZero(value *time.Time) time.Time {
	if value == nil {
		return time.Time{}
	}
	return *value
}
