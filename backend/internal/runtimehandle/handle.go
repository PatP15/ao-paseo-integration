// Package runtimehandle owns the wire format for externally managed runtime
// handles. Local runtime IDs remain opaque and unprefixed.
package runtimehandle

import (
	"fmt"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// Parts identifies the execution backend, host, and agent encoded in a remote
// runtime handle.
type Parts struct {
	Backend domain.ExecutionBackendType
	HostID  domain.ExecutionHostID
	AgentID domain.ExecutionAgentID
}

// New constructs <backend>:<host>/<agent>. The delimiters are reserved so a
// handle always has exactly one interpretation.
func New(backend domain.ExecutionBackendType, hostID domain.ExecutionHostID, agentID domain.ExecutionAgentID) (ports.RuntimeHandle, error) {
	if backend == "" || hostID == "" || agentID == "" ||
		strings.ContainsAny(string(backend), ":/") ||
		strings.ContainsAny(string(hostID), ":/") ||
		strings.ContainsAny(string(agentID), ":/") {
		return ports.RuntimeHandle{}, fmt.Errorf("invalid namespaced runtime handle parts")
	}
	return ports.RuntimeHandle{ID: string(backend) + ":" + string(hostID) + "/" + string(agentID)}, nil
}

// Parse decodes a namespaced handle. found is false for an unprefixed local
// handle. A prefixed but malformed handle returns found=true and an error so it
// can never fall through to a local runtime by accident.
func Parse(handle ports.RuntimeHandle) (parts Parts, found bool, err error) {
	backend, remainder, found := strings.Cut(handle.ID, ":")
	if !found {
		return Parts{}, false, nil
	}
	if backend == "" || strings.ContainsAny(backend, "/") || strings.Contains(remainder, ":") || strings.Count(remainder, "/") != 1 {
		return Parts{}, true, fmt.Errorf("invalid namespaced runtime handle %q", handle.ID)
	}
	hostID, agentID, _ := strings.Cut(remainder, "/")
	if hostID == "" || agentID == "" {
		return Parts{}, true, fmt.Errorf("invalid namespaced runtime handle %q", handle.ID)
	}
	return Parts{
		Backend: domain.ExecutionBackendType(backend),
		HostID:  domain.ExecutionHostID(hostID),
		AgentID: domain.ExecutionAgentID(agentID),
	}, true, nil
}

// IsNamespaced reports whether an ID has a backend prefix. It deliberately
// returns true for malformed prefixed IDs: API clients must not try to attach
// a local terminal to a handle intended for an external backend.
func IsNamespaced(id string) bool {
	return strings.Contains(id, ":")
}
