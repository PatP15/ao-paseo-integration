// Package paseo observes remote execution hosts by polling and turns what it
// reads into durable AO facts.
//
// Two rules shape everything here, and both come from the fork's invariants:
//
//   - A failure to contact a host is not a fact about its sessions. Every probe
//     or inspect failure is recorded against the host and leaves session state
//     exactly as it was. Nothing in this package can terminate a session.
//   - A remote process that stopped is not a completed task. Paseo's `idle`
//     conflates finished, never started, and awaiting a prompt, so it maps to
//     AO's idle activity and never to completion or termination. Only AO
//     decides that a work item is done.
package paseo

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// SessionFacts is AO's reading of one remote agent observation.
type SessionFacts struct {
	// Activity is the state to apply to the session. It is never a terminal
	// fact: ActivityExited means the remote process is no longer running, and
	// AO keeps ownership of the session either way.
	Activity domain.ActivityState
	// EventType names the durable execution_events row for this observation.
	EventType string
}

// DeriveSessionFacts maps one inspected remote agent onto AO facts.
//
// Precedence is deliberate:
//
//  1. Archived wins over any status. An archived agent is gone; whatever status
//     the read model still reports about it is stale.
//  2. A pending permission wins over a live status. It must land as blocked,
//     not waiting-input, so that AO physically cannot answer a permission with
//     free text and has to issue an explicit decision instead.
//  3. Otherwise the remote status maps across.
//
// The bool is false for a status AO has no reading for; callers record nothing
// rather than inventing a conclusion.
func DeriveSessionFacts(detail domain.ExecutionAgentDetail) (SessionFacts, bool) {
	if detail.Archived {
		return SessionFacts{Activity: domain.ActivityExited, EventType: domain.ExecutionObservedArchived}, true
	}
	if len(detail.PendingPermissions) > 0 {
		return SessionFacts{Activity: domain.ActivityBlocked, EventType: domain.ExecutionObservedBlocked}, true
	}
	switch detail.Status {
	case domain.ExecutionAgentInitializing, domain.ExecutionAgentRunning:
		return SessionFacts{Activity: domain.ActivityActive, EventType: domain.ExecutionObservedRunning}, true
	case domain.ExecutionAgentIdle:
		// Not completion, and deliberately not ActivityExited: an idle agent may
		// have finished, may never have started, or may be waiting for a prompt.
		// AO records idleness and lets its own completion evidence decide.
		return SessionFacts{Activity: domain.ActivityIdle, EventType: domain.ExecutionObservedIdle}, true
	case domain.ExecutionAgentError:
		return SessionFacts{Activity: domain.ActivityExited, EventType: domain.ExecutionObservedFailed}, true
	case domain.ExecutionAgentClosed:
		return SessionFacts{Activity: domain.ActivityExited, EventType: domain.ExecutionObservedClosed}, true
	default:
		return SessionFacts{}, false
	}
}

// observationPayload is the content-addressed body of an execution_events row.
// It holds only facts, never a timestamp: the store hashes it to collapse an
// unchanged observation repeated every tick into one durable row, which a
// clock reading would defeat.
type observationPayload struct {
	Status               domain.ExecutionAgentStatus `json:"status"`
	Activity             domain.ActivityState        `json:"activity"`
	Archived             bool                        `json:"archived"`
	Worktree             string                      `json:"worktree,omitempty"`
	Cwd                  string                      `json:"cwd,omitempty"`
	ParentAgentID        domain.ExecutionAgentID     `json:"parentAgentId,omitempty"`
	PendingPermissionIDs []string                    `json:"pendingPermissionIds,omitempty"`
}

func encodeObservation(detail domain.ExecutionAgentDetail, facts SessionFacts) (string, error) {
	permissions := make([]string, 0, len(detail.PendingPermissions))
	for _, permission := range detail.PendingPermissions {
		permissions = append(permissions, permission.ID)
	}
	sort.Strings(permissions)
	payload, err := json.Marshal(observationPayload{
		Status: detail.Status, Activity: facts.Activity, Archived: detail.Archived,
		Worktree: detail.Worktree, Cwd: detail.Cwd, ParentAgentID: detail.ParentAgentID,
		PendingPermissionIDs: permissions,
	})
	if err != nil {
		return "", fmt.Errorf("encode execution observation: %w", err)
	}
	return string(payload), nil
}
