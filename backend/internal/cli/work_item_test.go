package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/runfile"
)

type workItemCLICapture struct {
	method string
	path   string
	query  string
	body   []byte
}

func workItemCLIDeps(t *testing.T, status int, response string) (Deps, *workItemCLICapture) {
	t.Helper()
	cfg := setConfigEnv(t)
	if err := runfile.Write(cfg.runFile, runfile.Info{PID: os.Getpid(), Port: 3001, StartedAt: time.Unix(100, 0).UTC()}); err != nil {
		t.Fatal(err)
	}
	capture := &workItemCLICapture{}
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == "/internal/telemetry/cli-invoked" {
			return jsonResponse(http.StatusAccepted, ""), nil
		}
		capture.method, capture.path, capture.query = req.Method, req.URL.Path, req.URL.RawQuery
		body, err := io.ReadAll(req.Body)
		if err != nil {
			t.Fatal(err)
		}
		capture.body = body
		return jsonResponse(status, response), nil
	})}
	return Deps{HTTPClient: client, ProcessAlive: func(int) bool { return true }}, capture
}

func TestWorkItemAddCallsCreateRouteAsDraftSurface(t *testing.T) {
	deps, capture := workItemCLIDeps(t, http.StatusCreated, `{"workItem":{"id":"wi_1","projectId":"project","title":"Ship G1","body":"","acceptanceCriteria":[],"allowedScope":[],"excludedScope":[],"riskLevel":"normal","approvalState":"draft","lifecycleFact":"open","priority":100,"createdByType":"human","createdAt":"2026-08-08T03:00:00Z","updatedAt":"2026-08-08T03:00:00Z"}}`)
	out, errOut, err := executeCLI(t, deps, "work-item", "add", "--project", "project", "--title", "Ship G1", "--acceptance", "CLI works")
	if err != nil {
		t.Fatalf("add: %v\nstderr=%s", err, errOut)
	}
	if capture.method != http.MethodPost || capture.path != "/api/v1/work-items" {
		t.Fatalf("request = %s %s", capture.method, capture.path)
	}
	var req createWorkItemRequest
	if err := json.Unmarshal(capture.body, &req); err != nil {
		t.Fatal(err)
	}
	if req.ProjectID != "project" || req.Title != "Ship G1" || len(req.AcceptanceCriteria) != 1 {
		t.Fatalf("request = %#v", req)
	}
	if !strings.Contains(out, "Created work item wi_1 (draft)") {
		t.Fatalf("out = %q", out)
	}
}

func TestWorkItemApproveAndListTargetCanonicalRoutes(t *testing.T) {
	t.Run("approve", func(t *testing.T) {
		deps, capture := workItemCLIDeps(t, http.StatusOK, `{"workItem":{"id":"wi_1","projectId":"project","title":"Ship G1","body":"","acceptanceCriteria":[],"allowedScope":[],"excludedScope":[],"riskLevel":"normal","approvalState":"approved","lifecycleFact":"open","priority":100,"createdByType":"human","approvedBy":"pat","approvedAt":"2026-08-08T03:01:00Z","createdAt":"2026-08-08T03:00:00Z","updatedAt":"2026-08-08T03:01:00Z"}}`)
		out, errOut, err := executeCLI(t, deps, "work-item", "approve", "wi_1", "--by", "pat")
		if err != nil {
			t.Fatalf("approve: %v\nstderr=%s", err, errOut)
		}
		if capture.method != http.MethodPost || capture.path != "/api/v1/work-items/wi_1/approval" || !strings.Contains(string(capture.body), `"approver":"pat"`) {
			t.Fatalf("request = %s %s %s", capture.method, capture.path, capture.body)
		}
		if !strings.Contains(out, "Approved work item wi_1 by pat") {
			t.Fatalf("out = %q", out)
		}
	})

	t.Run("list", func(t *testing.T) {
		deps, capture := workItemCLIDeps(t, http.StatusOK, `{"workItems":[]}`)
		if _, errOut, err := executeCLI(t, deps, "work-item", "ls", "--project", "project with space"); err != nil {
			t.Fatalf("list: %v\nstderr=%s", err, errOut)
		}
		if capture.method != http.MethodGet || capture.path != "/api/v1/work-items" || capture.query != "projectId=project+with+space" {
			t.Fatalf("request = %s %s?%s", capture.method, capture.path, capture.query)
		}
	})
}

func TestWorkItemCommandsRejectMissingRequiredInputAsUsage(t *testing.T) {
	for _, args := range [][]string{
		{"work-item", "add", "--title", "title"},
		{"work-item", "add", "--project", "project"},
		{"work-item", "approve", " "},
		{"work-item", "ls"},
	} {
		_, _, err := executeCLI(t, Deps{}, args...)
		if ExitCode(err) != 2 {
			t.Errorf("%v: ExitCode(%v) = %d, want 2", args, err, ExitCode(err))
		}
	}
}
