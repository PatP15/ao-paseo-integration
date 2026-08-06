package reaper

import (
	"context"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// executionBindingSource is optional so the reaper retains its existing local
// runtime contract and its small test doubles do not need execution storage.
// The SQLite store implements this interface once the fork-owned execution
// tables are present.
type executionBindingSource interface {
	GetSessionExecutionBinding(context.Context, domain.SessionID) (domain.SessionExecutionBinding, bool, error)
}

// externalDeadProbeIsAmbiguous reports whether a successful false liveness
// result is unsafe to treat as death. Remote CLI-backed runtimes cannot prove
// that an empty result means the agent died: an unreachable host, an archived
// agent, and a missing agent can collapse to the same result. Their observer
// owns the stronger lifecycle conclusion; the generic reaper records a failed
// probe and preserves session ownership.
//
// A binding lookup failure is also inconclusive. Returning true is deliberate:
// a transient SQLite read failure must not turn into a terminal session fact.
func (r *Reaper) externalDeadProbeIsAmbiguous(ctx context.Context, sessionID domain.SessionID) bool {
	source, ok := r.sessions.(executionBindingSource)
	if !ok {
		return false
	}
	binding, found, err := source.GetSessionExecutionBinding(ctx, sessionID)
	if err != nil {
		r.logger.Debug("reaper: execution binding lookup failed; dead probe is inconclusive",
			"session", sessionID, "err", err)
		return true
	}
	return found && binding.BackendType != domain.ExecutionBackendLocal
}
