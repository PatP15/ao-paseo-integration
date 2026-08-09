package paseo

import (
	"context"
	"reflect"
	"testing"
	"time"
)

func TestProvidersFetchesModelsForAvailableProvidersOnly(t *testing.T) {
	client := newFakeExecutionClient(nil)
	client.providers = []Provider{
		{Provider: "claude", Label: "Claude", Status: "available", Enabled: "Enabled",
			DefaultMode: "auto", Modes: "Plan Mode, Always Ask, Bypass"},
		{Provider: "copilot", Label: "Copilot", Status: "unavailable", Enabled: "Enabled"},
	}
	client.providerModels = map[string][]ProviderModel{
		"claude": {{
			Model: "Opus 5", ID: "claude-opus-5", Description: "Latest release",
			ThinkingOptionIDs: []string{"off", "low", "high"}, DefaultThinkingOptionID: "low",
		}},
	}
	backend := newBackend(client, newMemoryExecutionStore(nil), func() time.Time { return backendTestNow })

	providers, err := backend.Providers(context.Background(), "host-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(providers) != 2 {
		t.Fatalf("providers = %#v", providers)
	}
	claude := providers[0]
	if claude.Provider != "claude" || !claude.Enabled || claude.DefaultMode != "auto" {
		t.Fatalf("claude = %#v", claude)
	}
	if !reflect.DeepEqual(claude.ModeLabels, []string{"Plan Mode", "Always Ask", "Bypass"}) {
		t.Fatalf("mode labels = %#v", claude.ModeLabels)
	}
	if len(claude.Models) != 1 || claude.Models[0].ID != "claude-opus-5" ||
		!reflect.DeepEqual(claude.Models[0].ThinkingOptionIDs, []string{"off", "low", "high"}) {
		t.Fatalf("claude models = %#v", claude.Models)
	}
	if len(providers[1].Models) != 0 {
		t.Fatalf("unavailable provider has models: %#v", providers[1].Models)
	}
	// One models fetch, for the available provider only: each is a ~0.9s CLI
	// invocation, so an unavailable provider must not cost one.
	for _, call := range client.calls {
		if call == "provider-models:copilot" {
			t.Fatal("models were fetched for an unavailable provider")
		}
	}
}

func TestProvidersRefusesAnUnregisteredHost(t *testing.T) {
	client := newFakeExecutionClient(nil)
	backend := newBackend(client, newMemoryExecutionStore(nil), func() time.Time { return backendTestNow })
	if _, err := backend.Providers(context.Background(), "ghost"); err == nil {
		t.Fatal("unregistered host was accepted")
	}
	if len(client.calls) != 0 {
		t.Fatalf("client was called for an unregistered host: %v", client.calls)
	}
}

func TestRunArgsForwardsThinkingOptionID(t *testing.T) {
	args, err := runArgs("worker:6767", RunRequest{
		WorkspaceID: "wks-1", Provider: "claude", Model: "claude-opus-5",
		Thinking: "high", Prompt: "do the task",
	})
	if err != nil {
		t.Fatal(err)
	}
	for i, arg := range args {
		if arg == "--thinking" {
			if args[i+1] != "high" {
				t.Fatalf("--thinking value = %q", args[i+1])
			}
			return
		}
	}
	t.Fatalf("--thinking missing from %v", args)
}

func TestProviderModelsArgsRejectsFlagShapedProviders(t *testing.T) {
	for _, provider := range []string{"", "-claude", "clau de"} {
		if _, err := providerModelsArgs("worker:6767", provider); err == nil {
			t.Fatalf("provider %q was accepted", provider)
		}
	}
}
