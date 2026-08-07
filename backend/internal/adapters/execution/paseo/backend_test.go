package paseo

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

var backendTestNow = time.Date(2026, time.January, 2, 12, 0, 0, 0, time.UTC)

type memoryExecutionStore struct {
	hosts    map[domain.ExecutionHostID]domain.ExecutionHost
	bindings map[domain.SessionID]domain.SessionExecutionBinding
	events   *[]string
}

func newMemoryExecutionStore(events *[]string) *memoryExecutionStore {
	return &memoryExecutionStore{
		hosts: map[domain.ExecutionHostID]domain.ExecutionHost{
			"host-1": {
				ID: "host-1", BackendType: domain.ExecutionBackendPaseo,
				Enabled: true, ServerID: "server-1", PaseoVersion: SupportedVersion,
			},
		},
		bindings: make(map[domain.SessionID]domain.SessionExecutionBinding),
		events:   events,
	}
}

func (s *memoryExecutionStore) GetExecutionHost(_ context.Context, id domain.ExecutionHostID) (domain.ExecutionHost, []string, bool, error) {
	host, found := s.hosts[id]
	return host, nil, found, nil
}

func (s *memoryExecutionStore) GetSessionExecutionBinding(_ context.Context, id domain.SessionID) (domain.SessionExecutionBinding, bool, error) {
	binding, found := s.bindings[id]
	binding.LabelsWritten = cloneLabels(binding.LabelsWritten)
	return binding, found, nil
}

func (s *memoryExecutionStore) UpsertSessionExecutionBinding(_ context.Context, binding domain.SessionExecutionBinding) error {
	binding.LabelsWritten = cloneLabels(binding.LabelsWritten)
	s.bindings[binding.SessionID] = binding
	if s.events != nil {
		event := "persist:seed"
		switch {
		case binding.ExternalAgentID != "":
			event = "persist:agent"
		case len(binding.LabelsWritten) != 0:
			event = "persist:intent"
		case binding.ExternalWorkspaceID != "":
			event = "persist:workspace"
		}
		*s.events = append(*s.events, event)
	}
	return nil
}

type fakeExecutionClient struct {
	status          DaemonStatus
	statusErr       error
	workspaces      []Workspace
	workspaceResult Workspace
	workspaceErr    error
	runResult       RunResult
	runErr          error
	agents          []Agent
	listLabels      []string
	listErr         error
	details         map[string]AgentDetail
	inspectErr      error
	logs            string
	logsErr         error
	stopErr         error
	sendErr         error
	calls           []string
	events          *[]string
	onCreate        func()
	onRun           func()
}

func newFakeExecutionClient(events *[]string) *fakeExecutionClient {
	return &fakeExecutionClient{
		status: DaemonStatus{
			Status: "server_info", ServerID: "server-1", Version: SupportedVersion,
			DesktopManaged: boolPointer(false),
		},
		workspaceResult: Workspace{
			WorkspaceID: "wks-1", Name: "ao:session-1:1", Isolation: "worktree", Cwd: "/remote/worktree",
		},
		runResult: RunResult{AgentID: "agent-1", Status: "running", Provider: "codex", Cwd: "/remote/worktree"},
		details:   make(map[string]AgentDetail),
		events:    events,
	}
}

func (c *fakeExecutionClient) record(call string) {
	c.calls = append(c.calls, call)
	if c.events != nil {
		*c.events = append(*c.events, call)
	}
}

func (c *fakeExecutionClient) Version() string { return SupportedVersion }

func (c *fakeExecutionClient) Status(context.Context) (DaemonStatus, error) {
	c.record("status")
	return c.status, c.statusErr
}

func (c *fakeExecutionClient) CreateWorkspace(context.Context, WorkspaceCreateRequest) (Workspace, error) {
	c.record("create-workspace")
	if c.onCreate != nil {
		c.onCreate()
	}
	return c.workspaceResult, c.workspaceErr
}

func (c *fakeExecutionClient) ListWorkspaces(context.Context) ([]Workspace, error) {
	c.record("list-workspaces")
	return append([]Workspace(nil), c.workspaces...), nil
}

func (c *fakeExecutionClient) Run(context.Context, RunRequest) (RunResult, error) {
	c.record("run")
	if c.onRun != nil {
		c.onRun()
	}
	return c.runResult, c.runErr
}

func (c *fakeExecutionClient) ListAgents(_ context.Context, label string) ([]Agent, error) {
	c.record("list-agents")
	c.listLabels = append(c.listLabels, label)
	if c.listErr != nil {
		return nil, c.listErr
	}
	return append([]Agent(nil), c.agents...), nil
}

func (c *fakeExecutionClient) Inspect(_ context.Context, id string) (AgentDetail, error) {
	c.record("inspect:" + id)
	if c.inspectErr != nil {
		return AgentDetail{}, c.inspectErr
	}
	detail, found := c.details[id]
	if !found {
		return AgentDetail{}, errors.New("missing fake detail")
	}
	return detail, nil
}

func (c *fakeExecutionClient) Stop(_ context.Context, id string) error {
	c.record("stop:" + id)
	return c.stopErr
}

func (c *fakeExecutionClient) Delete(_ context.Context, id string) error {
	c.record("delete:" + id)
	return nil
}

func (c *fakeExecutionClient) Logs(_ context.Context, id string) (string, error) {
	c.record("logs:" + id)
	return c.logs, c.logsErr
}

func (c *fakeExecutionClient) Send(_ context.Context, id, message string) error {
	c.record("send:" + id + ":" + message)
	return c.sendErr
}

func provisionRequest() ports.ExecutionProvisionRequest {
	return ports.ExecutionProvisionRequest{
		SessionID: "session-1", ProjectID: "project-1", HostID: "host-1",
		WorkspaceTitle: "ao:session-1:1", RepoPath: "/repos/project", Branch: "ao/task/one",
		BaseBranch: "main", Provider: "codex", Model: "gpt-5.4", Mode: "auto",
	}
}

func launchRequest() ports.ExecutionLaunchRequest {
	return ports.ExecutionLaunchRequest{
		SessionID: "session-1", HostID: "host-1", WorkspaceID: "wks-1", IntentID: "intent-1",
		Prompt: "implement the task", Provider: "codex", Model: "gpt-5.4", Mode: "auto",
		Labels: map[string]string{
			"ao.session": "session-1", "ao.attempt": "1", "ao.intent": "intent-1",
			"paseo.worktree": "session-1:1",
		},
	}
}

func validCandidate(id string) AgentDetail {
	return AgentDetail{
		ID: id, Provider: "codex", Model: "gpt-5.4", Mode: "auto", Status: "running",
		Cwd: "/remote/worktree", Worktree: "session-1:1", CreatedAt: backendTestNow.Add(-time.Minute),
	}
}

func provisionedBackend(t *testing.T, client *fakeExecutionClient, store *memoryExecutionStore) *Backend {
	t.Helper()
	backend := newBackend(client, store, func() time.Time { return backendTestNow })
	if _, err := backend.Provision(context.Background(), provisionRequest()); err != nil {
		t.Fatalf("provision: %v", err)
	}
	return backend
}

func TestBackendPersistsTwoStepCreateIDsBeforeUse(t *testing.T) {
	t.Parallel()
	var events []string
	store := newMemoryExecutionStore(&events)
	client := newFakeExecutionClient(&events)
	backend := newBackend(client, store, func() time.Time { return backendTestNow })

	workspace, err := backend.Provision(context.Background(), provisionRequest())
	if err != nil {
		t.Fatal(err)
	}
	client.onRun = func() {
		binding := store.bindings["session-1"]
		if binding.ExternalWorkspaceID != "wks-1" || binding.LabelsWritten["ao.intent"] != "intent-1" {
			t.Fatalf("run began before workspace and intent were persisted: %#v", binding)
		}
	}
	agent, err := backend.Launch(context.Background(), launchRequest())
	if err != nil {
		t.Fatal(err)
	}
	if workspace.WorkspaceID != "wks-1" || agent.AgentID != "agent-1" {
		t.Fatalf("workspace=%#v agent=%#v", workspace, agent)
	}
	binding := store.bindings["session-1"]
	if binding.ExternalAgentID != "agent-1" {
		t.Fatalf("agent id was not persisted: %#v", binding)
	}
	assertBefore(t, events, "persist:workspace", "run")
	assertBefore(t, events, "persist:intent", "run")
	assertBefore(t, events, "run", "persist:agent")
}

func TestProvisionCreatesExactlyOneWorktreePerAttempt(t *testing.T) {
	t.Parallel()
	store := newMemoryExecutionStore(nil)
	client := newFakeExecutionClient(nil)
	backend := newBackend(client, store, func() time.Time { return backendTestNow })

	first, err := backend.Provision(context.Background(), provisionRequest())
	if err != nil {
		t.Fatal(err)
	}
	second, err := backend.Provision(context.Background(), provisionRequest())
	if err != nil {
		t.Fatal(err)
	}
	if first.WorkspaceID != second.WorkspaceID || countCall(client.calls, "create-workspace") != 1 {
		t.Fatalf("workspace ids %q/%q, calls=%v", first.WorkspaceID, second.WorkspaceID, client.calls)
	}
}

func TestProvisionReconcilesLostCreateResponseByTitle(t *testing.T) {
	t.Parallel()
	store := newMemoryExecutionStore(nil)
	client := newFakeExecutionClient(nil)
	client.workspaceErr = &Error{Kind: ErrorNetwork, Message: "Paseo command failed: timed out"}
	client.onCreate = func() { client.workspaces = []Workspace{client.workspaceResult} }
	backend := newBackend(client, store, func() time.Time { return backendTestNow })

	workspace, err := backend.Provision(context.Background(), provisionRequest())
	if err != nil {
		t.Fatal(err)
	}
	if workspace.WorkspaceID != "wks-1" || store.bindings["session-1"].ExternalWorkspaceID != "wks-1" {
		t.Fatalf("workspace was not reconciled: %#v", workspace)
	}
	if countCall(client.calls, "create-workspace") != 1 || countCall(client.calls, "list-workspaces") != 2 {
		t.Fatalf("calls=%v", client.calls)
	}
}

func TestTimedOutRunReconcilesByIntentAndVerifiesCandidate(t *testing.T) {
	t.Parallel()
	store := newMemoryExecutionStore(nil)
	client := newFakeExecutionClient(nil)
	backend := provisionedBackend(t, client, store)
	client.runErr = &Error{Kind: ErrorNetwork, Message: "Paseo command failed: timed out"}
	client.onRun = func() {
		client.agents = []Agent{{ID: "agent-lost"}}
		client.details["agent-lost"] = validCandidate("agent-lost")
	}

	agent, err := backend.Launch(context.Background(), launchRequest())
	if err != nil {
		t.Fatal(err)
	}
	if agent.AgentID != "agent-lost" || store.bindings["session-1"].ExternalAgentID != "agent-lost" {
		t.Fatalf("agent was not adopted: %#v", agent)
	}
	wantSequence := []string{"run", "list-agents", "inspect:agent-lost"}
	if !containsSequence(client.calls, wantSequence) {
		t.Fatalf("calls=%v, want subsequence=%v", client.calls, wantSequence)
	}
}

func TestHardRunFailureSweepsVerifiedPossiblyCreatedAgent(t *testing.T) {
	t.Parallel()
	store := newMemoryExecutionStore(nil)
	client := newFakeExecutionClient(nil)
	backend := provisionedBackend(t, client, store)
	hardErr := &Error{Kind: ErrorInvalidRequest, Message: "provider rejected prompt"}
	client.runErr = hardErr
	client.onRun = func() {
		client.agents = []Agent{{ID: "agent-zombie"}}
		client.details["agent-zombie"] = validCandidate("agent-zombie")
	}

	_, err := backend.Launch(context.Background(), launchRequest())
	if !errors.Is(err, hardErr) {
		t.Fatalf("error=%v", err)
	}
	want := []string{"list-agents", "inspect:agent-zombie", "stop:agent-zombie", "delete:agent-zombie"}
	if !containsSequence(client.calls, want) {
		t.Fatalf("calls=%v, want subsequence=%v", client.calls, want)
	}
	if store.bindings["session-1"].ExternalAgentID != "" {
		t.Fatal("hard-failed zombie was bound as a live agent")
	}
}

func TestHardRunFailureNeverSweepsUnverifiedCandidate(t *testing.T) {
	t.Parallel()
	store := newMemoryExecutionStore(nil)
	client := newFakeExecutionClient(nil)
	backend := provisionedBackend(t, client, store)
	client.runErr = &Error{Kind: ErrorInvalidRequest, Message: "provider rejected prompt"}
	client.onRun = func() {
		client.agents = []Agent{{ID: "agent-other"}}
		detail := validCandidate("agent-other")
		detail.Cwd = "/somebody/elses/worktree"
		client.details["agent-other"] = detail
	}

	if _, err := backend.Launch(context.Background(), launchRequest()); err == nil {
		t.Fatal("unverified candidate did not fail launch")
	}
	if countPrefix(client.calls, "stop:") != 0 || countPrefix(client.calls, "delete:") != 0 {
		t.Fatalf("unverified candidate was mutated: %v", client.calls)
	}
}

func TestRepeatedUnknownLaunchDoesNotRetryRunBlind(t *testing.T) {
	t.Parallel()
	store := newMemoryExecutionStore(nil)
	client := newFakeExecutionClient(nil)
	backend := provisionedBackend(t, client, store)
	client.runErr = &Error{Kind: ErrorNetwork, Message: "Paseo command failed: timed out"}

	if _, err := backend.Launch(context.Background(), launchRequest()); err == nil {
		t.Fatal("ambiguous timeout unexpectedly succeeded")
	}
	client.runErr = nil
	if _, err := backend.Launch(context.Background(), launchRequest()); err == nil || !strings.Contains(err.Error(), "unresolved") {
		t.Fatalf("second launch error=%v", err)
	}
	if countCall(client.calls, "run") != 1 {
		t.Fatalf("run was retried blindly: %v", client.calls)
	}
}

func TestAdoptionCandidateVerification(t *testing.T) {
	t.Parallel()
	binding := domain.SessionExecutionBinding{
		HostWorkspacePath: "/remote/worktree", CreatedAt: backendTestNow.Add(-2 * time.Minute),
	}
	req := launchRequest()
	tests := []struct {
		name   string
		mutate func(*AgentDetail)
	}{
		{"wrong worktree", func(detail *AgentDetail) { detail.Worktree = "other:1" }},
		{"wrong cwd", func(detail *AgentDetail) { detail.Cwd = "/other" }},
		{"archived", func(detail *AgentDetail) { detail.Archived = true }},
		{"archived timestamp", func(detail *AgentDetail) { value := backendTestNow; detail.ArchivedAt = &value }},
		{"implausibly old", func(detail *AgentDetail) { detail.CreatedAt = backendTestNow.Add(-time.Hour) }},
		{"future", func(detail *AgentDetail) { detail.CreatedAt = backendTestNow.Add(time.Hour) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			detail := validCandidate("candidate")
			test.mutate(&detail)
			if err := verifyAdoptionCandidate(binding, req, detail, backendTestNow); err == nil {
				t.Fatal("invalid adoption candidate accepted")
			}
		})
	}
}

func TestBackendRefusesUnsafeHostIdentity(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func(*fakeExecutionClient)
	}{
		{"desktop managed", func(client *fakeExecutionClient) { client.status.DesktopManaged = boolPointer(true) }},
		{"server changed", func(client *fakeExecutionClient) { client.status.ServerID = "server-rebuilt" }},
		{"unsupported remote version", func(client *fakeExecutionClient) { client.status.Version = "0.3.0" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := newMemoryExecutionStore(nil)
			client := newFakeExecutionClient(nil)
			test.mutate(client)
			backend := newBackend(client, store, func() time.Time { return backendTestNow })
			if _, err := backend.Provision(context.Background(), provisionRequest()); err == nil {
				t.Fatal("unsafe host accepted")
			}
			if countCall(client.calls, "create-workspace") != 0 || len(store.bindings) != 0 {
				t.Fatalf("unsafe host caused mutation: calls=%v bindings=%v", client.calls, store.bindings)
			}
		})
	}
}

func TestBackendStatusCommandsPlaceHostOnSubcommand(t *testing.T) {
	t.Parallel()
	status, err := statusArgs("worker:6767")
	if err != nil {
		t.Fatal(err)
	}
	workspaces, err := workspaceListArgs("worker:6767")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(status, []string{"status", "--host", "worker:6767", "--json"}) {
		t.Fatalf("status args=%v", status)
	}
	if !reflect.DeepEqual(workspaces, []string{"workspace", "ls", "--host", "worker:6767", "--json"}) {
		t.Fatalf("workspace args=%v", workspaces)
	}
}

func TestWorkspaceAttemptAndLabelsStayInLockstep(t *testing.T) {
	t.Parallel()
	attempt, err := workspaceAttempt("ao:session-1:7", "session-1")
	if err != nil || attempt != 7 {
		t.Fatalf("attempt=%d error=%v", attempt, err)
	}
	req := launchRequest()
	req.Labels["ao.attempt"] = "7"
	req.Labels["paseo.worktree"] = "session-1:7"
	if err := validateRequiredLabels(req, 7); err != nil {
		t.Fatal(err)
	}
	req.Labels["paseo.worktree"] = "somebody-else:7"
	if err := validateRequiredLabels(req, 7); err == nil {
		t.Fatal("mismatched worktree label accepted")
	}
}

func assertBefore(t *testing.T, events []string, first, second string) {
	t.Helper()
	firstIndex, secondIndex := -1, -1
	for index, event := range events {
		if event == first && firstIndex == -1 {
			firstIndex = index
		}
		if event == second && secondIndex == -1 {
			secondIndex = index
		}
	}
	if firstIndex == -1 || secondIndex == -1 || firstIndex >= secondIndex {
		t.Fatalf("%q must precede %q in %v", first, second, events)
	}
}

func countCall(calls []string, want string) int {
	count := 0
	for _, call := range calls {
		if call == want {
			count++
		}
	}
	return count
}

func countPrefix(calls []string, prefix string) int {
	count := 0
	for _, call := range calls {
		if strings.HasPrefix(call, prefix) {
			count++
		}
	}
	return count
}

func containsSequence(calls, want []string) bool {
	index := 0
	for _, call := range calls {
		if index < len(want) && call == want[index] {
			index++
		}
	}
	return index == len(want)
}

func boolPointer(value bool) *bool { return &value }
