package paseo

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/execution/fake"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

var observerNow = time.Date(2026, time.February, 3, 9, 0, 0, 0, time.UTC)

type memoryStore struct {
	hosts    []domain.ExecutionHost
	bindings map[domain.ExecutionHostID][]domain.SessionExecutionBinding

	probes       []domain.ExecutionHostProbe
	observations []domain.ExecutionObservationEvent
	questions    []domain.ExecutionPermissionQuestion
	orphans      []domain.ExecutionOrphan
	observedAt   map[domain.SessionID]time.Time

	seenObservations map[string]struct{}
	seenQuestions    map[string]struct{}
	seenOrphans      map[string]struct{}
}

func newMemoryStore() *memoryStore {
	return &memoryStore{
		hosts: []domain.ExecutionHost{{
			ID: "host-1", BackendType: domain.ExecutionBackendPaseo, Enabled: true, ServerID: "server-1",
		}},
		bindings:         map[domain.ExecutionHostID][]domain.SessionExecutionBinding{},
		observedAt:       map[domain.SessionID]time.Time{},
		seenObservations: map[string]struct{}{},
		seenQuestions:    map[string]struct{}{},
		seenOrphans:      map[string]struct{}{},
	}
}

func (s *memoryStore) ListExecutionHosts(context.Context) ([]domain.ExecutionHost, error) {
	return append([]domain.ExecutionHost(nil), s.hosts...), nil
}

func (s *memoryStore) ListActiveSessionExecutionBindingsByHost(_ context.Context, id domain.ExecutionHostID) ([]domain.SessionExecutionBinding, error) {
	return append([]domain.SessionExecutionBinding(nil), s.bindings[id]...), nil
}

func (s *memoryStore) RecordExecutionHostProbe(_ context.Context, probe domain.ExecutionHostProbe) error {
	s.probes = append(s.probes, probe)
	return nil
}

func (s *memoryStore) RecordExecutionObservation(_ context.Context, event domain.ExecutionObservationEvent) (bool, error) {
	s.observations = append(s.observations, event)
	key := string(event.SessionID) + "\x00" + event.Type + "\x00" + event.PayloadJSON
	if _, seen := s.seenObservations[key]; seen {
		return false, nil
	}
	s.seenObservations[key] = struct{}{}
	return true, nil
}

func (s *memoryStore) OpenExecutionPermissionQuestion(_ context.Context, question domain.ExecutionPermissionQuestion) (bool, error) {
	key := string(question.SessionID) + "\x00" + question.ExternalID
	if _, seen := s.seenQuestions[key]; seen {
		return false, nil
	}
	s.seenQuestions[key] = struct{}{}
	s.questions = append(s.questions, question)
	return true, nil
}

func (s *memoryStore) RecordExecutionOrphan(_ context.Context, orphan domain.ExecutionOrphan) (bool, error) {
	key := string(orphan.Kind) + "\x00" + string(orphan.HostID) + "\x00" + string(orphan.AgentID)
	if _, seen := s.seenOrphans[key]; seen {
		return false, nil
	}
	s.seenOrphans[key] = struct{}{}
	s.orphans = append(s.orphans, orphan)
	return true, nil
}

func (s *memoryStore) MarkSessionExecutionObserved(_ context.Context, id domain.SessionID, at time.Time) error {
	s.observedAt[id] = at
	return nil
}

type recordedSignal struct {
	sessionID domain.SessionID
	signal    ports.ActivitySignal
}

type fakeLifecycle struct {
	signals []recordedSignal
	err     error
}

func (l *fakeLifecycle) ApplyActivitySignal(_ context.Context, id domain.SessionID, signal ports.ActivitySignal) error {
	l.signals = append(l.signals, recordedSignal{sessionID: id, signal: signal})
	return l.err
}

func (l *fakeLifecycle) states() []domain.ActivityState {
	states := make([]domain.ActivityState, 0, len(l.signals))
	for _, recorded := range l.signals {
		states = append(states, recorded.signal.State)
	}
	return states
}

func newRemote() *fake.Backend {
	remote := fake.New()
	remote.SetTime(observerNow)
	remote.SetHostStatus(domain.ExecutionHostStatus{
		HostID: "host-1", Reachable: true, ServerID: "server-1", Version: "0.2.5",
	})
	return remote
}

func inspectCount(remote *fake.Backend) int {
	count := 0
	for _, call := range remote.Calls() {
		if call.Operation == fake.OperationInspect {
			count++
		}
	}
	return count
}

func operations(remote *fake.Backend) []string {
	var ops []string
	for _, call := range remote.Calls() {
		op := string(call.Operation)
		if call.AgentID != "" {
			op += ":" + string(call.AgentID)
		}
		ops = append(ops, op)
	}
	return ops
}

func newTestObserver(store *memoryStore, lifecycle Lifecycle, remote ports.ExecutionObserver) *Observer {
	observer := newSweepingObserver(store, lifecycle, remote)
	for _, host := range store.hosts {
		observer.lastSweep[host.ID] = observerNow
	}
	return observer
}

func newSweepingObserver(store Store, lifecycle Lifecycle, remote ports.ExecutionObserver) *Observer {
	observer := New(store, lifecycle, ObserverResolverFunc(func(domain.ExecutionHostID) (ports.ExecutionObserver, bool) {
		return remote, true
	}), slog.New(slog.NewTextHandler(io.Discard, nil)))
	observer.now = func() time.Time { return observerNow }
	return observer
}

func binding(sessionID domain.SessionID, agentID domain.ExecutionAgentID) domain.SessionExecutionBinding {
	return domain.SessionExecutionBinding{
		SessionID: sessionID, WorkItemID: "work-1", BackendType: domain.ExecutionBackendPaseo,
		HostID: "host-1", ExternalAgentID: agentID, ExternalWorkspaceID: "wks-1",
		BoundServerID: "server-1", HostWorkspacePath: "/remote/worktree/" + string(sessionID),
		LaunchID: "launch-1", Attempt: 1,
	}
}

func detail(agentID domain.ExecutionAgentID, status domain.ExecutionAgentStatus) domain.ExecutionAgentDetail {
	return domain.ExecutionAgentDetail{
		ExecutionAgentObservation: domain.ExecutionAgentObservation{
			HostID: "host-1", AgentID: agentID, Status: status, Worktree: "session-1:1",
			Cwd: "/remote/worktree/session-1", CreatedAt: observerNow.Add(-time.Hour),
		},
	}
}

func TestDeriveSessionFacts(t *testing.T) {
	t.Parallel()
	permitted := domain.ExecutionAgentDetail{PendingPermissions: []domain.ExecutionPermission{{ID: "perm-1"}}}
	permitted.Status = domain.ExecutionAgentRunning
	archived := detail("agent-1", domain.ExecutionAgentRunning)
	archived.Archived = true
	archivedWithPermission := archived
	archivedWithPermission.PendingPermissions = []domain.ExecutionPermission{{ID: "perm-1"}}

	for _, tc := range []struct {
		name      string
		detail    domain.ExecutionAgentDetail
		wantState domain.ActivityState
		wantType  string
		wantOK    bool
	}{
		{"running", detail("a", domain.ExecutionAgentRunning), domain.ActivityActive, domain.ExecutionObservedRunning, true},
		{"initializing", detail("a", domain.ExecutionAgentInitializing), domain.ActivityActive, domain.ExecutionObservedRunning, true},
		{"idle is not completion", detail("a", domain.ExecutionAgentIdle), domain.ActivityIdle, domain.ExecutionObservedIdle, true},
		{"error", detail("a", domain.ExecutionAgentError), domain.ActivityExited, domain.ExecutionObservedFailed, true},
		{"closed", detail("a", domain.ExecutionAgentClosed), domain.ActivityExited, domain.ExecutionObservedClosed, true},
		{"permission blocks", permitted, domain.ActivityBlocked, domain.ExecutionObservedBlocked, true},
		{"archived wins over status", archived, domain.ActivityExited, domain.ExecutionObservedArchived, true},
		{"archived wins over permission", archivedWithPermission, domain.ActivityExited, domain.ExecutionObservedArchived, true},
		{"unknown status has no reading", domain.ExecutionAgentDetail{}, "", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			facts, ok := DeriveSessionFacts(tc.detail)
			if ok != tc.wantOK || facts.Activity != tc.wantState || facts.EventType != tc.wantType {
				t.Fatalf("facts = %#v ok=%v, want state=%q type=%q ok=%v",
					facts, ok, tc.wantState, tc.wantType, tc.wantOK)
			}
			// Nothing in this mapping may conclude the session is over.
			if facts.Activity == domain.ActivityExited && tc.detail.Status == domain.ExecutionAgentIdle {
				t.Fatal("idle must never map to exited")
			}
		})
	}
}

func TestObserverMapsStatusPermissionAndWorktreeFacts(t *testing.T) {
	t.Parallel()
	store := newMemoryStore()
	store.bindings["host-1"] = []domain.SessionExecutionBinding{binding("session-1", "agent-1")}
	remote := newRemote()
	blocked := detail("agent-1", domain.ExecutionAgentRunning)
	blocked.PendingPermissions = []domain.ExecutionPermission{
		{ID: "perm_c0ffee1234567890", ToolName: "Bash", Reason: "run the test suite"},
	}
	remote.SetAgent(blocked, true)
	lifecycle := &fakeLifecycle{}

	if err := newTestObserver(store, lifecycle, remote).Poll(context.Background()); err != nil {
		t.Fatalf("poll: %v", err)
	}

	if len(store.probes) != 1 || !store.probes[0].Reachable || store.probes[0].ServerID != "server-1" {
		t.Fatalf("probes = %#v", store.probes)
	}
	if len(store.observations) != 1 {
		t.Fatalf("observations = %#v", store.observations)
	}
	event := store.observations[0]
	if event.Type != domain.ExecutionObservedBlocked || event.Transport != domain.ExecutionEventInspect ||
		event.LaunchID != "launch-1" {
		t.Fatalf("event = %#v", event)
	}
	for _, want := range []string{`"worktree":"session-1:1"`, `"cwd":"/remote/worktree/session-1"`, `"perm_c0ffee1234567890"`} {
		if !strings.Contains(event.PayloadJSON, want) {
			t.Fatalf("payload %s is missing %s", event.PayloadJSON, want)
		}
	}
	if len(store.questions) != 1 || store.questions[0].ExternalID != "perm_c0ffee1234567890" {
		t.Fatalf("questions = %#v", store.questions)
	}
	if store.questions[0].WorkItemID != "work-1" {
		t.Fatalf("question lost its work item: %#v", store.questions[0])
	}
	// Blocked, not waiting_input: a permission cannot be answered with free text.
	if states := lifecycle.states(); len(states) != 1 || states[0] != domain.ActivityBlocked {
		t.Fatalf("activity = %v, want one blocked signal", states)
	}
	if store.observedAt["session-1"] != observerNow {
		t.Fatalf("observation clock = %v", store.observedAt["session-1"])
	}
}

func TestObserverHostOutageLeavesSessionsAlone(t *testing.T) {
	t.Parallel()
	store := newMemoryStore()
	store.bindings["host-1"] = []domain.SessionExecutionBinding{binding("session-1", "agent-1")}
	remote := newRemote()
	remote.SetHostReachable("host-1", false)
	lifecycle := &fakeLifecycle{}

	if err := newTestObserver(store, lifecycle, remote).Poll(context.Background()); err != nil {
		t.Fatalf("poll: %v", err)
	}

	if len(store.probes) != 1 || store.probes[0].Reachable || store.probes[0].Error == "" {
		t.Fatalf("probes = %#v", store.probes)
	}
	if len(lifecycle.signals) != 0 {
		t.Fatalf("an unreachable host wrote session state: %#v", lifecycle.signals)
	}
	if len(store.observations) != 0 || len(store.orphans) != 0 {
		t.Fatalf("an unreachable host produced session facts: %#v %#v", store.observations, store.orphans)
	}
	if inspectCount(remote) != 0 {
		t.Fatalf("an unreachable host was still inspected: %v", operations(remote))
	}
}

// silentlyUnreachable reports an outage as a flag with no error — the shape the
// deterministic backend refuses to produce, and the shape that must never be
// read as anything other than an outage.
type silentlyUnreachable struct{ ports.ExecutionObserver }

func (silentlyUnreachable) Status(context.Context, domain.ExecutionHostID) (domain.ExecutionHostStatus, error) {
	return domain.ExecutionHostStatus{HostID: "host-1", Reachable: false, ServerID: "server-1"}, nil
}

func TestObserverTreatsAnUnreachableFlagAsAnOutage(t *testing.T) {
	t.Parallel()
	store := newMemoryStore()
	store.bindings["host-1"] = []domain.SessionExecutionBinding{binding("session-1", "agent-1")}
	remote := newRemote()
	remote.SetAgent(detail("agent-1", domain.ExecutionAgentRunning), true)
	lifecycle := &fakeLifecycle{}

	observer := newTestObserver(store, lifecycle, silentlyUnreachable{ExecutionObserver: remote})
	if err := observer.Poll(context.Background()); err != nil {
		t.Fatalf("poll: %v", err)
	}
	if len(store.probes) != 1 || store.probes[0].Reachable {
		t.Fatalf("probes = %#v", store.probes)
	}
	if inspectCount(remote) != 0 || len(lifecycle.signals) != 0 {
		t.Fatalf("a silently unreachable host still drove session facts: %v", operations(remote))
	}
}

func TestObserverInspectFailureLeavesSessionFactsUnchanged(t *testing.T) {
	t.Parallel()
	store := newMemoryStore()
	store.bindings["host-1"] = []domain.SessionExecutionBinding{binding("session-1", "agent-1")}
	remote := newRemote()
	remote.FailNext(fake.OperationInspect, errors.New("empty result"))
	lifecycle := &fakeLifecycle{}

	if err := newTestObserver(store, lifecycle, remote).Poll(context.Background()); err != nil {
		t.Fatalf("poll: %v", err)
	}
	if len(lifecycle.signals) != 0 || len(store.observations) != 0 {
		t.Fatalf("an ambiguous inspect wrote facts: %#v %#v", lifecycle.signals, store.observations)
	}
	if len(store.probes) != 1 || !store.probes[0].Reachable {
		t.Fatalf("probes = %#v", store.probes)
	}
}

func TestObserverIdleIsNeverCompletion(t *testing.T) {
	t.Parallel()
	store := newMemoryStore()
	store.bindings["host-1"] = []domain.SessionExecutionBinding{binding("session-1", "agent-1")}
	remote := newRemote()
	remote.SetAgent(detail("agent-1", domain.ExecutionAgentIdle), true)
	lifecycle := &fakeLifecycle{}

	if err := newTestObserver(store, lifecycle, remote).Poll(context.Background()); err != nil {
		t.Fatalf("poll: %v", err)
	}
	if states := lifecycle.states(); len(states) != 1 || states[0] != domain.ActivityIdle {
		t.Fatalf("activity = %v, want idle", states)
	}
	if store.observations[0].Type != domain.ExecutionObservedIdle {
		t.Fatalf("event type = %q", store.observations[0].Type)
	}
}

func TestObserverIgnoresDuplicateObservations(t *testing.T) {
	t.Parallel()
	store := newMemoryStore()
	store.bindings["host-1"] = []domain.SessionExecutionBinding{binding("session-1", "agent-1")}
	remote := newRemote()
	remote.SetAgent(detail("agent-1", domain.ExecutionAgentRunning), true)
	observer := newTestObserver(store, &fakeLifecycle{}, remote)

	for i := 0; i < 3; i++ {
		if err := observer.Poll(context.Background()); err != nil {
			t.Fatalf("poll %d: %v", i, err)
		}
	}
	if len(store.seenObservations) != 1 {
		t.Fatalf("stored %d distinct observations, want 1", len(store.seenObservations))
	}
	if len(store.seenQuestions) != 0 {
		t.Fatalf("questions = %#v", store.questions)
	}

	// A genuine change is a new fact, not a duplicate.
	remote.SetAgent(detail("agent-1", domain.ExecutionAgentIdle), true)
	observer.now = func() time.Time { return observerNow.Add(time.Minute) }
	if err := observer.Poll(context.Background()); err != nil {
		t.Fatalf("poll after change: %v", err)
	}
	if len(store.seenObservations) != 2 {
		t.Fatalf("stored %d distinct observations, want 2", len(store.seenObservations))
	}
}

func TestObserverIngestsMissedFactsAfterRestart(t *testing.T) {
	t.Parallel()
	store := newMemoryStore()
	store.bindings["host-1"] = []domain.SessionExecutionBinding{binding("session-1", "agent-1")}
	remote := newRemote()
	remote.SetAgent(detail("agent-1", domain.ExecutionAgentIdle), true)

	first := newTestObserver(store, &fakeLifecycle{}, remote)
	if err := first.Poll(context.Background()); err != nil {
		t.Fatalf("poll: %v", err)
	}
	// Idle schedules the cold cadence, so the same process would wait. A fresh
	// process has no schedule and must read everything again immediately.
	if err := first.Poll(context.Background()); err != nil {
		t.Fatalf("second poll: %v", err)
	}
	if inspectCount(remote) != 1 {
		t.Fatalf("cold session was re-inspected within the tick: %v", operations(remote))
	}

	restarted := newTestObserver(store, &fakeLifecycle{}, remote)
	blocked := detail("agent-1", domain.ExecutionAgentRunning)
	blocked.PendingPermissions = []domain.ExecutionPermission{{ID: "perm-missed", ToolName: "Bash"}}
	remote.SetAgent(blocked, true)
	if err := restarted.Poll(context.Background()); err != nil {
		t.Fatalf("poll after restart: %v", err)
	}
	if inspectCount(remote) != 2 {
		t.Fatalf("a restarted observer did not re-read the session: %v", operations(remote))
	}
	if len(store.questions) != 1 || store.questions[0].ExternalID != "perm-missed" {
		t.Fatalf("the permission raised while AO was down was not ingested: %#v", store.questions)
	}
}

func TestObserverSurfacesOrphanedAgentAndWorkspacePairs(t *testing.T) {
	t.Parallel()
	store := newMemoryStore()
	bound := binding("session-1", "agent-1")
	lost := binding("session-2", "agent-gone")
	store.bindings["host-1"] = []domain.SessionExecutionBinding{bound, lost}
	remote := newRemote()
	remote.SetAgent(detail("agent-1", domain.ExecutionAgentRunning), true)
	// agent-gone is deliberately absent from details: the host neither lists nor
	// inspects it, which is the two-negative evidence the sweep requires.
	intruder := detail("agent-stray", domain.ExecutionAgentRunning)
	intruder.Cwd = bound.HostWorkspacePath
	remote.SetAgent(intruder, true)
	archivedStray := detail("agent-archived", domain.ExecutionAgentClosed)
	archivedStray.Archived = true
	remote.SetAgent(archivedStray, true)
	remote.SetListOwned("host-1", []domain.ExecutionAgentObservation{
		{HostID: "host-1", AgentID: "agent-1", Status: domain.ExecutionAgentRunning, Cwd: bound.HostWorkspacePath},
		{HostID: "host-1", AgentID: "agent-stray", Status: domain.ExecutionAgentRunning, Cwd: bound.HostWorkspacePath},
		{HostID: "host-1", AgentID: "agent-archived", Status: domain.ExecutionAgentClosed, Cwd: lost.HostWorkspacePath},
		{HostID: "host-1", AgentID: "someone-elses", Status: domain.ExecutionAgentRunning, Cwd: "/home/other/repo"},
	})

	if err := newSweepingObserver(store, &fakeLifecycle{}, remote).Poll(context.Background()); err != nil {
		t.Fatalf("poll: %v", err)
	}

	kinds := map[domain.ExecutionOrphanKind]domain.ExecutionOrphan{}
	for _, orphan := range store.orphans {
		kinds[orphan.Kind] = orphan
	}
	if len(store.orphans) != 2 {
		t.Fatalf("orphans = %#v, want exactly the stray agent and the lost one", store.orphans)
	}
	stray, ok := kinds[domain.ExecutionOrphanAgent]
	if !ok || stray.AgentID != "agent-stray" || stray.SessionID != "session-1" {
		t.Fatalf("stray orphan = %#v", stray)
	}
	missing, ok := kinds[domain.ExecutionOrphanMissingAgent]
	if !ok || missing.AgentID != "agent-gone" || missing.WorkspacePath != lost.HostWorkspacePath {
		t.Fatalf("missing-agent orphan = %#v", missing)
	}
}

func TestObserverDoesNotReportLostAgentsFromAnAmbiguousListing(t *testing.T) {
	t.Parallel()
	store := newMemoryStore()
	store.bindings["host-1"] = []domain.SessionExecutionBinding{binding("session-1", "agent-1")}
	remote := newRemote()
	remote.SetAgent(detail("agent-1", domain.ExecutionAgentRunning), true)
	// An empty listing has the same shape whether the host is idle or the query
	// was malformed, and Paseo's label filter is known to fail open. A direct
	// inspect still finds the agent, so nothing is lost.
	remote.SetListOwned("host-1", nil)

	if err := newSweepingObserver(store, &fakeLifecycle{}, remote).Poll(context.Background()); err != nil {
		t.Fatalf("poll: %v", err)
	}
	if len(store.orphans) != 0 {
		t.Fatalf("an empty listing was read as agent death: %#v", store.orphans)
	}
}

func TestObserverDefersTheSweepOnASaturatedHost(t *testing.T) {
	t.Parallel()
	store := newMemoryStore()
	remote := newRemote()
	var bindings []domain.SessionExecutionBinding
	for _, id := range []domain.SessionID{"s1", "s2", "s3", "s4", "s5"} {
		agentID := domain.ExecutionAgentID("agent-" + string(id))
		bindings = append(bindings, binding(id, agentID))
		remote.SetAgent(detail(agentID, domain.ExecutionAgentRunning), true)
	}
	store.bindings["host-1"] = bindings

	if err := newSweepingObserver(store, &fakeLifecycle{}, remote).Poll(context.Background()); err != nil {
		t.Fatalf("poll: %v", err)
	}
	for _, call := range operations(remote) {
		if call == "list" {
			t.Fatalf("a budget-starved tick still ran the sweep: %v", operations(remote))
		}
	}
}

func TestObserverEscalatesChangedServerIdentityWithoutTouchingSessions(t *testing.T) {
	t.Parallel()
	store := newMemoryStore()
	store.bindings["host-1"] = []domain.SessionExecutionBinding{binding("session-1", "agent-1")}
	remote := newRemote()
	remote.SetHostStatus(domain.ExecutionHostStatus{
		HostID: "host-1", Reachable: true, ServerID: "server-2", Version: "0.2.5",
	})
	remote.SetAgent(detail("agent-1", domain.ExecutionAgentRunning), true)
	lifecycle := &fakeLifecycle{}

	if err := newTestObserver(store, lifecycle, remote).Poll(context.Background()); err != nil {
		t.Fatalf("poll: %v", err)
	}

	if len(store.orphans) != 1 || store.orphans[0].Kind != domain.ExecutionOrphanServerIdentity {
		t.Fatalf("orphans = %#v", store.orphans)
	}
	if len(store.probes) != 1 || store.probes[0].Reachable {
		t.Fatalf("a changed identity must not count as a healthy probe: %#v", store.probes)
	}
	if inspectCount(remote) != 0 || len(lifecycle.signals) != 0 {
		t.Fatalf("agent ids from a replaced daemon were still used: %v %#v", operations(remote), lifecycle.signals)
	}
	if len(store.probes) > 0 && store.probes[0].ServerID != "" {
		t.Fatalf("the observed identity must not overwrite the registered one: %#v", store.probes[0])
	}
}

func TestObserverSpendsABoundedPollingBudget(t *testing.T) {
	t.Parallel()
	store := newMemoryStore()
	remote := newRemote()
	var bindings []domain.SessionExecutionBinding
	for _, id := range []domain.SessionID{"s1", "s2", "s3", "s4", "s5", "s6", "s7"} {
		agentID := domain.ExecutionAgentID("agent-" + string(id))
		bindings = append(bindings, binding(id, agentID))
		remote.SetAgent(detail(agentID, domain.ExecutionAgentRunning), true)
	}
	// Not launched yet: the outbox still owns it, and it must not cost budget.
	unlaunched := binding("s8", "")
	store.bindings["host-1"] = append(bindings, unlaunched)
	observer := newTestObserver(store, &fakeLifecycle{}, remote)

	if err := observer.Poll(context.Background()); err != nil {
		t.Fatalf("poll: %v", err)
	}
	// 5 s hot tick / 0.9 s per invocation, minus the status probe (FINDINGS S10).
	if got, want := inspectCount(remote), 4; got != want {
		t.Fatalf("inspected %d sessions in one tick, want %d: %v", got, want, operations(remote))
	}
	if _, deferred := store.observedAt["s8"]; deferred {
		t.Fatal("an unlaunched binding was observed")
	}

	// The deferred sessions are still due on the next tick.
	observer.now = func() time.Time { return observerNow.Add(time.Second) }
	if err := observer.Poll(context.Background()); err != nil {
		t.Fatalf("second poll: %v", err)
	}
	if got, want := inspectCount(remote), 7; got != want {
		t.Fatalf("inspected %d sessions across two ticks, want %d", got, want)
	}
}

func TestObserverPollsActiveHotAndEverythingElseCold(t *testing.T) {
	t.Parallel()
	store := newMemoryStore()
	store.bindings["host-1"] = []domain.SessionExecutionBinding{
		binding("session-active", "agent-active"), binding("session-idle", "agent-idle"),
	}
	remote := newRemote()
	remote.SetAgent(detail("agent-active", domain.ExecutionAgentRunning), true)
	remote.SetAgent(detail("agent-idle", domain.ExecutionAgentIdle), true)
	observer := newTestObserver(store, &fakeLifecycle{}, remote)

	if err := observer.Poll(context.Background()); err != nil {
		t.Fatalf("poll: %v", err)
	}
	observer.now = func() time.Time { return observerNow.Add(hotTick) }
	if err := observer.Poll(context.Background()); err != nil {
		t.Fatalf("second poll: %v", err)
	}

	inspects := map[string]int{}
	for _, call := range operations(remote) {
		inspects[call]++
	}
	if inspects["inspect:agent-active"] != 2 {
		t.Fatalf("an active session was not polled on the hot tick: %v", operations(remote))
	}
	if inspects["inspect:agent-idle"] != 1 {
		t.Fatalf("an idle session was polled on the hot tick: %v", operations(remote))
	}
}

func TestObserverSkipsSessionsBoundToAReplacedDaemon(t *testing.T) {
	t.Parallel()
	store := newMemoryStore()
	stale := binding("session-1", "agent-1")
	stale.BoundServerID = "server-old"
	store.bindings["host-1"] = []domain.SessionExecutionBinding{stale}
	store.hosts[0].ServerID = ""
	remote := newRemote()
	remote.SetAgent(detail("agent-1", domain.ExecutionAgentRunning), true)
	lifecycle := &fakeLifecycle{}

	if err := newTestObserver(store, lifecycle, remote).Poll(context.Background()); err != nil {
		t.Fatalf("poll: %v", err)
	}
	if inspectCount(remote) != 0 || len(lifecycle.signals) != 0 {
		t.Fatalf("a stale binding was still inspected: %v", operations(remote))
	}
	if len(store.orphans) != 1 || store.orphans[0].Kind != domain.ExecutionOrphanServerIdentity {
		t.Fatalf("orphans = %#v", store.orphans)
	}
}

func TestObserverSkipsDisabledAndLocalHosts(t *testing.T) {
	t.Parallel()
	store := newMemoryStore()
	store.hosts = []domain.ExecutionHost{
		{ID: "host-off", BackendType: domain.ExecutionBackendPaseo, Enabled: false},
		{ID: "host-local", BackendType: domain.ExecutionBackendLocal, Enabled: true},
	}
	remote := newRemote()

	if err := newTestObserver(store, &fakeLifecycle{}, remote).Poll(context.Background()); err != nil {
		t.Fatalf("poll: %v", err)
	}
	if len(operations(remote)) != 0 {
		t.Fatalf("calls = %v, want none", operations(remote))
	}
}
