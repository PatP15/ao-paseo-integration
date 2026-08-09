package domain

import "time"

// ExecutionHostSchedule is one recurring schedule as a host's daemon reports
// it. Point-in-time discovery, never stored.
//
// The blind spot is heartbeats: the pinned Paseo CLI has no heartbeat listing,
// so absence of schedules proves nothing about heartbeats on the host.
type ExecutionHostSchedule struct {
	HostID    ExecutionHostID
	ID        string
	Name      string
	Cadence   string
	Target    string
	Status    string
	NextRunAt time.Time
	LastRunAt time.Time
}
