package dispatch

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/paseoevent"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
)

var errSimulatedCrash = errors.New("simulated process loss")

func TestDispatchCrashCheckpointsNeverDuplicateRemoteResources(t *testing.T) {
	checkpoints := []struct {
		name       string
		checkpoint deliveryCheckpoint
	}{
		{name: "after durable enqueue"},
		{name: "after command claim", checkpoint: checkpointClaimed},
		{name: "after workspace provision", checkpoint: checkpointProvisioned},
		{name: "after agent launch", checkpoint: checkpointLaunched},
	}
	for _, test := range checkpoints {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			now := time.Date(2026, time.August, 7, 2, 0, 0, 0, time.UTC)
			store := newDispatchTestStore(t, now)
			ids := 0
			service := newService(store, func() time.Time { return now }, func() string {
				ids++
				return fmt.Sprintf("id-%d", ids)
			})
			dispatched, err := service.Dispatch(ctx, testDispatchRequest())
			if err != nil {
				t.Fatal(err)
			}

			backend := newIdempotentBackend()
			worker := NewWorker(store, BackendResolverFunc(func(domain.ExecutionHostID) (ports.ExecutionBackend, bool) {
				return backend, true
			}))
			worker.now = func() time.Time { return now }
			worker.lease = time.Second
			if test.checkpoint != "" {
				worker.checkpoint = func(got deliveryCheckpoint) error {
					if got == test.checkpoint {
						return errSimulatedCrash
					}
					return nil
				}
				if delivered, err := worker.DeliverOne(ctx); !delivered || !errors.Is(err, errSimulatedCrash) {
					t.Fatalf("first delivery = (%v, %v), want simulated crash", delivered, err)
				}
				now = now.Add(2 * time.Second)
				worker.checkpoint = nil
			}
			if delivered, err := worker.DeliverOne(ctx); err != nil || !delivered {
				t.Fatalf("replayed delivery = (%v, %v)", delivered, err)
			}
			if backend.workspaceCreations != 1 || backend.agentCreations != 1 {
				t.Fatalf("remote creations = workspaces:%d agents:%d", backend.workspaceCreations, backend.agentCreations)
			}
			command, ok, err := store.GetExecutionCommand(ctx, dispatched.Command.ID)
			if err != nil || !ok || command.State != domain.ExecutionCommandAcknowledged {
				t.Fatalf("command after replay = (%+v, %v, %v)", command, ok, err)
			}
			session, ok, err := store.GetSession(ctx, dispatched.Session.ID)
			if err != nil || !ok || session.Metadata.RuntimeHandleID == "" || session.Metadata.RuntimeLaunchID != "id-2" {
				t.Fatalf("session after replay = (%+v, %v, %v)", session, ok, err)
			}
		})
	}
}

func TestSecondDispatchClaimRollsBackSessionAndCommand(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, time.August, 7, 3, 0, 0, 0, time.UTC)
	store := newDispatchTestStore(t, now)
	service := New(store)
	first, err := service.Dispatch(ctx, testDispatchRequest())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Dispatch(ctx, testDispatchRequest()); err == nil {
		t.Fatal("second active implementer dispatch unexpectedly succeeded")
	}
	sessions, err := store.ListSessions(ctx, "project")
	if err != nil || len(sessions) != 1 || sessions[0].ID != first.Session.ID {
		t.Fatalf("sessions after rejected claim = (%+v, %v)", sessions, err)
	}
	commands, err := store.ListExecutionCommandsBySession(ctx, first.Session.ID)
	if err != nil || len(commands) != 1 {
		t.Fatalf("commands after rejected claim = (%+v, %v)", commands, err)
	}
}

func TestOutboxPreservesPerSessionFIFO(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, time.August, 7, 4, 0, 0, 0, time.UTC)
	store := newDispatchTestStore(t, now)
	dispatched, err := New(store).Dispatch(ctx, testDispatchRequest())
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.EnqueueExecutionCommand(ctx, domain.ExecutionCommand{
		ID: "message-1", SessionID: dispatched.Session.ID, HostID: dispatched.Binding.HostID,
		Type: domain.ExecutionCommandSendMessage, PayloadJSON: `{"message":"next"}`,
		IdempotencyKey: string(dispatched.Session.ID) + ":message-1", CreatedAt: now.Add(time.Second),
	})
	if err != nil || second.Sequence != 2 {
		t.Fatalf("enqueue second command = (%+v, %v)", second, err)
	}
	first, found, err := store.ClaimNextExecutionCommand(ctx, now, now.Add(time.Minute))
	if err != nil || !found || first.ID != dispatched.Command.ID {
		t.Fatalf("first claim = (%+v, %v, %v)", first, found, err)
	}
	if got, found, err := store.ClaimNextExecutionCommand(ctx, now, now.Add(time.Minute)); err != nil || found {
		t.Fatalf("overtaking claim = (%+v, %v, %v)", got, found, err)
	}
	if err := store.AcknowledgeExecutionStart(ctx, first.ID, first.SessionID, "paseo:host/agent", "launch", now); err != nil {
		t.Fatal(err)
	}
	got, found, err := store.ClaimNextExecutionCommand(ctx, now.Add(2*time.Second), now.Add(time.Minute))
	if err != nil || !found || got.ID != second.ID {
		t.Fatalf("second claim = (%+v, %v, %v)", got, found, err)
	}
}

func testDispatchRequest() Request {
	return Request{
		WorkItemID: "work-1", ProjectID: "project", TrustZone: domain.ExecutionTrustZoneWork,
		RequiredCapabilities: []string{"linux"}, Harness: domain.HarnessCodex,
		DisplayName: "Implement work", Branch: "ao/work-1", Provider: "codex",
		Model: "gpt-test", Mode: "auto", Prompt: "Implement the approved task.",
	}
}

func newDispatchTestStore(t *testing.T, now time.Time) *sqlite.Store {
	t.Helper()
	store, err := sqlite.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	if err := store.UpsertProject(ctx, domain.ProjectRecord{
		ID: "project", Path: "/local/project", RegisteredAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertWorkItem(ctx, domain.WorkItem{
		ID: "work-1", ProjectID: "project", Title: "Approved work", ApprovalState: domain.WorkItemApproved,
		LifecycleFact: domain.WorkItemOpen, CreatedByType: "human", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertExecutionHost(ctx, domain.ExecutionHost{
		ID: "host", Name: "worker", BackendType: domain.ExecutionBackendPaseo,
		Transport: domain.ExecutionTransportTailscale, Endpoint: "worker:6767",
		TrustZone: domain.ExecutionTrustZoneWork, Enabled: true, MaxConcurrentSessions: 4,
		ServerID: "server", RequiresNoMCP: true, RequiresNoRelay: true,
		LastSuccessfulProbeAt: now, CreatedAt: now, UpdatedAt: now,
	}, []string{"linux"}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertProjectHostBinding(ctx, domain.ProjectHostBinding{
		ProjectID: "project", HostID: "host", HostRepoPath: "/remote/project",
		BaseBranch: "main", Priority: 1, Enabled: true, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	return store
}

type idempotentBackend struct {
	mu                 sync.Mutex
	workspaces         map[domain.SessionID]domain.ExecutionWorkspace
	agents             map[domain.ExecutionIntentID]domain.ExecutionAgent
	workspaceCreations int
	agentCreations     int
	prompts            []string
	onLaunch           func()
}

func newIdempotentBackend() *idempotentBackend {
	return &idempotentBackend{
		workspaces: make(map[domain.SessionID]domain.ExecutionWorkspace),
		agents:     make(map[domain.ExecutionIntentID]domain.ExecutionAgent),
	}
}

func (b *idempotentBackend) Provision(_ context.Context, req ports.ExecutionProvisionRequest) (domain.ExecutionWorkspace, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if workspace, ok := b.workspaces[req.SessionID]; ok {
		return workspace, nil
	}
	b.workspaceCreations++
	workspace := domain.ExecutionWorkspace{
		HostID: req.HostID, WorkspaceID: domain.ExecutionWorkspaceID(fmt.Sprintf("workspace-%d", b.workspaceCreations)),
		Title: req.WorkspaceTitle, RepoPath: req.RepoPath, Branch: req.Branch,
	}
	b.workspaces[req.SessionID] = workspace
	return workspace, nil
}

func (b *idempotentBackend) Launch(_ context.Context, req ports.ExecutionLaunchRequest) (domain.ExecutionAgent, error) {
	if b.onLaunch != nil {
		b.onLaunch()
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.prompts = append(b.prompts, req.Prompt)
	if agent, ok := b.agents[req.IntentID]; ok {
		return agent, nil
	}
	b.agentCreations++
	agent := domain.ExecutionAgent{
		HostID: req.HostID, AgentID: domain.ExecutionAgentID(fmt.Sprintf("agent-%d", b.agentCreations)),
		WorkspaceID: req.WorkspaceID,
	}
	b.agents[req.IntentID] = agent
	return agent, nil
}

func TestDeliveryCommitsOneBriefBeforeTheLaunchItAuthorizes(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, time.August, 7, 5, 0, 0, 0, time.UTC)
	store := newDispatchTestStore(t, now)
	dispatched, err := New(store).Dispatch(ctx, testDispatchRequest())
	if err != nil {
		t.Fatal(err)
	}

	backend := newIdempotentBackend()
	backend.onLaunch = func() {
		// The brief carries the launch's report nonce. Minting it after the agent
		// starts would leave a running agent reporting under a nonce AO never
		// recorded, and those reports would be unreadable forever.
		if _, found, err := store.GetLatestSessionBrief(ctx, dispatched.Session.ID); err != nil || !found {
			t.Fatalf("launch began with no committed brief: found=%v err=%v", found, err)
		}
	}
	worker := NewWorkerWithBriefs(store, BackendResolverFunc(func(domain.ExecutionHostID) (ports.ExecutionBackend, bool) {
		return backend, true
	}), paseoevent.NewBriefs(store))
	worker.now = func() time.Time { return now }
	worker.lease = time.Second

	// Crash after the workspace exists, then replay the whole delivery.
	worker.checkpoint = func(got deliveryCheckpoint) error {
		if got == checkpointProvisioned {
			return errSimulatedCrash
		}
		return nil
	}
	if delivered, err := worker.DeliverOne(ctx); !delivered || !errors.Is(err, errSimulatedCrash) {
		t.Fatalf("first delivery = (%v, %v), want the simulated crash", delivered, err)
	}
	now = now.Add(2 * time.Second)
	worker.checkpoint = nil
	if delivered, err := worker.DeliverOne(ctx); err != nil || !delivered {
		t.Fatalf("replayed delivery = (%v, %v)", delivered, err)
	}

	row, found, err := store.GetLatestSessionBrief(ctx, dispatched.Session.ID)
	if err != nil || !found {
		t.Fatalf("brief after replay: found=%v err=%v", found, err)
	}
	// One brief, one nonce: a redelivery must not supersede the contract the
	// first attempt may already have handed to an agent.
	if row.Version != 1 || row.SupersedesBriefID != "" {
		t.Fatalf("brief = %#v, want a single version", row)
	}
	brief, err := paseoevent.DecodeBrief(row.BriefJSON)
	if err != nil {
		t.Fatalf("decode brief: %v", err)
	}
	if brief.LaunchID != dispatched.Binding.LaunchID || brief.ReportNonce != row.ReportNonce {
		t.Fatalf("brief = %#v, want it bound to this launch", brief)
	}
	if len(backend.prompts) == 0 {
		t.Fatal("no launch prompt recorded")
	}
	for _, prompt := range backend.prompts {
		if prompt != brief.Prompt() {
			t.Fatalf("launch prompt is not the brief's:\n%s", prompt)
		}
		if !strings.Contains(prompt, row.ReportNonce) {
			t.Fatal("the launched agent was never told its report nonce")
		}
		if !strings.Contains(prompt, testDispatchRequest().Prompt) {
			t.Fatal("the brief dropped the approved work prompt")
		}
	}
}
