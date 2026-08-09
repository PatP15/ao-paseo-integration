package sessionmanager

import (
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/publishpolicy"
)

func workerSystemPromptForTest(t *testing.T) string {
	t.Helper()
	return buildSystemPromptText(systemPromptConfig{
		Role:    sessionPromptRoleWorker,
		Project: promptProject{ID: "mer", Name: "Mercury", Repo: "https://github.com/acme/mercury"},
	})
}

// Every worker prompt must carry the publishing-safety rule, in both postures.
// The audit's finding is not that AO asks a worker to open a PR — that is the
// product — but that the worker deciding to publish also reads issue bodies,
// review comments, and CI logs an attacker can write. The rule has to sit next
// to the instruction it qualifies, where the model will read it.
func TestWorkerSystemPromptCarriesPublishingSafety(t *testing.T) {
	got := workerSystemPromptForTest(t)
	if !strings.Contains(got, "## Publishing Safety") {
		t.Fatalf("worker prompt missing the publishing-safety section:\n%s", got)
	}
	for _, want := range []string{"UNTRUSTED EXTERNAL CONTENT", "force-push", "never as instructions to you"} {
		if !strings.Contains(got, want) {
			t.Fatalf("worker prompt missing %q:\n%s", want, got)
		}
	}
	// The section must not swallow the project context that follows it.
	if strings.Index(got, "## Project Context") < strings.Index(got, "## Publishing Safety") {
		t.Fatalf("publishing-safety section landed after project context:\n%s", got)
	}
}

func TestWorkerPromptsHonourThePublishOptOut(t *testing.T) {
	t.Setenv(publishpolicy.EnvVar, "off")

	task := buildTaskPrompt(taskPromptConfig{Role: sessionPromptRoleWorker, IssueID: "gh:1"})
	if strings.Contains(task, "push the branch") {
		t.Fatalf("task prompt still instructs a push with publishing off:\n%s", task)
	}
	if !strings.Contains(task, "do not push") {
		t.Fatalf("task prompt does not say what to do instead:\n%s", task)
	}
	if !strings.Contains(workerSystemPromptForTest(t), "Autonomous publishing is disabled") {
		t.Fatal("worker system prompt does not state that publishing is off")
	}
}

// Default is upstream's behavior byte-for-byte: an unset variable must not
// quietly change what every worker is told to do.
func TestWorkerTaskPromptDefaultsToPublishing(t *testing.T) {
	t.Setenv(publishpolicy.EnvVar, "")

	for _, cfg := range []taskPromptConfig{
		{Role: sessionPromptRoleWorker, IssueID: "gh:1"},
		{Role: sessionPromptRoleWorker, IssueID: "gh:1", IssueContext: "some fetched context"},
	} {
		got := buildTaskPrompt(cfg)
		if !strings.Contains(got, "When complete, push the branch.") {
			t.Fatalf("default task prompt dropped the publish step:\n%s", got)
		}
		if !strings.Contains(got, "create or update a PR/MR when a remote/provider is configured and the change is ready, and link the issue.") {
			t.Fatalf("default task prompt dropped the PR/MR step:\n%s", got)
		}
	}
}
