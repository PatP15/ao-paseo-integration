package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type remoteCapture struct {
	method string
	path   string
	body   []byte
}

func remoteServer(t *testing.T, status int, respBody string) (*httptest.Server, *remoteCapture) {
	t.Helper()
	capture := &remoteCapture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capture.method = r.Method
		capture.path = r.URL.Path
		data, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
		}
		capture.body = data
		if !strings.HasPrefix(r.URL.Path, "/api/v1/execution/") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, respBody)
	}))
	t.Cleanup(srv.Close)
	return srv, capture
}

func TestRemoteHostsRendersLoadAndReachability(t *testing.T) {
	cfg := setConfigEnv(t)
	srv, capture := remoteServer(t, http.StatusOK, `{"hosts":[
		{"id":"worker-1","name":"Linux worker","endpoint":"worker:6780","trustZone":"work",
		 "enabled":true,"maxConcurrentSessions":4,"activeSessions":2,"capabilities":["linux"],"reachable":true},
		{"id":"worker-2","name":"Windows worker","endpoint":"win:6780","trustZone":"hobby",
		 "enabled":true,"maxConcurrentSessions":2,"activeSessions":0,"capabilities":[],
		 "reachable":false,"lastProbeError":"connection refused"}]}`)
	writeRunFileFor(t, cfg, srv)

	out, errOut, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }}, "remote", "hosts")
	if err != nil {
		t.Fatalf("remote hosts: %v\nstderr=%s", err, errOut)
	}
	if capture.method != http.MethodGet || capture.path != "/api/v1/execution/hosts" {
		t.Fatalf("request = %s %s", capture.method, capture.path)
	}
	if !strings.Contains(out, "worker-1") || !strings.Contains(out, "2/4") || !strings.Contains(out, "online") {
		t.Fatalf("out = %q", out)
	}
	// An unreachable host shows why, so the operator is not left guessing whether
	// AO simply stopped looking at it.
	if !strings.Contains(out, "offline") || !strings.Contains(out, "connection refused") {
		t.Fatalf("out = %q, want the failed probe explained", out)
	}
}

func TestRemoteRegisterSendsThePathIDAndAssertsNoMCP(t *testing.T) {
	cfg := setConfigEnv(t)
	srv, capture := remoteServer(t, http.StatusOK,
		`{"host":{"id":"worker-1","endpoint":"worker:6780","trustZone":"work","maxConcurrentSessions":4}}`)
	writeRunFileFor(t, cfg, srv)

	_, errOut, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }},
		"remote", "register", "worker-1",
		"--name", "Linux worker", "--endpoint", "worker:6780", "--trust-zone", "work",
		"--secret-ref", "keychain://worker", "--max-sessions", "4",
		"--capability", "linux", "--capability", "docker")
	if err != nil {
		t.Fatalf("remote register: %v\nstderr=%s", err, errOut)
	}
	if capture.method != http.MethodPut || capture.path != "/api/v1/execution/hosts/worker-1" {
		t.Fatalf("request = %s %s, want PUT /api/v1/execution/hosts/worker-1", capture.method, capture.path)
	}
	var got registerHostRequest
	if err := json.Unmarshal(capture.body, &got); err != nil {
		t.Fatalf("decode request: %v\nbody=%s", err, capture.body)
	}
	// The CLI never offers a way to register a host that permits agent-control tool
	// injection, so the assertion is always sent.
	if !got.RequiresNoMCP || !got.RequiresNoRelay {
		t.Fatalf("request = %#v, want both host constraints asserted", got)
	}
	if got.Endpoint != "worker:6780" || got.TrustZone != "work" || got.MaxConcurrentSessions != 4 {
		t.Fatalf("request = %#v", got)
	}
	if !got.Enabled {
		t.Fatal("a host registered without --disabled is dispatchable")
	}
	if len(got.Capabilities) != 2 || got.Capabilities[0] != "linux" || got.Capabilities[1] != "docker" {
		t.Fatalf("capabilities = %v", got.Capabilities)
	}
}

func TestRemoteRegisterHonoursDisabledAndAllowRelay(t *testing.T) {
	cfg := setConfigEnv(t)
	srv, capture := remoteServer(t, http.StatusOK, `{"host":{"id":"worker-1"}}`)
	writeRunFileFor(t, cfg, srv)

	if _, errOut, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }},
		"remote", "register", "worker-1", "--endpoint", "worker:6780", "--trust-zone", "work",
		"--disabled", "--allow-relay"); err != nil {
		t.Fatalf("remote register: %v\nstderr=%s", err, errOut)
	}
	var got registerHostRequest
	if err := json.Unmarshal(capture.body, &got); err != nil {
		t.Fatalf("decode request: %v\nbody=%s", err, capture.body)
	}
	if got.Enabled || got.RequiresNoRelay {
		t.Fatalf("request = %#v", got)
	}
	if !got.RequiresNoMCP {
		t.Fatal("--allow-relay must not relax the MCP-injection constraint")
	}
}

func TestRemoteDispatchSendsTheDaemonDTOFieldNames(t *testing.T) {
	cfg := setConfigEnv(t)
	srv, capture := remoteServer(t, http.StatusCreated,
		`{"sessionId":"project-1","hostId":"worker-1","workspaceTitle":"ao:project-1:1",
		  "intentId":"intent-1","attempt":1,"commandId":"command-1","commandState":"pending"}`)
	writeRunFileFor(t, cfg, srv)

	out, errOut, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }},
		"remote", "dispatch",
		"--work-item", "work-1", "--project", "project", "--trust-zone", "work",
		"--harness", "codex", "--branch", "ao/work-1", "--provider", "codex",
		"--prompt", "Implement the approved task.", "--capability", "linux")
	if err != nil {
		t.Fatalf("remote dispatch: %v\nstderr=%s", err, errOut)
	}
	if capture.method != http.MethodPost || capture.path != "/api/v1/execution/dispatch" {
		t.Fatalf("request = %s %s", capture.method, capture.path)
	}
	var got dispatchRequest
	if err := json.Unmarshal(capture.body, &got); err != nil {
		t.Fatalf("decode request: %v\nbody=%s", err, capture.body)
	}
	if got.WorkItemID != "work-1" || got.ProjectID != "project" || got.TrustZone != "work" {
		t.Fatalf("request = %#v", got)
	}
	if got.Harness != "codex" || got.Branch != "ao/work-1" || got.Provider != "codex" {
		t.Fatalf("request = %#v", got)
	}
	if len(got.RequiredCapabilities) != 1 || got.RequiredCapabilities[0] != "linux" {
		t.Fatalf("capabilities = %v", got.RequiredCapabilities)
	}
	// The operator never names a host: routing is AO's decision, so the output
	// reports which host was chosen.
	if !strings.Contains(out, "worker-1") || !strings.Contains(out, "pending") {
		t.Fatalf("out = %q", out)
	}
}

func TestRemoteInboxLabelsTheTwoAnswerPaths(t *testing.T) {
	cfg := setConfigEnv(t)
	srv, _ := remoteServer(t, http.StatusOK, `{"questions":[
		{"id":"q-agent","sessionId":"project-1","source":"agent_event","externalId":"event-1",
		 "question":"Rebase or merge?","recommendation":"rebase","options":["rebase","merge"],
		 "createdAt":"2026-08-07T12:00:00Z"},
		{"id":"q-perm","sessionId":"project-1","source":"paseo_permission",
		 "externalId":"perm_2f6c9a4b8e1d0f37a5c2b9e4d8f1a6c3","question":"Allow Bash",
		 "options":[],"createdAt":"2026-08-07T12:01:00Z"}]}`)
	writeRunFileFor(t, cfg, srv)

	out, errOut, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }}, "remote", "inbox")
	if err != nil {
		t.Fatalf("remote inbox: %v\nstderr=%s", err, errOut)
	}
	// The listing says which verb each item takes, because answering a permission
	// request with text cannot release the agent.
	if !strings.Contains(out, "q-agent\tproject-1\tagent_event\tanswer") {
		t.Fatalf("out = %q, want the agent question labelled answerable", out)
	}
	if !strings.Contains(out, "q-perm\tproject-1\tpaseo_permission\tallow / deny") {
		t.Fatalf("out = %q, want the permission labelled decidable", out)
	}
	if !strings.Contains(out, "agent suggests: rebase") {
		t.Fatalf("out = %q", out)
	}
}

func TestRemoteAnswerAndDecisionTargetTheirOwnRoutes(t *testing.T) {
	response := `{"questionId":"q-1","sessionId":"project-1","commandId":"command-1",
	              "commandType":"send_message","commandState":"pending"}`
	tests := []struct {
		name string
		args []string
		path string
		body func(*testing.T, []byte)
	}{
		{
			name: "answer", args: []string{"remote", "answer", "q-1", "rebase", "--by", "operator"},
			path: "/api/v1/execution/questions/q-1/answer",
			body: func(t *testing.T, raw []byte) {
				var got answerQuestionRequest
				if err := json.Unmarshal(raw, &got); err != nil {
					t.Fatalf("decode: %v", err)
				}
				if got.Answer != "rebase" || got.AnsweredBy != "operator" {
					t.Fatalf("request = %#v", got)
				}
			},
		},
		{
			name: "allow", args: []string{"remote", "allow", "q-1", "--note", "reviewed"},
			path: "/api/v1/execution/permissions/q-1/decision",
			body: func(t *testing.T, raw []byte) {
				var got decidePermissionRequest
				if err := json.Unmarshal(raw, &got); err != nil {
					t.Fatalf("decode: %v", err)
				}
				if got.Decision != "allow" || got.Note != "reviewed" {
					t.Fatalf("request = %#v", got)
				}
				// The CLI sends no request id unless the operator passes one: the
				// daemon uses the full id it already stored.
				if got.RequestID != "" {
					t.Fatalf("requestId = %q, want it omitted", got.RequestID)
				}
			},
		},
		{
			name: "deny", args: []string{"remote", "deny", "q-1"},
			path: "/api/v1/execution/permissions/q-1/decision",
			body: func(t *testing.T, raw []byte) {
				var got decidePermissionRequest
				if err := json.Unmarshal(raw, &got); err != nil {
					t.Fatalf("decode: %v", err)
				}
				if got.Decision != "deny" {
					t.Fatalf("request = %#v", got)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := setConfigEnv(t)
			srv, capture := remoteServer(t, http.StatusAccepted, response)
			writeRunFileFor(t, cfg, srv)

			if _, errOut, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }}, test.args...); err != nil {
				t.Fatalf("%s: %v\nstderr=%s", test.name, err, errOut)
			}
			if capture.method != http.MethodPost || capture.path != test.path {
				t.Fatalf("request = %s %s, want POST %s", capture.method, capture.path, test.path)
			}
			test.body(t, capture.body)
		})
	}
}

func TestRemoteDecisionCommandsOfferNoBroaderScope(t *testing.T) {
	// The host enforces one pending request at a time and has no durable per-tool
	// grant, so the CLI must not offer a flag that implies one.
	root := NewRootCommand(Deps{})
	for _, decision := range []string{"allow", "deny"} {
		cmd, _, err := root.Find([]string{"remote", decision})
		if err != nil {
			t.Fatalf("find remote %s: %v", decision, err)
		}
		for _, forbidden := range []string{"all", "always", "scope", "tool", "remember"} {
			if cmd.Flags().Lookup(forbidden) != nil {
				t.Fatalf("remote %s must not expose --%s", decision, forbidden)
			}
		}
		for _, expected := range []string{"note", "by", "request-id"} {
			if cmd.Flags().Lookup(expected) == nil {
				t.Fatalf("remote %s is missing --%s", decision, expected)
			}
		}
	}
}

func TestRemoteAnswerRejectsEmptyInputAsUsage(t *testing.T) {
	setConfigEnv(t)
	for _, args := range [][]string{
		{"remote", "answer", "  ", "text"},
		{"remote", "answer", "q-1", "   "},
		{"remote", "answer", "q-1"},
	} {
		_, _, err := executeCLI(t, Deps{}, args...)
		if err == nil {
			t.Fatalf("%v: want an error", args)
		}
		if got := ExitCode(err); got != 2 {
			t.Fatalf("%v: ExitCode = %d, want 2 (usage)", args, got)
		}
	}
}
