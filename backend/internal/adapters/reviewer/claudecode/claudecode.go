// Package claudecode is the claude-code reviewer adapter. claude-code is a
// prompt-driven agent, so this reviewer feeds AO's review prompt (authored
// centrally and passed in ReviewInvocation.Prompt) to the worker claude-code
// adapter's launch-command construction (binary resolution, flags). The reviewer
// contract stays prompt-agnostic, so a one-shot CLI reviewer (e.g. greptile) can
// ignore the prompt entirely.
package claudecode

import (
	"context"

	workeragent "github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/claudecode"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// Reviewer is the claude-code code-review adapter.
type Reviewer struct {
	agent ports.Agent
}

// New builds the claude-code reviewer adapter.
func New() *Reviewer {
	return &Reviewer{agent: workeragent.New()}
}

// Harness identifies this reviewer in the reviewer registry.
func (r *Reviewer) Harness() domain.ReviewerHarness {
	return domain.ReviewerClaudeCode
}

var _ ports.Reviewer = (*Reviewer)(nil)
var _ ports.ReviewerCanceller = (*Reviewer)(nil)

// reviewerAllowedTools is the read-only tool allowlist the reviewer launches
// with. The reviewer runs headless (no human to approve prompts) but must stay
// read-only, so instead of bypassPermissions — which skips the permission
// system entirely and ignores allow/deny rules — it launches in the default
// mode where these rules are honored: allow rules auto-approve without
// prompting, so the reviewer can read the checkout and run the few commands it
// needs (git diff/log/show to inspect the PR, printf to pipe review JSON into
// the downstream commands without writing a worktree file, gh to post the
// review, and `ao review submit` to record the verdict) without stalling.
//
// The gh entries are deliberately narrow. A blanket Bash(gh:*) auto-approves
// the whole forge CLI — `gh auth token`, `gh secret set`, `gh repo delete`,
// `gh api --method DELETE` — under a token that reaches every repository the
// operator can see, while the reviewer's entire input is a diff and a PR
// context that anyone who can comment on the PR helped write. Because nothing
// outside this list can be approved (there is no human in the loop), narrowing
// it is a hard stop rather than a hint.
//
// The two entries are exactly what internal/review/prompt.go instructs. Note it
// is `gh api --method POST`, not `gh pr review`, that posts the review: the
// prompt uses the API deliberately, because it is the only form that attaches
// inline comments and returns the created review's id for AO to record.
var reviewerAllowedTools = []string{
	"Read",
	"Grep",
	"Glob",
	"Bash(printf:*)",
	"Bash(gh api --method GET:*)",
	"Bash(gh api --method POST repos/:*)",
	"Bash(git diff:*)",
	"Bash(git log:*)",
	"Bash(git show:*)",
	"Bash(git status:*)",
	"Bash(ao review submit:*)",
}

// reviewerDisallowedTools hard-denies the write paths as defense in depth, so a
// misbehaving model cannot edit files or move the branch even if a future
// allowlist entry would otherwise admit it.
//
// The gh denials cover the subcommands that would do real damage with a forge
// token if a later allowlist entry re-widened the CLI, and the credential read
// that would put the token itself into a transcript.
var reviewerDisallowedTools = []string{
	"Edit",
	"Write",
	"NotebookEdit",
	"Bash(git push:*)",
	"Bash(git commit:*)",
	"Bash(gh auth:*)",
	"Bash(gh secret:*)",
	"Bash(gh repo:*)",
	"Bash(gh release:*)",
	"Bash(gh workflow:*)",
	"Bash(gh pr merge:*)",
	"Bash(gh pr close:*)",
	"Bash(gh api --method DELETE:*)",
	"Bash(gh api --method PATCH:*)",
	"Bash(gh api --method PUT:*)",
}

// ReviewCommand builds a claude-code invocation that reviews the worker's
// checkout for the PR. Production launches provide the standing instructions
// through an AO-owned prompt file so only the short task-file reference is
// terminal-visible.
func (r *Reviewer) ReviewCommand(ctx context.Context, inv ports.ReviewInvocation) (ports.ReviewCommandSpec, error) {
	argv, err := r.agent.GetLaunchCommand(ctx, ports.LaunchConfig{
		SessionID:        inv.ReviewerID,
		WorkspacePath:    inv.WorkspacePath,
		Prompt:           inv.Prompt,
		SystemPrompt:     inv.SystemPrompt,
		SystemPromptFile: inv.SystemPromptFile,
		// Launch off bypassPermissions so the allow/deny lists are enforced.
		// Set an explicit non-bypass mode instead of deferring to the user's
		// Claude defaultMode, which may itself be bypassPermissions.
		Permissions:     ports.PermissionModeAuto,
		AllowedTools:    reviewerAllowedTools,
		DisallowedTools: reviewerDisallowedTools,
	})
	if err != nil {
		return ports.ReviewCommandSpec{}, err
	}
	return ports.ReviewCommandSpec{Argv: argv}, nil
}

// PreLaunch runs any reviewer-specific preflight. For Claude Code this records
// the worker checkout as trusted before the headless reviewer pane starts.
func (r *Reviewer) PreLaunch(ctx context.Context, inv ports.ReviewInvocation) error {
	pl, ok := r.agent.(interface {
		PreLaunch(context.Context, ports.LaunchConfig) error
	})
	if !ok {
		return nil
	}
	return pl.PreLaunch(ctx, ports.LaunchConfig{
		SessionID:     inv.ReviewerID,
		WorkspacePath: inv.WorkspacePath,
	})
}

// ReviewMessage is the text injected into an already-running reviewer pane to
// review a new commit — AO's central review prompt.
func (r *Reviewer) ReviewMessage(_ context.Context, inv ports.ReviewInvocation) (string, error) {
	return inv.Prompt, nil
}

// ReviewCancel stops the active Claude Code reviewer turn while preserving the
// terminal pane for inspection.
func (r *Reviewer) ReviewCancel(context.Context) (ports.ReviewCancelSpec, error) {
	return ports.ReviewCancelSpec{Mode: ports.ReviewCancelInterrupt, Interrupts: 2}, nil
}
