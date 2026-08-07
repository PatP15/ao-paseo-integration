package execution

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
)

var testNow = time.Date(2026, time.August, 7, 12, 0, 0, 0, time.UTC)

// fakeStore records what the service asked it to commit. Nothing here talks to a
// host: the service's whole contract is that a decision is durable before any
// remote call, so the assertions are about what lands in the outbox.
type fakeStore struct {
	hosts       []domain.ExecutionHost
	caps        map[domain.ExecutionHostID][]string
	bindings    map[domain.ExecutionHostID][]domain.SessionExecutionBinding
	questions   map[string]domain.ExecutionInboxQuestion
	upserted    domain.ExecutionHost
	upsertedCap []string
	resolved    domain.ExecutionQuestionResolution
	resolveErr  error

	projectBindings map[domain.ProjectID][]domain.ProjectHostBinding
	upsertedBinding domain.ProjectHostBinding
}

// UpsertProjectHostBinding records where a project is checked out on a host.
// Without a binding the router has no candidates at all, so this is what makes
// a project dispatchable.
func (f *fakeStore) UpsertProjectHostBinding(_ context.Context, binding domain.ProjectHostBinding) error {
	f.upsertedBinding = binding
	if f.projectBindings == nil {
		f.projectBindings = map[domain.ProjectID][]domain.ProjectHostBinding{}
	}
	f.projectBindings[binding.ProjectID] = append(f.projectBindings[binding.ProjectID], binding)
	return nil
}

func (f *fakeStore) ListProjectHostBindings(_ context.Context, projectID domain.ProjectID) ([]domain.ProjectHostBinding, error) {
	return f.projectBindings[projectID], nil
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		caps:      map[domain.ExecutionHostID][]string{},
		bindings:  map[domain.ExecutionHostID][]domain.SessionExecutionBinding{},
		questions: map[string]domain.ExecutionInboxQuestion{},
	}
}

func (f *fakeStore) ListExecutionHosts(context.Context) ([]domain.ExecutionHost, error) {
	return f.hosts, nil
}

func (f *fakeStore) GetExecutionHost(_ context.Context, id domain.ExecutionHostID) (domain.ExecutionHost, []string, bool, error) {
	for _, host := range f.hosts {
		if host.ID == id {
			return host, f.caps[id], true, nil
		}
	}
	return domain.ExecutionHost{}, nil, false, nil
}

func (f *fakeStore) UpsertExecutionHost(_ context.Context, host domain.ExecutionHost, caps []string) error {
	f.upserted, f.upsertedCap = host, caps
	f.hosts = append(f.hosts, host)
	f.caps[host.ID] = caps
	return nil
}

func (f *fakeStore) ListActiveSessionExecutionBindingsByHost(
	_ context.Context, id domain.ExecutionHostID,
) ([]domain.SessionExecutionBinding, error) {
	return f.bindings[id], nil
}

func (f *fakeStore) ListOpenExecutionQuestions(context.Context) ([]domain.ExecutionInboxQuestion, error) {
	out := make([]domain.ExecutionInboxQuestion, 0, len(f.questions))
	for _, question := range f.questions {
		out = append(out, question)
	}
	return out, nil
}

func (f *fakeStore) GetExecutionQuestion(_ context.Context, id string) (domain.ExecutionInboxQuestion, bool, error) {
	question, ok := f.questions[id]
	return question, ok, nil
}

func (f *fakeStore) ResolveExecutionQuestion(
	_ context.Context, resolution domain.ExecutionQuestionResolution,
) (domain.ExecutionCommand, error) {
	if f.resolveErr != nil {
		return domain.ExecutionCommand{}, f.resolveErr
	}
	f.resolved = resolution
	return domain.ExecutionCommand{
		ID: resolution.CommandID, SessionID: "project-1", HostID: "worker-1",
		Type: resolution.CommandType, PayloadJSON: resolution.PayloadJSON,
		State: domain.ExecutionCommandPending,
	}, nil
}

func newTestService(store Store) *Service {
	ids := 0
	return newService(store, func() time.Time { return testNow }, func() string {
		ids++
		return "id-1"
	})
}

func errCode(t *testing.T, err error) string {
	t.Helper()
	var apiErr *apierr.Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("err = %v, want an apierr.Error", err)
	}
	return apiErr.Code
}

func validHostInput() HostInput {
	return HostInput{
		ID: "worker-1", Name: "Linux worker", Transport: domain.ExecutionTransportTailscale,
		Endpoint: "worker.example.ts.net:6780", EndpointSecretRef: "keychain://worker",
		TrustZone: domain.ExecutionTrustZoneWork, Enabled: true, MaxConcurrentSessions: 4,
		RequiresNoMCP: true, RequiresNoRelay: true, Capabilities: []string{"Linux", "linux", " docker "},
	}
}

func TestRegisterHostRejectsUnsafeRegistrations(t *testing.T) {
	tests := []struct {
		name string
		code string
		edit func(*HostInput)
	}{
		{
			// A colonless host string resolves to null in the remote CLI and falls
			// through to the LOCAL daemon, so this typo would run remote work here.
			name: "endpoint without a colon", code: "HOST_ENDPOINT_INVALID",
			edit: func(in *HostInput) { in.Endpoint = "worker.example.ts.net" },
		},
		{
			name: "endpoint that is a flag", code: "HOST_ENDPOINT_INVALID",
			edit: func(in *HostInput) { in.Endpoint = "--host=evil:1" },
		},
		{
			// A relay offer URL and a query-string password are both bearer
			// credentials; storing one plaintext would echo it from every list call.
			name: "endpoint carrying a password", code: "HOST_ENDPOINT_HAS_CREDENTIAL",
			edit: func(in *HostInput) { in.Endpoint = "tcp://worker:6780?ssl=true&password=hunter2" },
		},
		{
			name: "endpoint carrying a relay token", code: "HOST_ENDPOINT_HAS_CREDENTIAL",
			edit: func(in *HostInput) { in.Endpoint = "https://relay.paseo.sh/o/abc?token=deadbeef" },
		},
		{
			// The remote daemon's agent-control tool catalog is gated by one
			// daemon-wide switch. Without it every dispatched agent could dispatch
			// more, so AO refuses the host rather than defaulting the flag.
			name: "MCP injection not disabled", code: "HOST_REQUIRES_NO_MCP",
			edit: func(in *HostInput) { in.RequiresNoMCP = false },
		},
		{
			name: "unknown transport", code: "HOST_TRANSPORT_INVALID",
			edit: func(in *HostInput) { in.Transport = "ssh" },
		},
		{
			name: "unknown trust zone", code: "HOST_TRUST_ZONE_INVALID",
			edit: func(in *HostInput) { in.TrustZone = "personal" },
		},
		{
			name: "no concurrency", code: "HOST_CONCURRENCY_INVALID",
			edit: func(in *HostInput) { in.MaxConcurrentSessions = 0 },
		},
		{
			name: "empty capability", code: "HOST_CAPABILITY_INVALID",
			edit: func(in *HostInput) { in.Capabilities = []string{"linux", ""} },
		},
		{
			name: "no id", code: "HOST_ID_REQUIRED",
			edit: func(in *HostInput) { in.ID = "  " },
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := newFakeStore()
			in := validHostInput()
			test.edit(&in)
			if _, err := newTestService(store).RegisterHost(context.Background(), in); errCode(t, err) != test.code {
				t.Fatalf("code = %q, want %q", errCode(t, err), test.code)
			}
			if store.upserted.ID != "" {
				t.Fatal("a rejected registration must not reach the store")
			}
		})
	}
}

func TestRegisterHostNormalizesAndPreservesObservedFacts(t *testing.T) {
	store := newFakeStore()
	probed := testNow.Add(-time.Minute)
	store.hosts = []domain.ExecutionHost{{
		ID: "worker-1", Name: "old name", BackendType: domain.ExecutionBackendPaseo,
		ServerID: "srv_original", PaseoVersion: "0.2.5", LastSuccessfulProbeAt: probed,
		CreatedAt: testNow.Add(-time.Hour),
	}}
	service := newTestService(store)

	host, err := service.RegisterHost(context.Background(), validHostInput())
	if err != nil {
		t.Fatalf("register host: %v", err)
	}
	// Capabilities are matched exactly during routing, so "Linux" and "linux" on
	// two hosts must not become two different capabilities.
	if got := store.upsertedCap; len(got) != 2 || got[0] != "linux" || got[1] != "docker" {
		t.Fatalf("capabilities = %v, want [linux docker]", got)
	}
	// A registry edit must not overwrite the observed server identity: that value
	// is the only evidence a daemon was replaced, which invalidates every agent id
	// AO holds for the host.
	if store.upserted.ServerID != "srv_original" || store.upserted.PaseoVersion != "0.2.5" {
		t.Fatalf("upserted host lost observed facts: %#v", store.upserted)
	}
	if !store.upserted.LastSuccessfulProbeAt.Equal(probed) {
		t.Fatalf("upserted probe time = %v, want %v", store.upserted.LastSuccessfulProbeAt, probed)
	}
	if !store.upserted.CreatedAt.Equal(testNow.Add(-time.Hour)) {
		t.Fatalf("createdAt = %v, want the original registration time", store.upserted.CreatedAt)
	}
	if store.upserted.BackendType != domain.ExecutionBackendPaseo {
		t.Fatalf("backend type = %q", store.upserted.BackendType)
	}
	if !host.Reachable {
		t.Fatal("a host whose latest probe succeeded reads as reachable")
	}
}

func TestListHostsDerivesReachabilityAndRedactsCredentials(t *testing.T) {
	store := newFakeStore()
	store.hosts = []domain.ExecutionHost{
		{
			ID: "online", Endpoint: "a:1",
			LastSuccessfulProbeAt: testNow, LastFailedProbeAt: testNow.Add(-time.Minute),
		},
		{
			ID: "offline", Endpoint: "tcp://b:2?password=hunter2",
			LastSuccessfulProbeAt: testNow.Add(-time.Hour), LastFailedProbeAt: testNow,
			LastProbeError: "connection refused",
		},
		{ID: "never-probed", Endpoint: "c:3"},
	}
	store.caps["online"] = []string{"linux"}
	store.bindings["online"] = []domain.SessionExecutionBinding{{SessionID: "project-1"}, {SessionID: "project-2"}}

	hosts, err := newTestService(store).ListHosts(context.Background())
	if err != nil {
		t.Fatalf("list hosts: %v", err)
	}
	if len(hosts) != 3 {
		t.Fatalf("hosts = %d, want 3", len(hosts))
	}
	if !hosts[0].Reachable || hosts[0].ActiveSessions != 2 {
		t.Fatalf("online host = %#v", hosts[0])
	}
	// An older success does not outvote the most recent failure.
	if hosts[1].Reachable {
		t.Fatal("a host whose latest probe failed must not read as reachable")
	}
	if hosts[1].Endpoint != "tcp://b:2?password=REDACTED" {
		t.Fatalf("endpoint = %q, want the credential masked", hosts[1].Endpoint)
	}
	if hosts[2].Reachable {
		t.Fatal("a never-probed host is not reachable")
	}
	if hosts[2].Capabilities == nil {
		t.Fatal("capabilities must serialise as an empty list, never null")
	}
}

func agentQuestion() domain.ExecutionInboxQuestion {
	return domain.ExecutionInboxQuestion{
		ID: "q-agent", SessionID: "project-1", Source: domain.ExecutionQuestionAgentEvent,
		ExternalID: "event-1", Question: "Rebase or merge?", CreatedAt: testNow,
	}
}

func permissionQuestion() domain.ExecutionInboxQuestion {
	return domain.ExecutionInboxQuestion{
		ID: "q-perm", SessionID: "project-1", Source: domain.ExecutionQuestionPaseoPermission,
		ExternalID: "perm_2f6c9a4b8e1d0f37a5c2b9e4d8f1a6c3", Question: "Allow Bash", CreatedAt: testNow,
	}
}

func TestAnswerSendsAMessageAndRefusesPermissions(t *testing.T) {
	store := newFakeStore()
	store.questions["q-agent"] = agentQuestion()
	store.questions["q-perm"] = permissionQuestion()
	service := newTestService(store)
	ctx := context.Background()

	command, err := service.Answer(ctx, AnswerInput{QuestionID: "q-agent", Answer: " rebase ", AnsweredBy: "operator"})
	if err != nil {
		t.Fatalf("answer: %v", err)
	}
	if command.Type != domain.ExecutionCommandSendMessage {
		t.Fatalf("command type = %q, want send_message", command.Type)
	}
	var payload domain.ExecutionAnswerPayload
	if err := json.Unmarshal([]byte(store.resolved.PayloadJSON), &payload); err != nil {
		t.Fatalf("decode payload %q: %v", store.resolved.PayloadJSON, err)
	}
	if payload.QuestionID != "q-agent" || payload.Message != "rebase" {
		t.Fatalf("payload = %#v", payload)
	}
	if store.resolved.AnsweredBy != "operator" {
		t.Fatalf("answeredBy = %q", store.resolved.AnsweredBy)
	}

	// A host permission request pauses the agent on a prompt that text cannot
	// release. Answering it would leave the request pending while AO believed it
	// had replied.
	if _, err := service.Answer(ctx, AnswerInput{QuestionID: "q-perm", Answer: "yes"}); errCode(t, err) != "QUESTION_REQUIRES_DECISION" {
		t.Fatalf("code = %q, want QUESTION_REQUIRES_DECISION", errCode(t, err))
	}
	if _, err := service.Answer(ctx, AnswerInput{QuestionID: "q-agent", Answer: "  "}); errCode(t, err) != "ANSWER_REQUIRED" {
		t.Fatalf("code = %q, want ANSWER_REQUIRED", errCode(t, err))
	}
	if _, err := service.Answer(ctx, AnswerInput{QuestionID: "nope", Answer: "hi"}); errCode(t, err) != "QUESTION_NOT_FOUND" {
		t.Fatalf("code = %q, want QUESTION_NOT_FOUND", errCode(t, err))
	}
}

func TestDecideAlwaysDeliversTheFullStoredRequestID(t *testing.T) {
	store := newFakeStore()
	store.questions["q-perm"] = permissionQuestion()
	store.questions["q-agent"] = agentQuestion()
	service := newTestService(store)
	ctx := context.Background()

	command, err := service.Decide(ctx, DecisionInput{
		QuestionID: "q-perm", Decision: domain.ExecutionPermissionAllow, DecidedBy: "operator",
	})
	if err != nil {
		t.Fatalf("decide: %v", err)
	}
	if command.Type != domain.ExecutionCommandAnswerPermission {
		t.Fatalf("command type = %q, want answer_permission", command.Type)
	}
	var payload domain.ExecutionPermissionPayload
	if err := json.Unmarshal([]byte(store.resolved.PayloadJSON), &payload); err != nil {
		t.Fatalf("decode payload %q: %v", store.resolved.PayloadJSON, err)
	}
	// The full id is what makes the decision land on THIS request. The host
	// rejects a truncated id, and a decision with no id approves every pending
	// request on the agent.
	if payload.RequestID != "perm_2f6c9a4b8e1d0f37a5c2b9e4d8f1a6c3" {
		t.Fatalf("requestId = %q, want the full stored id", payload.RequestID)
	}
	if payload.Decision != domain.ExecutionPermissionAllow {
		t.Fatalf("payload decision = %q", payload.Decision)
	}

	deny, err := service.Decide(ctx, DecisionInput{QuestionID: "q-perm", Decision: domain.ExecutionPermissionDeny})
	if err != nil {
		t.Fatalf("deny: %v", err)
	}
	if deny.Type != domain.ExecutionCommandDenyPermission {
		t.Fatalf("deny command type = %q, want deny_permission", deny.Type)
	}
}

func TestDecideRejectsShortenedIDsAndWrongKinds(t *testing.T) {
	store := newFakeStore()
	store.questions["q-perm"] = permissionQuestion()
	store.questions["q-agent"] = agentQuestion()
	idless := permissionQuestion()
	idless.ID, idless.ExternalID = "q-idless", ""
	store.questions["q-idless"] = idless
	service := newTestService(store)
	ctx := context.Background()

	tests := []struct {
		name string
		code string
		in   DecisionInput
	}{
		{
			// The host's own listing truncates request ids to eight characters, so a
			// UI built on that listing must fail here rather than send a short id.
			name: "truncated confirmation", code: "PERMISSION_ID_MISMATCH",
			in: DecisionInput{QuestionID: "q-perm", Decision: domain.ExecutionPermissionAllow, RequestID: "perm_2f6"},
		},
		{
			name: "no decision", code: "DECISION_INVALID",
			in: DecisionInput{QuestionID: "q-perm"},
		},
		{
			name: "invented decision", code: "DECISION_INVALID",
			in: DecisionInput{QuestionID: "q-perm", Decision: "allow-always"},
		},
		{
			name: "agent question", code: "QUESTION_REQUIRES_ANSWER",
			in: DecisionInput{QuestionID: "q-agent", Decision: domain.ExecutionPermissionAllow},
		},
		{
			// Without a stored id there is nothing safe to send: an empty id is how
			// the host spells "approve everything".
			name: "stored id missing", code: "PERMISSION_ID_MISSING",
			in: DecisionInput{QuestionID: "q-idless", Decision: domain.ExecutionPermissionAllow},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := service.Decide(ctx, test.in); errCode(t, err) != test.code {
				t.Fatalf("code = %q, want %q", errCode(t, err), test.code)
			}
			if store.resolved.QuestionID != "" {
				t.Fatal("a rejected decision must not reach the store")
			}
		})
	}

	// The exact full id is accepted as a confirmation.
	if _, err := service.Decide(ctx, DecisionInput{
		QuestionID: "q-perm", Decision: domain.ExecutionPermissionAllow,
		RequestID: "perm_2f6c9a4b8e1d0f37a5c2b9e4d8f1a6c3",
	}); err != nil {
		t.Fatalf("exact confirmation: %v", err)
	}
}

func TestResolveMapsStorageRefusalsToConflicts(t *testing.T) {
	for name, test := range map[string]struct {
		storeErr error
		code     string
	}{
		"already answered": {domain.ErrExecutionQuestionNotOpen, "QUESTION_NOT_OPEN"},
		"no host":          {domain.ErrSessionNotRemote, "SESSION_NOT_REMOTE"},
	} {
		t.Run(name, func(t *testing.T) {
			store := newFakeStore()
			store.questions["q-agent"] = agentQuestion()
			store.resolveErr = test.storeErr
			_, err := newTestService(store).Answer(context.Background(), AnswerInput{QuestionID: "q-agent", Answer: "yes"})
			if errCode(t, err) != test.code {
				t.Fatalf("code = %q, want %q", errCode(t, err), test.code)
			}
		})
	}
}

func TestListQuestionsReturnsBothSources(t *testing.T) {
	store := newFakeStore()
	store.questions["q-agent"] = agentQuestion()
	store.questions["q-perm"] = permissionQuestion()

	questions, err := newTestService(store).ListQuestions(context.Background())
	if err != nil {
		t.Fatalf("list questions: %v", err)
	}
	sources := map[domain.ExecutionQuestionSource]bool{}
	for _, question := range questions {
		sources[question.Source] = true
	}
	if !sources[domain.ExecutionQuestionAgentEvent] || !sources[domain.ExecutionQuestionPaseoPermission] {
		t.Fatalf("sources = %v, want both kinds in one queue", sources)
	}
}

// TestBindProjectRequiresHostRepoPath pins the field AO cannot infer.
//
// The same repository is /home/u/x on one machine and C:\Projects\X on another,
// which is why the path lives on the binding rather than the project. Accepting
// an empty one would defer the failure to worktree creation on a remote host,
// where it reads as a Paseo error rather than a missing registration.
func TestBindProjectRequiresHostRepoPath(t *testing.T) {
	t.Parallel()
	store := &fakeStore{hosts: []domain.ExecutionHost{{ID: "h1", Name: "h1"}}}
	svc := newTestService(store)

	if _, err := svc.BindProject(context.Background(), BindingInput{
		ProjectID: "p1", HostID: "h1",
	}); err == nil {
		t.Fatal("empty hostRepoPath accepted")
	}

	binding, err := svc.BindProject(context.Background(), BindingInput{
		ProjectID: "p1", HostID: "h1", HostRepoPath: "/srv/p1",
	})
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	if binding.BaseBranch != "main" || binding.Priority != 100 || !binding.Enabled {
		t.Fatalf("defaults not applied: %+v", binding)
	}
}

// TestBindProjectRejectsUnregisteredHost keeps a binding from naming a host that
// does not exist: the router would silently skip it, so dispatch would fail with
// "no eligible host" and give no hint that the host id was simply wrong.
func TestBindProjectRejectsUnregisteredHost(t *testing.T) {
	t.Parallel()
	svc := newTestService(&fakeStore{})
	if _, err := svc.BindProject(context.Background(), BindingInput{
		ProjectID: "p1", HostID: "nope", HostRepoPath: "/srv/p1",
	}); err == nil {
		t.Fatal("binding to an unregistered host accepted")
	}
}
