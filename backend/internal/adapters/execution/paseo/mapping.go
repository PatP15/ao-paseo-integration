package paseo

import (
	"fmt"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func mapAgentDetail(hostID domain.ExecutionHostID, detail AgentDetail) (domain.ExecutionAgentDetail, error) {
	status, err := mapStatus(detail.Status)
	if err != nil {
		return domain.ExecutionAgentDetail{}, err
	}
	permissions := make([]domain.ExecutionPermission, 0, len(detail.PendingPermissions))
	for _, permission := range detail.PendingPermissions {
		permissions = append(permissions, domain.ExecutionPermission{
			ID:       permission.ID,
			ToolName: permission.ToolName,
			Reason:   permission.Reason,
		})
	}
	parentID := ""
	if detail.ParentAgentID != nil {
		parentID = *detail.ParentAgentID
	}
	return domain.ExecutionAgentDetail{
		ExecutionAgentObservation: domain.ExecutionAgentObservation{
			HostID:        hostID,
			AgentID:       domain.ExecutionAgentID(detail.ID),
			ParentAgentID: domain.ExecutionAgentID(parentID),
			Status:        status,
			Worktree:      detail.Worktree,
			Cwd:           detail.Cwd,
			Archived:      detail.Archived,
			CreatedAt:     detail.CreatedAt,
		},
		PendingPermissions: permissions,
	}, nil
}

func mapStatus(status string) (domain.ExecutionAgentStatus, error) {
	switch strings.ToLower(status) {
	case "initializing":
		return domain.ExecutionAgentInitializing, nil
	case "idle":
		return domain.ExecutionAgentIdle, nil
	case "running":
		return domain.ExecutionAgentRunning, nil
	case "error":
		return domain.ExecutionAgentError, nil
	case "closed":
		return domain.ExecutionAgentClosed, nil
	default:
		return "", fmt.Errorf("unknown Paseo agent status %q", status)
	}
}
