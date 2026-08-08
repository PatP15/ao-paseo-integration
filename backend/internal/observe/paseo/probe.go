package paseo

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// ProbeStore is the subset of Store a single probe writes to.
type ProbeStore interface {
	RecordExecutionHostProbe(context.Context, domain.ExecutionHostProbe) error
	RecordExecutionOrphan(context.Context, domain.ExecutionOrphan) (bool, error)
}

// ProbeHost probes one host and records the outcome, returning the probe it
// recorded. It is the single implementation of the probe rules, shared by the
// observer's tick and the on-demand probe endpoint so the two can never
// disagree about what a probe means:
//
//   - an error or a Reachable:false status is an outage — recorded as a failed
//     probe, and never a statement about the host's sessions;
//   - a server id different from the registered one is a DIFFERENT DAEMON —
//     recorded as an identity orphan plus a failed probe, because every agent
//     id AO holds for this host now addresses something that no longer exists.
//
// The returned error is a storage failure only; a host that cannot be reached
// returns a recorded unreachable probe and a nil error.
func ProbeHost(
	ctx context.Context,
	store ProbeStore,
	remote ports.ExecutionObserver,
	host domain.ExecutionHost,
	now time.Time,
	logger *slog.Logger,
) (domain.ExecutionHostProbe, error) {
	status, err := remote.Status(ctx, host.ID)
	// A backend that reports unreachability as a flag rather than an error is
	// treated the same way. Reading (Reachable:false, nil) as anything other
	// than an outage is the same mistake as reading (false, nil) from Alive as
	// death.
	if err != nil || !status.Reachable {
		reason := "execution host reported itself unreachable"
		if err != nil {
			reason = err.Error()
		}
		logger.Debug("paseo observer: host probe failed; sessions left untouched",
			"host", host.ID, "err", reason)
		probe := domain.ExecutionHostProbe{
			HostID: host.ID, Reachable: false, Error: reason, ObservedAt: now,
		}
		return probe, store.RecordExecutionHostProbe(ctx, probe)
	}
	if host.ServerID != "" && status.ServerID != host.ServerID {
		detail := fmt.Sprintf("registered server %s, observed %s", host.ServerID, status.ServerID)
		if _, orphanErr := store.RecordExecutionOrphan(ctx, domain.ExecutionOrphan{
			Kind: domain.ExecutionOrphanServerIdentity, HostID: host.ID,
			Detail: detail, ObservedAt: now,
		}); orphanErr != nil {
			return domain.ExecutionHostProbe{}, orphanErr
		}
		probe := domain.ExecutionHostProbe{
			HostID: host.ID, Reachable: false, Error: "paseo server identity changed: " + detail,
			ObservedAt: now,
		}
		return probe, store.RecordExecutionHostProbe(ctx, probe)
	}
	probe := domain.ExecutionHostProbe{
		HostID: host.ID, ServerID: status.ServerID, Version: status.Version,
		Reachable: true, ObservedAt: now,
	}
	return probe, store.RecordExecutionHostProbe(ctx, probe)
}
