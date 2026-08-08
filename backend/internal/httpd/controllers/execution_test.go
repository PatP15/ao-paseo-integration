package controllers_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/config"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/controllers"
	dispatchsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/dispatch"
	executionsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/execution"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/secretstore"
)

type fakeExecutionService struct {
	hosts      []executionsvc.Host
	questions  []domain.ExecutionInboxQuestion
	registered executionsvc.HostInput
	answered   executionsvc.AnswerInput
	decided    executionsvc.DecisionInput
	err        error
	bound      executionsvc.BindingInput
}

// BindProject records a project's checkout path on a host.
func (f *fakeExecutionService) BindProject(_ context.Context, in executionsvc.BindingInput) (domain.ProjectHostBinding, error) {
	f.bound = in
	return domain.ProjectHostBinding{
		ProjectID: in.ProjectID, HostID: in.HostID, HostRepoPath: in.HostRepoPath,
		BaseBranch: "main", Priority: 100, Enabled: true,
	}, nil
}

var _ controllers.ExecutionService = (*fakeExecutionService)(nil)

func (f *fakeExecutionService) ListHosts(context.Context) ([]executionsvc.Host, error) {
	return f.hosts, f.err
}

func (f *fakeExecutionService) RegisterHost(_ context.Context, in executionsvc.HostInput) (executionsvc.Host, error) {
	f.registered = in
	if f.err != nil {
		return executionsvc.Host{}, f.err
	}
	return executionsvc.Host{
		ExecutionHost: domain.ExecutionHost{
			ID: in.ID, Name: in.Name, BackendType: domain.ExecutionBackendPaseo,
			Transport: in.Transport, Endpoint: in.Endpoint, TrustZone: in.TrustZone,
			Enabled: in.Enabled, MaxConcurrentSessions: in.MaxConcurrentSessions,
			RequiresNoMCP: in.RequiresNoMCP,
		},
		Capabilities: in.Capabilities,
	}, nil
}

func (f *fakeExecutionService) ListQuestions(context.Context) ([]domain.ExecutionInboxQuestion, error) {
	return f.questions, f.err
}

func (f *fakeExecutionService) Answer(_ context.Context, in executionsvc.AnswerInput) (domain.ExecutionCommand, error) {
	f.answered = in
	if f.err != nil {
		return domain.ExecutionCommand{}, f.err
	}
	return domain.ExecutionCommand{
		ID: "command-1", SessionID: "project-1", Type: domain.ExecutionCommandSendMessage,
		State: domain.ExecutionCommandPending,
	}, nil
}

func (f *fakeExecutionService) Decide(_ context.Context, in executionsvc.DecisionInput) (domain.ExecutionCommand, error) {
	f.decided = in
	if f.err != nil {
		return domain.ExecutionCommand{}, f.err
	}
	return domain.ExecutionCommand{
		ID: "command-2", SessionID: "project-1", Type: domain.ExecutionCommandAnswerPermission,
		State: domain.ExecutionCommandPending,
	}, nil
}

type fakeDispatcher struct {
	request dispatchsvc.Request
	err     error
}

var _ controllers.ExecutionDispatcher = (*fakeDispatcher)(nil)

func (f *fakeDispatcher) Dispatch(_ context.Context, req dispatchsvc.Request) (domain.ExecutionDispatch, error) {
	f.request = req
	if f.err != nil {
		return domain.ExecutionDispatch{}, f.err
	}
	return domain.ExecutionDispatch{
		Session: domain.SessionRecord{ID: "project-1"},
		Binding: domain.SessionExecutionBinding{
			HostID: "worker-1", WorkspaceTitle: "ao:project-1:1", IntentID: "intent-1", Attempt: 1,
		},
		Command: domain.ExecutionCommand{ID: "command-1", State: domain.ExecutionCommandPending},
	}, nil
}

func executionServer(t *testing.T, deps httpd.APIDeps) *httptest.Server {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	deps.Sessions = newFakeSessionService()
	srv := httptest.NewServer(httpd.NewRouterWithControl(config.Config{}, log, nil, deps, httpd.ControlDeps{}))
	t.Cleanup(srv.Close)
	return srv
}

func doJSON(t *testing.T, method, url, body string) (*http.Response, []byte) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp, data
}

// TestExecutionRoutesReport501WithoutServices proves the surface stays mounted on
// a daemon that has no execution services wired, answering with the OpenAPI-backed
// 501 rather than 404. A client can then discover the contract from the endpoint.
func TestExecutionRoutesReport501WithoutServices(t *testing.T) {
	srv := executionServer(t, httpd.APIDeps{})
	for _, route := range []struct {
		method, path, body string
	}{
		{http.MethodGet, "/api/v1/execution/hosts", ""},
		{http.MethodPut, "/api/v1/execution/hosts/worker-1", `{}`},
		{http.MethodPost, "/api/v1/execution/dispatch", `{}`},
		{http.MethodGet, "/api/v1/execution/questions", ""},
		{http.MethodPost, "/api/v1/execution/questions/q-1/answer", `{}`},
		{http.MethodPost, "/api/v1/execution/permissions/q-1/decision", `{}`},
		{http.MethodPost, "/api/v1/execution/secrets", `{}`},
		{http.MethodGet, "/api/v1/execution/secrets", ""},
	} {
		resp, body := doJSON(t, route.method, srv.URL+route.path, route.body)
		if resp.StatusCode != http.StatusNotImplemented {
			t.Fatalf("%s %s = %d, want 501", route.method, route.path, resp.StatusCode)
		}
		var envelope struct {
			Code string         `json:"code"`
			Spec map[string]any `json:"spec"`
		}
		if err := json.Unmarshal(body, &envelope); err != nil {
			t.Fatalf("%s %s: decode %q: %v", route.method, route.path, body, err)
		}
		if envelope.Code != "NOT_IMPLEMENTED" || envelope.Spec["operationId"] == nil {
			t.Fatalf("%s %s envelope = %s", route.method, route.path, body)
		}
	}
}

func TestListExecutionHostsSerialisesEmptyListsNotNull(t *testing.T) {
	svc := &fakeExecutionService{}
	srv := executionServer(t, httpd.APIDeps{Execution: svc})

	resp, body := doJSON(t, http.MethodGet, srv.URL+"/api/v1/execution/hosts", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), `"hosts":[]`) {
		t.Fatalf("body = %s, want an empty array", body)
	}

	svc.hosts = []executionsvc.Host{{
		ExecutionHost:  domain.ExecutionHost{ID: "worker-1", Name: "worker", Endpoint: "worker:6780"},
		ActiveSessions: 1, Reachable: true,
	}}
	_, body = doJSON(t, http.MethodGet, srv.URL+"/api/v1/execution/hosts", "")
	var out controllers.ListExecutionHostsResponse
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode %q: %v", body, err)
	}
	if len(out.Hosts) != 1 || out.Hosts[0].ID != "worker-1" || !out.Hosts[0].Reachable {
		t.Fatalf("hosts = %#v", out.Hosts)
	}
	if out.Hosts[0].Capabilities == nil {
		t.Fatal("capabilities must be an empty array, never null")
	}
}

func TestRegisterExecutionHostPassesTheBodyThroughAndRejectsUnknownFields(t *testing.T) {
	svc := &fakeExecutionService{}
	srv := executionServer(t, httpd.APIDeps{Execution: svc})

	body := `{"name":"worker","transport":"tailscale","endpoint":"worker:6780",` +
		`"endpointSecretRef":"keychain://worker","trustZone":"work","enabled":true,` +
		`"maxConcurrentSessions":4,"requiresNoMcp":true,"requiresNoRelay":true,"capabilities":["linux"]}`
	resp, got := doJSON(t, http.MethodPut, srv.URL+"/api/v1/execution/hosts/worker-1", body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, got)
	}
	if svc.registered.ID != "worker-1" {
		t.Fatalf("host id = %q, want it taken from the path", svc.registered.ID)
	}
	if svc.registered.Endpoint != "worker:6780" || svc.registered.TrustZone != domain.ExecutionTrustZoneWork {
		t.Fatalf("registered = %#v", svc.registered)
	}
	if !svc.registered.RequiresNoMCP || svc.registered.MaxConcurrentSessions != 4 {
		t.Fatalf("registered = %#v", svc.registered)
	}

	// A field the daemon does not know is a constraint the operator believes they
	// set. Dropping it silently would register a weaker host than they asked for.
	resp, _ = doJSON(t, http.MethodPut, srv.URL+"/api/v1/execution/hosts/worker-1",
		`{"name":"worker","endpoint":"worker:6780","requiresNoMcpInjection":true}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown field status = %d, want 400", resp.StatusCode)
	}
}

func TestDispatchValidatesBeforeCommitting(t *testing.T) {
	valid := map[string]any{
		"workItemId": "work-1", "projectId": "project", "trustZone": "work",
		"harness": "codex", "branch": "ao/work-1", "provider": "codex",
		"prompt": "Implement the approved task.",
	}
	tests := []struct {
		name string
		code string
		edit func(map[string]any)
	}{
		{name: "no work item", code: "WORK_ITEM_ID_REQUIRED", edit: func(m map[string]any) { m["workItemId"] = " " }},
		{name: "no prompt", code: "PROMPT_REQUIRED", edit: func(m map[string]any) { delete(m, "prompt") }},
		{name: "unknown trust zone", code: "TRUST_ZONE_INVALID", edit: func(m map[string]any) { m["trustZone"] = "personal" }},
		{
			// A harness AO does not ship would be rejected by the sessions table
			// mid-dispatch; the API is the honest place to say no.
			name: "unknown harness", code: "HARNESS_INVALID",
			edit: func(m map[string]any) { m["harness"] = "paseo" },
		},
		{
			// Provider, model, mode, and branch each become one argv element for the
			// remote CLI. A leading dash would be parsed as a flag.
			name: "provider that is a flag", code: "PROVIDER_INVALID",
			edit: func(m map[string]any) { m["provider"] = "--dangerously-skip-permissions" },
		},
		{
			name: "branch with whitespace", code: "BRANCH_INVALID",
			edit: func(m map[string]any) { m["branch"] = "ao/work 1" },
		},
		{
			name: "empty capability", code: "CAPABILITY_INVALID",
			edit: func(m map[string]any) { m["requiredCapabilities"] = []string{"linux", ""} },
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dispatcher := &fakeDispatcher{}
			srv := executionServer(t, httpd.APIDeps{ExecutionDispatch: dispatcher})
			payload := map[string]any{}
			for key, value := range valid {
				payload[key] = value
			}
			test.edit(payload)
			encoded, err := json.Marshal(payload)
			if err != nil {
				t.Fatal(err)
			}
			resp, body := doJSON(t, http.MethodPost, srv.URL+"/api/v1/execution/dispatch", string(encoded))
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body %s)", resp.StatusCode, body)
			}
			var envelope struct{ Code string }
			if err := json.Unmarshal(body, &envelope); err != nil {
				t.Fatalf("decode %q: %v", body, err)
			}
			if envelope.Code != test.code {
				t.Fatalf("code = %q, want %q", envelope.Code, test.code)
			}
			if dispatcher.request.WorkItemID != "" {
				t.Fatal("a rejected dispatch must not reach the service")
			}
		})
	}

	dispatcher := &fakeDispatcher{}
	srv := executionServer(t, httpd.APIDeps{ExecutionDispatch: dispatcher})
	encoded, err := json.Marshal(valid)
	if err != nil {
		t.Fatal(err)
	}
	resp, body := doJSON(t, http.MethodPost, srv.URL+"/api/v1/execution/dispatch", string(encoded))
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}
	var out controllers.DispatchExecutionResponse
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode %q: %v", body, err)
	}
	if out.SessionID != "project-1" || out.HostID != "worker-1" || out.CommandID != "command-1" {
		t.Fatalf("response = %#v", out)
	}
	// Nothing remote exists yet; the caller is told only that a command is queued.
	if out.CommandState != domain.ExecutionCommandPending {
		t.Fatalf("commandState = %q, want pending", out.CommandState)
	}
	if dispatcher.request.Harness != domain.HarnessCodex {
		t.Fatalf("dispatched request = %#v", dispatcher.request)
	}
}

func TestListExecutionQuestionsExposesBothKinds(t *testing.T) {
	svc := &fakeExecutionService{questions: []domain.ExecutionInboxQuestion{
		{
			ID: "q-agent", SessionID: "project-1", Source: domain.ExecutionQuestionAgentEvent,
			ExternalID: "event-1", Question: "Rebase or merge?", Recommendation: "rebase",
			Options: []string{"rebase", "merge"}, CreatedAt: time.Now().UTC(),
		},
		{
			ID: "q-perm", SessionID: "project-1", Source: domain.ExecutionQuestionPaseoPermission,
			ExternalID: "perm_2f6c9a4b8e1d0f37a5c2b9e4d8f1a6c3", Question: "Allow Bash",
			CreatedAt: time.Now().UTC(),
		},
	}}
	srv := executionServer(t, httpd.APIDeps{Execution: svc})

	resp, body := doJSON(t, http.MethodGet, srv.URL+"/api/v1/execution/questions", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}
	var out controllers.ListExecutionQuestionsResponse
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode %q: %v", body, err)
	}
	if len(out.Questions) != 2 {
		t.Fatalf("questions = %d, want 2", len(out.Questions))
	}
	// The permission's full request id is served whole: a client that displayed a
	// prefix and echoed it back would be rejected by the decision endpoint.
	if out.Questions[1].ExternalID != "perm_2f6c9a4b8e1d0f37a5c2b9e4d8f1a6c3" {
		t.Fatalf("externalId = %q", out.Questions[1].ExternalID)
	}
	if out.Questions[1].Options == nil {
		t.Fatal("options must be an empty array, never null")
	}
}

func TestAnswerAndDecideForwardIdentityAndAccept(t *testing.T) {
	svc := &fakeExecutionService{}
	srv := executionServer(t, httpd.APIDeps{Execution: svc})

	resp, body := doJSON(t, http.MethodPost, srv.URL+"/api/v1/execution/questions/q-agent/answer",
		`{"answer":"rebase","answeredBy":"operator"}`)
	// Accepted, not OK: the answer is durable but not yet delivered to the host.
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("answer status = %d, body = %s", resp.StatusCode, body)
	}
	if svc.answered.QuestionID != "q-agent" || svc.answered.Answer != "rebase" || svc.answered.AnsweredBy != "operator" {
		t.Fatalf("answered = %#v", svc.answered)
	}
	var decision controllers.ExecutionDecisionResponse
	if err := json.Unmarshal(body, &decision); err != nil {
		t.Fatalf("decode %q: %v", body, err)
	}
	if decision.CommandType != domain.ExecutionCommandSendMessage || decision.QuestionID != "q-agent" {
		t.Fatalf("decision = %#v", decision)
	}

	resp, body = doJSON(t, http.MethodPost, srv.URL+"/api/v1/execution/permissions/q-perm/decision",
		`{"decision":"allow","requestId":"perm_2f6c9a4b8e1d0f37a5c2b9e4d8f1a6c3","by":"x"}`)
	// "by" is not a field: the decision vocabulary is closed, so an unknown key is
	// a 400 rather than a silently narrower decision.
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown field status = %d, want 400 (body %s)", resp.StatusCode, body)
	}

	resp, body = doJSON(t, http.MethodPost, srv.URL+"/api/v1/execution/permissions/q-perm/decision",
		`{"decision":"allow","requestId":"perm_2f6c9a4b8e1d0f37a5c2b9e4d8f1a6c3","decidedBy":"operator"}`)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("decision status = %d, body = %s", resp.StatusCode, body)
	}
	if svc.decided.Decision != domain.ExecutionPermissionAllow || svc.decided.QuestionID != "q-perm" {
		t.Fatalf("decided = %#v", svc.decided)
	}
	if svc.decided.RequestID != "perm_2f6c9a4b8e1d0f37a5c2b9e4d8f1a6c3" {
		t.Fatalf("decided requestId = %q, want the full id forwarded verbatim", svc.decided.RequestID)
	}
}

// TestPermissionDecisionRefusesAScopeWiderThanTheHostEnforces pins the one wire
// rule the UI cannot be trusted to keep: there is no durable per-tool grant on
// the host, so any attempt to express one is refused rather than downgraded to a
// single-request allow under a wider-sounding name.
func TestPermissionDecisionRefusesAScopeWiderThanTheHostEnforces(t *testing.T) {
	svc := &fakeExecutionService{}
	srv := executionServer(t, httpd.APIDeps{Execution: svc})

	for _, body := range []string{
		`{"decision":"allow","scope":"always"}`,
		`{"decision":"allow","tool":"Bash","remember":true}`,
		`{"decision":"allow","all":true}`,
	} {
		resp, got := doJSON(t, http.MethodPost, srv.URL+"/api/v1/execution/permissions/q-perm/decision", body)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("%s = %d, want 400 (body %s)", body, resp.StatusCode, got)
		}
		if svc.decided.QuestionID != "" {
			t.Fatalf("%s reached the service", body)
		}
	}
}

func TestExecutionErrorsUseTheLockedEnvelope(t *testing.T) {
	svc := &fakeExecutionService{err: apierr.Conflict("QUESTION_NOT_OPEN", "already answered", nil)}
	srv := executionServer(t, httpd.APIDeps{Execution: svc})

	resp, body := doJSON(t, http.MethodPost, srv.URL+"/api/v1/execution/questions/q-1/answer", `{"answer":"yes"}`)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}
	var envelope struct {
		Error string `json:"error"`
		Code  string `json:"code"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("decode %q: %v", body, err)
	}
	if envelope.Error != "conflict" || envelope.Code != "QUESTION_NOT_OPEN" {
		t.Fatalf("envelope = %s", body)
	}
}

// TestExecutionSecretsRoundTrip drives the real store through the HTTP surface:
// create returns the ref, a duplicate is refused without replace, and the list
// carries names only — the value appears in no response at any point.
func TestExecutionSecretsRoundTrip(t *testing.T) {
	srv := executionServer(t, httpd.APIDeps{ExecutionSecrets: secretstore.New(t.TempDir())})

	resp, body := doJSON(t, http.MethodPost, srv.URL+"/api/v1/execution/secrets",
		`{"name":"worker-pw","value":"hunter2"}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d, body = %s", resp.StatusCode, body)
	}
	var created struct {
		Ref string `json:"ref"`
	}
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatalf("decode %q: %v", body, err)
	}
	if created.Ref != "worker-pw" {
		t.Fatalf("ref = %q, want worker-pw", created.Ref)
	}
	if strings.Contains(string(body), "hunter2") {
		t.Fatalf("create response leaked the value: %s", body)
	}

	resp, body = doJSON(t, http.MethodPost, srv.URL+"/api/v1/execution/secrets",
		`{"name":"worker-pw","value":"other"}`)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("duplicate status = %d, body = %s", resp.StatusCode, body)
	}

	resp, body = doJSON(t, http.MethodPost, srv.URL+"/api/v1/execution/secrets",
		`{"name":"worker-pw","value":"rotated","replace":true}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("replace status = %d, body = %s", resp.StatusCode, body)
	}

	resp, body = doJSON(t, http.MethodGet, srv.URL+"/api/v1/execution/secrets", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list status = %d, body = %s", resp.StatusCode, body)
	}
	var listed struct {
		Refs []string `json:"refs"`
	}
	if err := json.Unmarshal(body, &listed); err != nil {
		t.Fatalf("decode %q: %v", body, err)
	}
	if len(listed.Refs) != 1 || listed.Refs[0] != "worker-pw" {
		t.Fatalf("refs = %v, want [worker-pw]", listed.Refs)
	}
	if strings.Contains(string(body), "rotated") {
		t.Fatalf("list response leaked the value: %s", body)
	}
}

// TestExecutionSecretsRejectUnknownKeysAndBadNames pins strict decoding and the
// bare-name rule at the HTTP boundary.
func TestExecutionSecretsRejectUnknownKeysAndBadNames(t *testing.T) {
	srv := executionServer(t, httpd.APIDeps{ExecutionSecrets: secretstore.New(t.TempDir())})

	resp, body := doJSON(t, http.MethodPost, srv.URL+"/api/v1/execution/secrets",
		`{"name":"pw","value":"v","endpoint":"sneaky:1"}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown key status = %d, body = %s", resp.StatusCode, body)
	}

	resp, body = doJSON(t, http.MethodPost, srv.URL+"/api/v1/execution/secrets",
		`{"name":"../escape","value":"v"}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("path-shaped name status = %d, body = %s", resp.StatusCode, body)
	}
	var envelope struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("decode %q: %v", body, err)
	}
	if envelope.Code != "SECRET_NAME_INVALID" {
		t.Fatalf("code = %q, want SECRET_NAME_INVALID", envelope.Code)
	}
}
