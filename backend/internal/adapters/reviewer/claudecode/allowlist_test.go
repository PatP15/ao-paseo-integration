package claudecode

import (
	"strings"
	"testing"
)

// The reviewer runs headless with no human to approve a prompt, so its
// allowlist is the whole of its authority: anything not listed cannot run.
// Upstream lists Bash(gh:*), which auto-approves the entire forge CLI under a
// token that reaches every repository the operator can see — while the
// reviewer's only inputs are a diff and PR context that anyone who can comment
// on the PR helped write.

func TestReviewerNeverAllowsTheWholeGhCLI(t *testing.T) {
	for _, tool := range reviewerAllowedTools {
		if tool == "Bash(gh:*)" || tool == "Bash(gh)" {
			t.Fatalf("reviewer allowlist re-widened to the whole gh CLI: %q", tool)
		}
	}
}

func TestReviewerAllowsOnlyReadsAndReviewPosts(t *testing.T) {
	var gh []string
	for _, tool := range reviewerAllowedTools {
		if strings.HasPrefix(tool, "Bash(gh") {
			gh = append(gh, tool)
		}
	}
	want := map[string]bool{
		// The two forms internal/review/prompt.go actually instructs. Note it
		// is gh api --method POST, not gh pr review, that posts the review: the
		// API call is the only form that attaches inline comments and returns
		// the created review's id for AO to record.
		"Bash(gh api --method GET:*)":         true,
		"Bash(gh api --method POST repos/:*)": true,
	}
	if len(gh) != len(want) {
		t.Fatalf("gh allowlist = %v, want exactly %d entries", gh, len(want))
	}
	for _, tool := range gh {
		if !want[tool] {
			t.Fatalf("unexpected gh allowlist entry %q", tool)
		}
	}
}

func TestReviewerDeniesCredentialAndDestructiveGhPaths(t *testing.T) {
	denied := make(map[string]bool, len(reviewerDisallowedTools))
	for _, tool := range reviewerDisallowedTools {
		denied[tool] = true
	}
	for _, want := range []string{
		"Bash(gh auth:*)",     // would print the token into the transcript
		"Bash(gh secret:*)",   // would write repository secrets
		"Bash(gh repo:*)",     // includes repo delete
		"Bash(gh release:*)",  // publishes artifacts
		"Bash(gh workflow:*)", // can disable or dispatch CI
		"Bash(gh pr merge:*)", // review is not merge authority
		"Bash(gh pr close:*)", //
		"Bash(gh api --method DELETE:*)",
		"Bash(gh api --method PATCH:*)",
		"Bash(gh api --method PUT:*)",
	} {
		if !denied[want] {
			t.Fatalf("reviewer denylist missing %q", want)
		}
	}
}

// The reviewer's read-only guarantee is the strongest positive finding in the
// audit; keep the write denials wired even as the gh entries change.
func TestReviewerStillDeniesWrites(t *testing.T) {
	denied := make(map[string]bool, len(reviewerDisallowedTools))
	for _, tool := range reviewerDisallowedTools {
		denied[tool] = true
	}
	for _, want := range []string{"Edit", "Write", "NotebookEdit", "Bash(git push:*)", "Bash(git commit:*)"} {
		if !denied[want] {
			t.Fatalf("reviewer denylist missing %q", want)
		}
	}
}
