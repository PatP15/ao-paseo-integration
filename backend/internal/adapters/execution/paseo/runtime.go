package paseo

import (
	"context"
	"fmt"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/runtimehandle"
)

// Stop requests termination only after verifying the registered host still
// resolves to the expected non-desktop Paseo daemon.
func (b *Backend) Stop(ctx context.Context, handle ports.RuntimeHandle) error {
	hostID, agentID, err := paseoHandle(handle)
	if err != nil {
		return err
	}
	if _, err := b.guardHost(ctx, hostID, ""); err != nil {
		return err
	}
	return b.client.Stop(ctx, string(agentID))
}

// Alive reports agent liveness. Every failed host or inspect probe returns an
// error; in particular, an unreachable host is never collapsed to false,nil.
func (b *Backend) Alive(ctx context.Context, handle ports.RuntimeHandle) (bool, error) {
	hostID, agentID, err := paseoHandle(handle)
	if err != nil {
		return false, err
	}
	if _, err := b.guardHost(ctx, hostID, ""); err != nil {
		return false, err
	}
	detail, err := b.client.Inspect(ctx, string(agentID))
	if err != nil {
		return false, fmt.Errorf("inspect Paseo agent liveness: %w", err)
	}
	status, err := mapStatus(detail.Status)
	if err != nil {
		return false, err
	}
	if detail.Archived || detail.ArchivedAt != nil {
		return false, nil
	}
	switch status {
	case domain.ExecutionAgentInitializing, domain.ExecutionAgentIdle, domain.ExecutionAgentRunning:
		return true, nil
	case domain.ExecutionAgentError, domain.ExecutionAgentClosed:
		return false, nil
	default:
		return false, fmt.Errorf("unsupported Paseo agent status %q", status)
	}
}

// Output returns a bounded tail of Paseo's rendered transcript.
func (b *Backend) Output(ctx context.Context, handle ports.RuntimeHandle, lines int) (string, error) {
	if lines <= 0 {
		return "", fmt.Errorf("lines must be greater than zero")
	}
	hostID, agentID, err := paseoHandle(handle)
	if err != nil {
		return "", err
	}
	if _, err := b.guardHost(ctx, hostID, ""); err != nil {
		return "", err
	}
	output, err := b.client.Logs(ctx, string(agentID))
	if err != nil {
		return "", err
	}
	return tailOutput(output, lines), nil
}

// SendMessage delivers the caller's already-validated message to Paseo.
func (b *Backend) SendMessage(ctx context.Context, handle ports.RuntimeHandle, message string) error {
	hostID, agentID, err := paseoHandle(handle)
	if err != nil {
		return err
	}
	if _, err := b.guardHost(ctx, hostID, ""); err != nil {
		return err
	}
	return b.client.Send(ctx, string(agentID), message)
}

func paseoHandle(handle ports.RuntimeHandle) (domain.ExecutionHostID, domain.ExecutionAgentID, error) {
	parts, namespaced, err := runtimehandle.Parse(handle)
	if err != nil {
		return "", "", err
	}
	if !namespaced || parts.Backend != domain.ExecutionBackendPaseo {
		return "", "", fmt.Errorf("invalid Paseo runtime handle %q", handle.ID)
	}
	return parts.HostID, parts.AgentID, nil
}

func tailOutput(output string, lines int) string {
	trimmed := strings.TrimRight(output, "\r\n")
	if trimmed == "" {
		return ""
	}
	rows := strings.Split(trimmed, "\n")
	if len(rows) > lines {
		rows = rows[len(rows)-lines:]
	}
	return strings.Join(rows, "\n")
}
