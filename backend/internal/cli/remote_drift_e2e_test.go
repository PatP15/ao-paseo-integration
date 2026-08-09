package cli

// remote_drift_e2e_test.go is the DTO-drift guard for `ao remote`.
//
// The CLI hand-mirrors the daemon's execution request and response shapes
// (remote.go) rather than importing the controller package, matching the
// deliberate manual boundary the rest of this package keeps. Nothing else proves
// the two sides agree on JSON field names: a renamed tag on either side compiles
// fine and then silently sends an empty host endpoint, or renders a blank command
// id, at runtime.
//
// So this stands up the REAL router and REAL controllers, with a fake only BELOW
// the controller at the service layer, and drives the actual commands over a real
// loopback round trip in both directions — request fields must arrive, and
// response fields must come back out in the rendered output.

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/config"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd"
	dispatchsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/dispatch"
	executionsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/execution"
)

// captureExecutionService records what the real controller decoded from the real
// CLI request body, and returns fully-populated responses so the CLI's decode
// side is exercised too.
type captureExecutionService struct {
	registered executionsvc.HostInput
	answered   executionsvc.AnswerInput
	decided    executionsvc.DecisionInput
	bound      executionsvc.BindingInput
}

// BindProject records where a project is checked out on a host. The drift test
// exists to catch exactly this: a CLI command whose request shape has drifted
// from the controller's, which no unit test on either side would notice.
func (c *captureExecutionService) BindProject(_ context.Context, in executionsvc.BindingInput) (domain.ProjectHostBinding, error) {
	c.bound = in
	return domain.ProjectHostBinding{
		ProjectID: in.ProjectID, HostID: in.HostID, HostRepoPath: in.HostRepoPath,
		BaseBranch: "main", Priority: 100, Enabled: true,
	}, nil
}

func (c *captureExecutionService) ProbeHost(_ context.Context, id domain.ExecutionHostID) (executionsvc.Host, error) {
	return executionsvc.Host{ExecutionHost: domain.ExecutionHost{ID: id}}, nil
}

func (c *captureExecutionService) HostProviders(context.Context, domain.ExecutionHostID) ([]domain.ExecutionHostProvider, error) {
	return nil, nil
}

func (c *captureExecutionService) ListSessionEvents(context.Context, executionsvc.EventsFilter) ([]domain.ExecutionEventRecord, error) {
	return nil, nil
}

func (c *captureExecutionService) ListBindings(context.Context, executionsvc.BindingFilter) ([]domain.ProjectHostBinding, error) {
	return nil, nil
}

func (c *captureExecutionService) GetCommand(_ context.Context, id string) (domain.ExecutionCommand, error) {
	return domain.ExecutionCommand{ID: id}, nil
}

func (c *captureExecutionService) ListHosts(context.Context) ([]executionsvc.Host, error) {
	return []executionsvc.Host{{
		ExecutionHost: domain.ExecutionHost{
			ID: "worker-1", Name: "Linux worker", Endpoint: "worker.ts.net:6780",
			TrustZone: domain.ExecutionTrustZoneWork, Enabled: true, MaxConcurrentSessions: 4,
			ServerID: "srv_abc", PaseoVersion: "0.2.5", LastProbeError: "connection refused",
		},
		Capabilities: []string{"linux"}, ActiveSessions: 3, Reachable: false,
	}}, nil
}

func (c *captureExecutionService) RegisterHost(_ context.Context, in executionsvc.HostInput) (executionsvc.Host, error) {
	c.registered = in
	return executionsvc.Host{
		ExecutionHost: domain.ExecutionHost{
			ID: in.ID, Name: in.Name, Endpoint: in.Endpoint, TrustZone: in.TrustZone,
			MaxConcurrentSessions: in.MaxConcurrentSessions,
		},
		Capabilities: in.Capabilities,
	}, nil
}

func (c *captureExecutionService) ListQuestions(context.Context) ([]domain.ExecutionInboxQuestion, error) {
	return []domain.ExecutionInboxQuestion{{
		ID: "q-perm", SessionID: "project-1", Source: domain.ExecutionQuestionPaseoPermission,
		ExternalID: "perm_2f6c9a4b8e1d0f37a5c2b9e4d8f1a6c3", Question: "Allow Bash: git push",
		Recommendation: "deny", CreatedAt: time.Unix(1000, 0).UTC(),
	}}, nil
}

func (c *captureExecutionService) Answer(_ context.Context, in executionsvc.AnswerInput) (domain.ExecutionCommand, error) {
	c.answered = in
	return domain.ExecutionCommand{
		ID: "command-answer", SessionID: "project-1", Type: domain.ExecutionCommandSendMessage,
		State: domain.ExecutionCommandPending,
	}, nil
}

func (c *captureExecutionService) Decide(_ context.Context, in executionsvc.DecisionInput) (domain.ExecutionCommand, error) {
	c.decided = in
	return domain.ExecutionCommand{
		ID: "command-decision", SessionID: "project-1", Type: domain.ExecutionCommandDenyPermission,
		State: domain.ExecutionCommandPending,
	}, nil
}

type captureDispatcher struct{ request dispatchsvc.Request }

func (c *captureDispatcher) Dispatch(_ context.Context, req dispatchsvc.Request) (domain.ExecutionDispatch, error) {
	c.request = req
	return domain.ExecutionDispatch{
		Session: domain.SessionRecord{ID: "project-7"},
		Binding: domain.SessionExecutionBinding{
			HostID: "worker-1", WorkspaceTitle: "ao:project-7:1", IntentID: "intent-9", Attempt: 1,
		},
		Command: domain.ExecutionCommand{ID: "command-start", State: domain.ExecutionCommandPending},
	}, nil
}

func startRemoteDaemon(t *testing.T, deps httpd.APIDeps) testConfig {
	t.Helper()
	cfg := setConfigEnv(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	deps.Sessions = &fakeSessionService{}
	srv := httptest.NewServer(httpd.NewRouterWithControl(config.Config{}, log, nil, deps, httpd.ControlDeps{}))
	t.Cleanup(srv.Close)
	writeRunFileFor(t, cfg, srv)
	return cfg
}

func TestRemoteRegisterDTOsMatchTheDaemon(t *testing.T) {
	svc := &captureExecutionService{}
	startRemoteDaemon(t, httpd.APIDeps{Execution: svc})

	out, errOut, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }},
		"remote", "register", "worker-1",
		"--name", "Linux worker", "--transport", "tailscale", "--endpoint", "worker.ts.net:6780",
		"--secret-ref", "keychain://worker", "--trust-zone", "work", "--max-sessions", "6",
		"--capability", "linux", "--capability", "docker")
	if err != nil {
		t.Fatalf("remote register: %v\nstderr=%s", err, errOut)
	}
	got := svc.registered
	if got.ID != "worker-1" || got.Name != "Linux worker" || got.Endpoint != "worker.ts.net:6780" {
		t.Fatalf("registered = %#v", got)
	}
	if got.Transport != domain.ExecutionTransportTailscale || got.TrustZone != domain.ExecutionTrustZoneWork {
		t.Fatalf("registered = %#v", got)
	}
	if got.EndpointSecretRef != "keychain://worker" || got.MaxConcurrentSessions != 6 {
		t.Fatalf("registered = %#v", got)
	}
	if !got.Enabled || !got.RequiresNoMCP || !got.RequiresNoRelay {
		t.Fatalf("registered = %#v", got)
	}
	if len(got.Capabilities) != 2 {
		t.Fatalf("capabilities = %v", got.Capabilities)
	}
	// Response side: these values exist only in the daemon's reply.
	if !strings.Contains(out, "worker-1") || !strings.Contains(out, "worker.ts.net:6780") ||
		!strings.Contains(out, "max 6 sessions") {
		t.Fatalf("out = %q", out)
	}
}

func TestRemoteHostsAndInboxDecodeTheDaemonResponses(t *testing.T) {
	startRemoteDaemon(t, httpd.APIDeps{Execution: &captureExecutionService{}})
	deps := Deps{ProcessAlive: func(int) bool { return true }}

	out, errOut, err := executeCLI(t, deps, "remote", "hosts")
	if err != nil {
		t.Fatalf("remote hosts: %v\nstderr=%s", err, errOut)
	}
	for _, want := range []string{"worker-1", "Linux worker", "worker.ts.net:6780", "work", "3/4", "offline", "connection refused"} {
		if !strings.Contains(out, want) {
			t.Fatalf("out = %q, missing %q", out, want)
		}
	}

	out, errOut, err = executeCLI(t, deps, "remote", "inbox")
	if err != nil {
		t.Fatalf("remote inbox: %v\nstderr=%s", err, errOut)
	}
	for _, want := range []string{"q-perm", "project-1", "paseo_permission", "allow / deny", "Allow Bash: git push", "agent suggests: deny"} {
		if !strings.Contains(out, want) {
			t.Fatalf("out = %q, missing %q", out, want)
		}
	}
}

func TestRemoteDispatchDTOsMatchTheDaemon(t *testing.T) {
	dispatcher := &captureDispatcher{}
	startRemoteDaemon(t, httpd.APIDeps{ExecutionDispatch: dispatcher})

	out, errOut, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }},
		"remote", "dispatch",
		"--work-item", "work-1", "--project", "project", "--trust-zone", "work",
		"--issue", "issue-4", "--harness", "codex", "--name", "Implement work",
		"--branch", "ao/work-1", "--provider", "codex", "--model", "gpt-test", "--mode", "auto",
		"--prompt", "Implement the approved task.", "--capability", "linux")
	if err != nil {
		t.Fatalf("remote dispatch: %v\nstderr=%s", err, errOut)
	}
	got := dispatcher.request
	if got.WorkItemID != "work-1" || got.ProjectID != "project" || got.TrustZone != domain.ExecutionTrustZoneWork {
		t.Fatalf("dispatched = %#v", got)
	}
	if got.IssueID != "issue-4" || got.Harness != domain.HarnessCodex || got.DisplayName != "Implement work" {
		t.Fatalf("dispatched = %#v", got)
	}
	if got.Branch != "ao/work-1" || got.Provider != "codex" || got.Model != "gpt-test" || got.Mode != "auto" {
		t.Fatalf("dispatched = %#v", got)
	}
	if got.Prompt != "Implement the approved task." {
		t.Fatalf("dispatched prompt = %q", got.Prompt)
	}
	if len(got.RequiredCapabilities) != 1 || got.RequiredCapabilities[0] != "linux" {
		t.Fatalf("capabilities = %v", got.RequiredCapabilities)
	}
	for _, want := range []string{"project-7", "worker-1", "command-start", "pending"} {
		if !strings.Contains(out, want) {
			t.Fatalf("out = %q, missing %q", out, want)
		}
	}
}

func TestRemoteAnswerAndDecideDTOsMatchTheDaemon(t *testing.T) {
	svc := &captureExecutionService{}
	startRemoteDaemon(t, httpd.APIDeps{Execution: svc})
	deps := Deps{ProcessAlive: func(int) bool { return true }}

	out, errOut, err := executeCLI(t, deps, "remote", "answer", "q-agent", "rebase", "--by", "operator")
	if err != nil {
		t.Fatalf("remote answer: %v\nstderr=%s", err, errOut)
	}
	if svc.answered.QuestionID != "q-agent" || svc.answered.Answer != "rebase" || svc.answered.AnsweredBy != "operator" {
		t.Fatalf("answered = %#v", svc.answered)
	}
	if !strings.Contains(out, "command-answer") || !strings.Contains(out, "project-1") {
		t.Fatalf("out = %q", out)
	}

	out, errOut, err = executeCLI(t, deps, "remote", "deny", "q-perm",
		"--note", "pushes to main", "--by", "operator",
		"--request-id", "perm_2f6c9a4b8e1d0f37a5c2b9e4d8f1a6c3")
	if err != nil {
		t.Fatalf("remote deny: %v\nstderr=%s", err, errOut)
	}
	if svc.decided.QuestionID != "q-perm" || svc.decided.Decision != domain.ExecutionPermissionDeny {
		t.Fatalf("decided = %#v", svc.decided)
	}
	if svc.decided.Note != "pushes to main" || svc.decided.DecidedBy != "operator" {
		t.Fatalf("decided = %#v", svc.decided)
	}
	// The confirmation id must survive the round trip byte-exact: the daemon
	// rejects anything but the full host request id, and the host rejects a
	// truncated one.
	if svc.decided.RequestID != "perm_2f6c9a4b8e1d0f37a5c2b9e4d8f1a6c3" {
		t.Fatalf("decided requestId = %q", svc.decided.RequestID)
	}
	if !strings.Contains(out, "command-decision") {
		t.Fatalf("out = %q", out)
	}
}
