package paseo

import (
	"fmt"

	"context"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

var _ ports.ExecutionObserver = (*Backend)(nil)

// Status probes one registered host's daemon identity.
//
// Unlike the mutating paths this does not run the full host guard: the guard
// refuses a desktop-managed daemon, and a refusal is exactly the fact an
// observer needs to report. It also costs one extra CLI invocation, and at
// ~0.9 s per invocation (spike FINDINGS S10) a per-call guard would consume the
// whole polling budget. Callers poll Status once per host per tick and compare
// ServerID themselves before trusting any agent id.
//
// MCPEnabled and RelayEnabled stay false: `paseo status --json` in 0.2.5 does
// not report them, so they are enforced at host registration
// (ExecutionHost.RequiresNoMCP / RequiresNoRelay) rather than guessed here.
func (b *Backend) Status(ctx context.Context, hostID domain.ExecutionHostID) (domain.ExecutionHostStatus, error) {
	if _, err := b.registeredHost(ctx, hostID); err != nil {
		return domain.ExecutionHostStatus{}, err
	}
	status, err := b.client.Status(ctx)
	if err != nil {
		return domain.ExecutionHostStatus{}, fmt.Errorf("probe execution host %s: %w", hostID, err)
	}
	if err := validateDaemonStatus(status); err != nil {
		return domain.ExecutionHostStatus{}, err
	}
	return domain.ExecutionHostStatus{
		HostID: hostID, Reachable: true, DesktopManaged: *status.DesktopManaged,
		ServerID: status.ServerID, Version: status.Version, ObservedAt: b.now().UTC(),
	}, nil
}

// ListOwned returns every agent the host knows about, archived included.
//
// The label filter is deliberately omitted: a query narrowed to one AO label
// cannot surface an agent AO has lost track of, which is the only reason to
// call this. Paseo's list shape carries no worktree, archive flag, or machine
// timestamp — `created` is a humanized string such as "just now" — so those
// fields stay zero and Inspect remains the only reconciliation-grade source.
func (b *Backend) ListOwned(ctx context.Context, hostID domain.ExecutionHostID) ([]domain.ExecutionAgentObservation, error) {
	if _, err := b.registeredHost(ctx, hostID); err != nil {
		return nil, err
	}
	agents, err := b.client.ListAgents(ctx, "")
	if err != nil {
		return nil, fmt.Errorf("list agents on execution host %s: %w", hostID, err)
	}
	observations := make([]domain.ExecutionAgentObservation, 0, len(agents))
	for _, agent := range agents {
		if agent.ID == "" {
			return nil, fmt.Errorf("paseo agent list omitted an agent id")
		}
		status, err := mapStatus(agent.Status)
		if err != nil {
			return nil, err
		}
		observations = append(observations, domain.ExecutionAgentObservation{
			HostID: hostID, AgentID: domain.ExecutionAgentID(agent.ID), Status: status, Cwd: agent.Cwd,
		})
	}
	return observations, nil
}

// Inspect returns the strict fact set for one agent. It performs no host status
// probe for the same budget reason as Status; the caller has already
// established the host identity for this tick.
func (b *Backend) Inspect(ctx context.Context, hostID domain.ExecutionHostID, agentID domain.ExecutionAgentID) (domain.ExecutionAgentDetail, error) {
	if _, err := b.registeredHost(ctx, hostID); err != nil {
		return domain.ExecutionAgentDetail{}, err
	}
	if agentID == "" {
		return domain.ExecutionAgentDetail{}, fmt.Errorf("inspect requires an agent id")
	}
	detail, err := b.client.Inspect(ctx, string(agentID))
	if err != nil {
		return domain.ExecutionAgentDetail{}, fmt.Errorf("inspect Paseo agent %s: %w", agentID, err)
	}
	if detail.ID != string(agentID) {
		return domain.ExecutionAgentDetail{}, fmt.Errorf("paseo inspect returned agent %q, not %s", detail.ID, agentID)
	}
	return mapAgentDetail(hostID, detail)
}
