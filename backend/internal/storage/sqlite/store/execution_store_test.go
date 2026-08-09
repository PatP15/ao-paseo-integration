package store_test

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestExecutionHostAndCapabilitiesRoundTrip(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	host := domain.ExecutionHost{
		ID: "worker-1", Name: "Linux worker", BackendType: domain.ExecutionBackendPaseo,
		Transport: domain.ExecutionTransportTailscale, Endpoint: "worker:6767",
		EndpointSecretRef: "keychain://worker", TrustZone: domain.ExecutionTrustZoneHobby,
		Enabled: true, MaxConcurrentSessions: 2, ServerID: "server-1", PaseoVersion: "0.2.5",
		RequiresNoMCP: true, RequiresNoRelay: true, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.UpsertExecutionHost(ctx, host, []string{"linux", "docker"}); err != nil {
		t.Fatalf("upsert execution host: %v", err)
	}
	got, capabilities, ok, err := s.GetExecutionHost(ctx, host.ID)
	if err != nil || !ok {
		t.Fatalf("get execution host: ok=%v err=%v", ok, err)
	}
	if !reflect.DeepEqual(got, host) {
		t.Fatalf("host = %#v, want %#v", got, host)
	}
	if want := []string{"docker", "linux"}; !reflect.DeepEqual(capabilities, want) {
		t.Fatalf("capabilities = %v, want %v", capabilities, want)
	}

	host.UpdatedAt = now.Add(time.Minute)
	if err := s.UpsertExecutionHost(ctx, host, []string{"cuda"}); err != nil {
		t.Fatalf("replace execution host capabilities: %v", err)
	}
	_, capabilities, _, err = s.GetExecutionHost(ctx, host.ID)
	if err != nil {
		t.Fatalf("get updated execution host: %v", err)
	}
	if want := []string{"cuda"}; !reflect.DeepEqual(capabilities, want) {
		t.Fatalf("replaced capabilities = %v, want %v", capabilities, want)
	}
}

func TestWorkItemRoundTrip(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "ao")
	now := time.Now().UTC().Truncate(time.Second)
	item := domain.WorkItem{
		ID: "work-1", ProjectID: "ao", Title: "Store execution graph", Body: "Persist durable facts.",
		AcceptanceCriteria: []string{"fresh DB", "future migration"}, AllowedScope: []string{"backend"},
		ExcludedScope: []string{"frontend"}, RiskLevel: "normal", ApprovalState: domain.WorkItemApproved,
		LifecycleFact: domain.WorkItemOpen, Priority: 10, CreatedByType: "human", ApprovedBy: "operator",
		ApprovedAt: now, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.UpsertWorkItem(ctx, item); err != nil {
		t.Fatalf("upsert work item: %v", err)
	}
	got, ok, err := s.GetWorkItem(ctx, item.ID)
	if err != nil || !ok {
		t.Fatalf("get work item: ok=%v err=%v", ok, err)
	}
	if !reflect.DeepEqual(got, item) {
		t.Fatalf("work item = %#v, want %#v", got, item)
	}
}
