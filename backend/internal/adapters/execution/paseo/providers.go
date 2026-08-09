package paseo

import (
	"context"
	"fmt"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

var _ ports.ExecutionProviderDiscovery = (*Backend)(nil)

// Providers reports what the host's daemon can launch: every provider from
// `provider ls`, with models and thinking options fetched for the available
// ones only. Unavailable providers are still returned — "codex is configured
// but currently unavailable" is a fact an operator acts on — but their model
// list stays empty rather than costing a CLI call that would fail anyway.
//
// Each models fetch is one CLI invocation (~0.9s, spike FINDINGS S10), which
// is why callers cache this result briefly instead of asking per keystroke.
func (b *Backend) Providers(ctx context.Context, hostID domain.ExecutionHostID) ([]domain.ExecutionHostProvider, error) {
	if _, err := b.registeredHost(ctx, hostID); err != nil {
		return nil, err
	}
	listed, err := b.client.ListProviders(ctx)
	if err != nil {
		return nil, fmt.Errorf("list providers on execution host %s: %w", hostID, err)
	}
	providers := make([]domain.ExecutionHostProvider, 0, len(listed))
	for _, entry := range listed {
		if entry.Provider == "" {
			return nil, fmt.Errorf("paseo provider list omitted a provider name")
		}
		provider := domain.ExecutionHostProvider{
			Provider:    entry.Provider,
			Label:       entry.Label,
			Status:      entry.Status,
			Enabled:     strings.EqualFold(entry.Enabled, "enabled"),
			DefaultMode: entry.DefaultMode,
			ModeLabels:  splitModeLabels(entry.Modes),
		}
		if entry.Status == "available" {
			models, err := b.client.ProviderModels(ctx, entry.Provider)
			if err != nil {
				return nil, fmt.Errorf("list models for provider %s on execution host %s: %w",
					entry.Provider, hostID, err)
			}
			provider.Models = make([]domain.ExecutionProviderModel, 0, len(models))
			for _, model := range models {
				if model.ID == "" {
					return nil, fmt.Errorf("paseo provider %s reported a model without an id", entry.Provider)
				}
				provider.Models = append(provider.Models, domain.ExecutionProviderModel{
					ID:                      model.ID,
					Label:                   model.Model,
					Description:             model.Description,
					ThinkingOptionIDs:       append([]string(nil), model.ThinkingOptionIDs...),
					DefaultThinkingOptionID: model.DefaultThinkingOptionID,
				})
			}
		}
		providers = append(providers, provider)
	}
	return providers, nil
}

func splitModeLabels(modes string) []string {
	if strings.TrimSpace(modes) == "" {
		return []string{}
	}
	parts := strings.Split(modes, ",")
	labels := make([]string, 0, len(parts))
	for _, part := range parts {
		if label := strings.TrimSpace(part); label != "" {
			labels = append(labels, label)
		}
	}
	return labels
}
