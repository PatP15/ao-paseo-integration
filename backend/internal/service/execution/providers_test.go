package execution

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func discoveredProviders() []domain.ExecutionHostProvider {
	return []domain.ExecutionHostProvider{
		{
			Provider: "claude", Label: "Claude", Status: "available", Enabled: true,
			DefaultMode: "auto", ModeLabels: []string{"Plan Mode", "Bypass"},
			Models: []domain.ExecutionProviderModel{{
				ID: "claude-opus-5", Label: "Opus 5",
				ThinkingOptionIDs:       []string{"off", "low", "high"},
				DefaultThinkingOptionID: "low",
			}},
		},
		{Provider: "copilot", Label: "Copilot", Status: "unavailable", Enabled: true},
	}
}

func TestHostProvidersRequiresARegisteredHostAndWiring(t *testing.T) {
	store := newFakeStore()
	svc := newTestService(store)
	if _, err := svc.HostProviders(context.Background(), "ghost"); errCode(t, err) != "HOST_NOT_FOUND" {
		t.Fatalf("unknown host error = %v", err)
	}

	if _, err := svc.RegisterHost(context.Background(), validHostInput()); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.HostProviders(context.Background(), "worker-1"); errCode(t, err) != "PROVIDER_DISCOVERY_UNAVAILABLE" {
		t.Fatalf("unwired discovery error = %v", err)
	}
}

func TestHostProvidersServesFromABriefCache(t *testing.T) {
	store := newFakeStore()
	current := testNow
	svc := newService(store, func() time.Time { return current }, func() string { return "id-1" })
	if _, err := svc.RegisterHost(context.Background(), validHostInput()); err != nil {
		t.Fatal(err)
	}
	calls := 0
	svc.SetProviderDiscovery(func(_ context.Context, host domain.ExecutionHost) ([]domain.ExecutionHostProvider, error) {
		calls++
		if host.ID != "worker-1" {
			t.Fatalf("discovery host = %s", host.ID)
		}
		return discoveredProviders(), nil
	})

	for i := 0; i < 3; i++ {
		providers, err := svc.HostProviders(context.Background(), "worker-1")
		if err != nil || len(providers) != 2 {
			t.Fatalf("HostProviders = (%v, %v)", providers, err)
		}
	}
	if calls != 1 {
		t.Fatalf("discovery calls within TTL = %d, want 1", calls)
	}
	current = current.Add(providerCacheTTL + time.Second)
	if _, err := svc.HostProviders(context.Background(), "worker-1"); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("discovery calls after TTL = %d, want 2", calls)
	}
}

func TestHostProvidersPassesDiscoveryErrorsThroughTyped(t *testing.T) {
	store := newFakeStore()
	svc := newTestService(store)
	if _, err := svc.RegisterHost(context.Background(), validHostInput()); err != nil {
		t.Fatal(err)
	}
	unreachable := errors.New("host did not answer")
	svc.SetProviderDiscovery(func(context.Context, domain.ExecutionHost) ([]domain.ExecutionHostProvider, error) {
		return nil, unreachable
	})
	if _, err := svc.HostProviders(context.Background(), "worker-1"); !errors.Is(err, unreachable) {
		t.Fatalf("discovery error = %v, want passed through", err)
	}
}

func TestListSessionEventsChecksSessionAndClampsLimit(t *testing.T) {
	store := newFakeStore()
	store.sessions = map[domain.SessionID]domain.SessionRecord{"project-1": {ID: "project-1"}}
	store.events = map[domain.SessionID][]domain.ExecutionEventRecord{
		"project-1": {{ID: "evt-1", SessionID: "project-1", EventType: "checkpoint"}},
	}
	svc := newTestService(store)
	ctx := context.Background()

	if _, err := svc.ListSessionEvents(ctx, EventsFilter{SessionID: "ghost"}); errCode(t, err) != "SESSION_NOT_FOUND" {
		t.Fatalf("unknown session error = %v", err)
	}
	events, err := svc.ListSessionEvents(ctx, EventsFilter{SessionID: "project-1", AfterID: " evt-0 ", Limit: 5000})
	if err != nil || len(events) != 1 {
		t.Fatalf("ListSessionEvents = (%v, %v)", events, err)
	}
	if store.eventsSeenAfter != "evt-0" || store.eventsSeenLimit != MaxEventLimit {
		t.Fatalf("store saw after=%q limit=%d", store.eventsSeenAfter, store.eventsSeenLimit)
	}
	if _, err := svc.ListSessionEvents(ctx, EventsFilter{SessionID: "project-1"}); err != nil {
		t.Fatal(err)
	}
	if store.eventsSeenLimit != DefaultEventLimit {
		t.Fatalf("default limit = %d", store.eventsSeenLimit)
	}

	store.eventsErr = domain.ErrExecutionEventCursorUnknown
	if _, err := svc.ListSessionEvents(ctx, EventsFilter{SessionID: "project-1", AfterID: "stale"}); errCode(t, err) != "EVENT_CURSOR_UNKNOWN" {
		t.Fatalf("stale cursor error = %v", err)
	}
}

func TestHostSchedulesFlagEveryRowAsAPolicyViolation(t *testing.T) {
	store := newFakeStore()
	svc := newTestService(store)
	ctx := context.Background()

	if _, err := svc.HostSchedules(ctx, "ghost"); errCode(t, err) != "HOST_NOT_FOUND" {
		t.Fatalf("unknown host error = %v", err)
	}
	if _, err := svc.RegisterHost(ctx, validHostInput()); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.HostSchedules(ctx, "worker-1"); errCode(t, err) != "SCHEDULE_CHANNEL_UNAVAILABLE" {
		t.Fatalf("unwired channel error = %v", err)
	}

	var deleted string
	svc.SetScheduleChannel(
		func(_ context.Context, host domain.ExecutionHost) ([]domain.ExecutionHostSchedule, error) {
			return []domain.ExecutionHostSchedule{
				{HostID: host.ID, ID: "sch-1", Cadence: "cron:0 3 * * *", Status: "active"},
			}, nil
		},
		func(_ context.Context, _ domain.ExecutionHost, scheduleID string) error {
			deleted = scheduleID
			return nil
		},
	)
	schedules, err := svc.HostSchedules(ctx, "worker-1")
	if err != nil || len(schedules) != 1 {
		t.Fatalf("HostSchedules = (%#v, %v)", schedules, err)
	}
	// D6: AO owns scheduling and offers no create, so anything present was
	// made outside AO.
	if !schedules[0].PolicyViolation {
		t.Fatalf("schedule not flagged: %#v", schedules[0])
	}

	if err := svc.DeleteHostSchedule(ctx, "worker-1", " sch-1 "); err != nil || deleted != "sch-1" {
		t.Fatalf("delete = %v, deleted = %q", err, deleted)
	}
	if err := svc.DeleteHostSchedule(ctx, "worker-1", " "); errCode(t, err) != "SCHEDULE_ID_REQUIRED" {
		t.Fatalf("empty schedule id error = %v", err)
	}
}

type fakeMaintenance struct {
	skills    []domain.ExecutionHostSkill
	prefs     domain.ExecutionHostPrefs
	written   []byte
	expectSHA string
	err       error
}

func (f *fakeMaintenance) Inventory(context.Context, domain.ExecutionHost) ([]domain.ExecutionHostSkill, error) {
	return f.skills, f.err
}

func (f *fakeMaintenance) ReadPrefs(context.Context, domain.ExecutionHost) (domain.ExecutionHostPrefs, error) {
	return f.prefs, f.err
}

func (f *fakeMaintenance) WritePrefs(_ context.Context, _ domain.ExecutionHost, content []byte, expectSHA256 string) (domain.ExecutionHostPrefs, error) {
	f.written, f.expectSHA = content, expectSHA256
	if f.err != nil {
		return domain.ExecutionHostPrefs{}, f.err
	}
	return domain.ExecutionHostPrefs{HostID: f.prefs.HostID, Content: string(content), SHA256: "confirmed", Exists: true}, nil
}

func TestInventoryCachesAndRefreshRunsTheChannel(t *testing.T) {
	store := newFakeStore()
	svc := newTestService(store)
	ctx := context.Background()
	if _, err := svc.RegisterHost(ctx, validHostInput()); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Inventory(ctx, "worker-1", false); errCode(t, err) != "MAINTENANCE_CHANNEL_UNAVAILABLE" {
		t.Fatalf("unwired channel error = %v", err)
	}
	channel := &fakeMaintenance{
		skills: []domain.ExecutionHostSkill{{HostID: "worker-1", Name: "deploy", Description: "Deploy"}},
		prefs:  domain.ExecutionHostPrefs{HostID: "worker-1", Content: `{"providers":{}}`, SHA256: "live-hash", Exists: true},
	}
	svc.SetMaintenanceChannel(channel)

	// Cached read on an empty cache: no skills, no prefs, no live call implied.
	empty, err := svc.Inventory(ctx, "worker-1", false)
	if err != nil || len(empty.Skills) != 0 || empty.Prefs != nil || empty.FromLiveProbe {
		t.Fatalf("cached-empty inventory = (%+v, %v)", empty, err)
	}

	refreshed, err := svc.Inventory(ctx, "worker-1", true)
	if err != nil || len(refreshed.Skills) != 1 || refreshed.Prefs == nil || !refreshed.FromLiveProbe {
		t.Fatalf("refreshed inventory = (%+v, %v)", refreshed, err)
	}
	if refreshed.Prefs.SHA256 != "live-hash" || !refreshed.SkillsAsOf.Equal(testNow) {
		t.Fatalf("refreshed facts = %+v", refreshed)
	}

	// The refresh persisted: a later cached read answers without the channel.
	svc.SetMaintenanceChannel(&fakeMaintenance{err: errors.New("must not be called")})
	cached, err := svc.Inventory(ctx, "worker-1", false)
	if err != nil || len(cached.Skills) != 1 || cached.Prefs == nil || cached.FromLiveProbe {
		t.Fatalf("cached inventory = (%+v, %v)", cached, err)
	}
}

func TestPutPreferencesValidatesAndPersistsTheConfirmRead(t *testing.T) {
	store := newFakeStore()
	svc := newTestService(store)
	ctx := context.Background()
	if _, err := svc.RegisterHost(ctx, validHostInput()); err != nil {
		t.Fatal(err)
	}
	channel := &fakeMaintenance{prefs: domain.ExecutionHostPrefs{HostID: "worker-1"}}
	svc.SetMaintenanceChannel(channel)

	if _, err := svc.PutPreferences(ctx, "worker-1", " ", "abc"); errCode(t, err) != "PREFS_CONTENT_REQUIRED" {
		t.Fatalf("empty content error = %v", err)
	}
	if _, err := svc.PutPreferences(ctx, "worker-1", "not json", "abc"); errCode(t, err) != "PREFS_CONTENT_INVALID" {
		t.Fatalf("invalid json error = %v", err)
	}
	if _, err := svc.PutPreferences(ctx, "worker-1", `{"providers":{}}`, ""); errCode(t, err) != "PREFS_BASE_HASH_REQUIRED" {
		t.Fatalf("missing base hash error = %v", err)
	}

	prefs, err := svc.PutPreferences(ctx, "worker-1", `{"providers":{}}`, "BASE1234")
	if err != nil || prefs.SHA256 != "confirmed" {
		t.Fatalf("put = (%+v, %v)", prefs, err)
	}
	if string(channel.written) != `{"providers":{}}` || channel.expectSHA != "base1234" {
		t.Fatalf("channel saw content=%q expect=%q", channel.written, channel.expectSHA)
	}
	stored, found, err := store.GetExecutionHostPrefs(ctx, "worker-1")
	if err != nil || !found || stored.SHA256 != "confirmed" || !stored.ConfirmedAt.Equal(testNow) {
		t.Fatalf("persisted prefs = (%+v, %v, %v)", stored, found, err)
	}
}

func TestValidateDispatchSettingsRefusesUndiscoveredIDs(t *testing.T) {
	store := newFakeStore()
	svc := newTestService(store)
	if _, err := svc.RegisterHost(context.Background(), validHostInput()); err != nil {
		t.Fatal(err)
	}
	svc.SetProviderDiscovery(func(context.Context, domain.ExecutionHost) ([]domain.ExecutionHostProvider, error) {
		return discoveredProviders(), nil
	})
	ctx := context.Background()

	if err := svc.ValidateDispatchSettings(ctx, "worker-1", "claude", "claude-opus-5", "high"); err != nil {
		t.Fatalf("discovered id refused: %v", err)
	}
	if err := svc.ValidateDispatchSettings(ctx, "worker-1", "gemini", "claude-opus-5", "high"); errCode(t, err) != "PROVIDER_UNKNOWN" {
		t.Fatalf("unknown provider error = %v", err)
	}
	if err := svc.ValidateDispatchSettings(ctx, "worker-1", "claude", "", "high"); errCode(t, err) != "MODEL_REQUIRED_FOR_SETTINGS" {
		t.Fatalf("missing model error = %v", err)
	}
	if err := svc.ValidateDispatchSettings(ctx, "worker-1", "claude", "claude-nonexistent", "high"); errCode(t, err) != "MODEL_UNKNOWN" {
		t.Fatalf("unknown model error = %v", err)
	}
	err := svc.ValidateDispatchSettings(ctx, "worker-1", "claude", "claude-opus-5", "ultrathink")
	if errCode(t, err) != "THINKING_OPTION_UNKNOWN" {
		t.Fatalf("unknown thinking option error = %v", err)
	}
}
