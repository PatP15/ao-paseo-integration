package dispatch

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

type routingStoreFake struct {
	hosts    []domain.ExecutionHost
	bindings []domain.ProjectHostBinding
	caps     map[domain.ExecutionHostID][]string
	active   map[domain.ExecutionHostID]int
}

func (f *routingStoreFake) ListExecutionHosts(context.Context) ([]domain.ExecutionHost, error) {
	return append([]domain.ExecutionHost(nil), f.hosts...), nil
}

func (f *routingStoreFake) GetExecutionHost(_ context.Context, id domain.ExecutionHostID) (domain.ExecutionHost, []string, bool, error) {
	for _, host := range f.hosts {
		if host.ID == id {
			return host, append([]string(nil), f.caps[id]...), true, nil
		}
	}
	return domain.ExecutionHost{}, nil, false, nil
}

func (f *routingStoreFake) ListProjectHostBindings(context.Context, domain.ProjectID) ([]domain.ProjectHostBinding, error) {
	return append([]domain.ProjectHostBinding(nil), f.bindings...), nil
}

func (f *routingStoreFake) ListActiveSessionExecutionBindingsByHost(_ context.Context, id domain.ExecutionHostID) ([]domain.SessionExecutionBinding, error) {
	return make([]domain.SessionExecutionBinding, f.active[id]), nil
}

func TestRouterFiltersEveryEligibilityBoundary(t *testing.T) {
	now := time.Date(2026, time.August, 7, 1, 0, 0, 0, time.UTC)
	store := &routingStoreFake{caps: map[domain.ExecutionHostID][]string{}, active: map[domain.ExecutionHostID]int{}}
	add := func(id string, mutate func(*domain.ExecutionHost, *domain.ProjectHostBinding)) {
		host := domain.ExecutionHost{
			ID: domain.ExecutionHostID(id), BackendType: domain.ExecutionBackendPaseo,
			TrustZone: domain.ExecutionTrustZoneWork, Enabled: true, MaxConcurrentSessions: 2,
			LastSuccessfulProbeAt: now,
		}
		binding := domain.ProjectHostBinding{
			ProjectID: "project", HostID: host.ID, HostRepoPath: "/repos/project", Enabled: true,
		}
		mutate(&host, &binding)
		store.hosts = append(store.hosts, host)
		store.bindings = append(store.bindings, binding)
		if _, ok := store.caps[host.ID]; !ok {
			store.caps[host.ID] = []string{"linux", "docker"}
		}
	}
	add("disabled-host", func(host *domain.ExecutionHost, _ *domain.ProjectHostBinding) { host.Enabled = false })
	add("not-allowed", func(_ *domain.ExecutionHost, binding *domain.ProjectHostBinding) { binding.Enabled = false })
	add("missing-path", func(_ *domain.ExecutionHost, binding *domain.ProjectHostBinding) { binding.HostRepoPath = "" })
	add("wrong-zone", func(host *domain.ExecutionHost, _ *domain.ProjectHostBinding) {
		host.TrustZone = domain.ExecutionTrustZoneHobby
	})
	add("offline", func(host *domain.ExecutionHost, _ *domain.ProjectHostBinding) {
		host.LastFailedProbeAt = now.Add(time.Minute)
	})
	add("missing-capability", func(host *domain.ExecutionHost, _ *domain.ProjectHostBinding) {
		store.caps[host.ID] = []string{"linux"}
	})
	add("full", func(host *domain.ExecutionHost, _ *domain.ProjectHostBinding) { store.active[host.ID] = 2 })
	add("eligible", func(*domain.ExecutionHost, *domain.ProjectHostBinding) {})

	selection, err := NewRouter(store).Select(context.Background(), RouteRequest{
		ProjectID: "project", TrustZone: domain.ExecutionTrustZoneWork,
		RequiredCapabilities: []string{"linux", "docker"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if selection.Host.ID != "eligible" {
		t.Fatalf("selected host = %s, want eligible", selection.Host.ID)
	}
}

func TestRouterRanksPriorityLoadSpecificityThenStableID(t *testing.T) {
	now := time.Date(2026, time.August, 7, 1, 0, 0, 0, time.UTC)
	makeStore := func() *routingStoreFake {
		store := &routingStoreFake{caps: map[domain.ExecutionHostID][]string{}, active: map[domain.ExecutionHostID]int{}}
		for _, id := range []domain.ExecutionHostID{"z", "a", "specific", "loaded", "preferred"} {
			store.hosts = append(store.hosts, domain.ExecutionHost{
				ID: id, BackendType: domain.ExecutionBackendPaseo, TrustZone: domain.ExecutionTrustZoneMixed,
				Enabled: true, MaxConcurrentSessions: 4, LastSuccessfulProbeAt: now,
			})
			store.bindings = append(store.bindings, domain.ProjectHostBinding{
				ProjectID: "project", HostID: id, HostRepoPath: "/repo", Enabled: true, Priority: 10,
			})
			store.caps[id] = []string{"linux", "docker", "cuda"}
		}
		return store
	}
	selectID := func(store *routingStoreFake) domain.ExecutionHostID {
		got, err := NewRouter(store).Select(context.Background(), RouteRequest{
			ProjectID: "project", TrustZone: domain.ExecutionTrustZoneWork, RequiredCapabilities: []string{"linux"},
		})
		if err != nil {
			t.Fatal(err)
		}
		return got.Host.ID
	}

	store := makeStore()
	for i := range store.bindings {
		if store.bindings[i].HostID == "preferred" {
			store.bindings[i].Priority = 1
		}
	}
	if got := selectID(store); got != "preferred" {
		t.Fatalf("priority winner = %s", got)
	}

	store = makeStore()
	store.active["a"], store.active["z"], store.active["specific"] = 2, 1, 0
	store.active["loaded"], store.active["preferred"] = 3, 3
	if got := selectID(store); got != "specific" {
		t.Fatalf("load winner = %s", got)
	}

	store = makeStore()
	store.caps["specific"] = []string{"linux"}
	if got := selectID(store); got != "specific" {
		t.Fatalf("specificity winner = %s", got)
	}

	store = makeStore()
	if got := selectID(store); got != "a" {
		t.Fatalf("stable-id winner = %s", got)
	}
	if got := normalizedCapabilities([]string{"docker", "linux", "docker", " "}); !reflect.DeepEqual(got, []string{"docker", "linux"}) {
		t.Fatalf("normalized capabilities = %v", got)
	}
}
