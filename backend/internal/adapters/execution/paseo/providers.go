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
// Mode IDs come from inspecting one existing agent per available provider:
// 0.2.5's provider list carries only display labels, while `inspect` returns
// the (id, label) vocabulary — so the map is re-derived from the live daemon
// on every discovery instead of hardcoded where it would drift. A provider
// with no agent yet reports no mode ids beyond its default.
//
// Each fetch is one CLI invocation (~0.9s, spike FINDINGS S10) — up to two
// per available provider here — which is why callers cache this result
// briefly instead of asking per keystroke.
func (b *Backend) Providers(ctx context.Context, hostID domain.ExecutionHostID) ([]domain.ExecutionHostProvider, error) {
	if _, err := b.registeredHost(ctx, hostID); err != nil {
		return nil, err
	}
	listed, err := b.client.ListProviders(ctx)
	if err != nil {
		return nil, fmt.Errorf("list providers on execution host %s: %w", hostID, err)
	}
	modeAgents := b.modeAgentsByProvider(ctx)
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
			if agentID, ok := modeAgents[entry.Provider]; ok {
				provider.Modes = b.inspectModes(ctx, agentID)
			}
		}
		providers = append(providers, provider)
	}
	return providers, nil
}

// modeAgentsByProvider picks one agent id per provider from the host's agent
// list (archived included — an archived agent's mode vocabulary is just as
// current). A missing or failed list degrades to no mode ids, never an error:
// modes are an enrichment, not the discovery itself.
func (b *Backend) modeAgentsByProvider(ctx context.Context) map[string]string {
	agents, err := b.client.ListAgents(ctx, "")
	if err != nil {
		return nil
	}
	byProvider := map[string]string{}
	for _, agent := range agents {
		if agent.ID == "" || agent.Provider == "" {
			continue
		}
		name, _, _ := strings.Cut(agent.Provider, "/")
		if _, seen := byProvider[name]; !seen {
			byProvider[name] = agent.ID
		}
	}
	return byProvider
}

func (b *Backend) inspectModes(ctx context.Context, agentID string) []domain.ExecutionProviderMode {
	detail, err := b.client.Inspect(ctx, agentID)
	if err != nil {
		return nil
	}
	modes := make([]domain.ExecutionProviderMode, 0, len(detail.AvailableModes))
	for _, mode := range detail.AvailableModes {
		if mode.ID == "" {
			continue
		}
		modes = append(modes, domain.ExecutionProviderMode{ID: mode.ID, Label: mode.Label})
	}
	return modes
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
