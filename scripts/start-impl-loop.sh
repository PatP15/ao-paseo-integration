#!/usr/bin/env bash
#
# Launches the AO x Paseo implementation loop: a fresh codex worker per
# iteration, verified by a claude reviewer plus ./scripts/verify-fork-baseline.sh.
#
# Cross-provider on purpose — worker and verifier catch each other's blind spots.
#
# Run this yourself. It spawns autonomous agents with write access to this repo,
# so it is a deliberate human action, not something automation should trigger.
#
#   ./scripts/start-impl-loop.sh
#   paseo loop ls                  # watch it
#   paseo loop logs <id>
#   paseo loop stop <id>           # stop it
#
# Bounds: 8 iterations, 3h wall clock. No --sleep: nothing external is being
# polled, so iterations run back to back.
#
# NOTE ON MODE: --mode is deliberately NOT set, so the provider default applies
# (codex defaults to auto-review). If the worker stalls waiting for approvals,
# that is the tradeoff — do not reach for `--mode full-access`. See
# docs/paseo-integration/VULNERABILITIES.md §3.4: the shipped Paseo skill
# examples use full-access, agents pattern-match them, and the result is
# permission-free agents holding your credentials.
#
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

# Single-quoted on purpose: the backticks below are literal prompt text naming
# shell commands for the agent to run, not command substitutions for this script.
# shellcheck disable=SC2016
WORKER_PROMPT='You are implementing the AO x Paseo integration in /Users/patrickpuma/Github/ao-paseo-integration on branch paseo-integration. Work autonomously. Do exactly ONE pull request from the plan this iteration, then stop.

START by reading, in this order:
  docs/paseo-integration/IMPLEMENTATION_PLAN.md   (the PR sequence and acceptance criteria)
  docs/paseo-integration/ARCHITECTURE.md          (the ExecutionBackend port design)
  docs/paseo-integration/DATA_MODEL.md            (exact DDL, if you are doing storage)
  AGENTS.md                                       (repo hard rules)

STATE OF PLAY. PR 0 (docs + spike) is committed. A runtime-capability fix landed in 8cc3fe71. A verification gate landed in d9de2578. The next unblocked PRs are PR 1 (ExecutionBackend port and fork-owned vocabulary) then PR 2 (execution and work-graph tables in the 0900 migration block). Run `git log --oneline -8` to see what has actually landed before assuming.

PICK the smallest unblocked PR not yet done. A PR is BLOCKED if it needs spike fixtures (docs/paseo-integration/spike/fixtures/ is empty until a human runs the spike, which gates PR 4 onward) or a human decision.

HARD RULES:
- Prefer new files in new packages. This fork rebases weekly against an upstream doing ~16 commits/day, so every edit to an existing upstream file is future conflict.
- Do NOT edit backend/internal/session_manager/manager.go. That is PR 12, deliberately last.
- Migrations go in the 0900-0949 block, four digits, zero-padded. Never modify an already-merged migration. Never widen the sessions.harness CHECK constraint (it is maintained by exact-substring replace() against sqlite_master, so a drifted needle silently no-ops).
- After any schema or .sql query change run `npm run sqlc` and commit backend/internal/storage/sqlite/gen/ in the SAME commit. Never hand-edit gen/.
- No Paseo-specific types or imports in backend/internal/ports or backend/internal/domain.
- ExecutionRuntime.Alive MUST return a non-nil error when a host is unreachable. Returning (false, nil) is read as death by the reaper and would terminate a live remote session. Document this in the interface comment.

BEFORE YOU FINISH:
- Run `./scripts/verify-fork-baseline.sh`. It must print VERIFY PASS. It gates build, the no-new-test-failures rule, golangci-lint, frontend typecheck, and generated-artifact drift. Three upstream tests already fail and are listed in scripts/known-failing-tests.txt; that is expected and is not yours to fix.
- Commit atomically with a conventional-commit message explaining WHY, not just what.

DO NOT: push to any remote. Run any paseo command. Start, stop, or restart any daemon. Create OS users. Run the compatibility spike. Modify anything under docs/paseo-integration/ except to correct a factual error you can prove.

If the only remaining work is blocked on a human, do not invent work. Say clearly which PR is next, what it is blocked on, and stop.'

# shellcheck disable=SC2016  # same: literal backticks in prompt text
VERIFY_PROMPT='You are verifying one iteration of work on the AO x Paseo fork at /Users/patrickpuma/Github/ao-paseo-integration, branch paseo-integration.

Check facts. Do not suggest fixes. Cite the exact commands you ran and their output.

Return done=true ONLY if BOTH hold:

(A) No unblocked code work remains in docs/paseo-integration/IMPLEMENTATION_PLAN.md. Remaining PRs are blocked if they depend on spike fixtures (PR 4 onward needs docs/paseo-integration/spike/fixtures/, empty until a human runs the spike) or on a human decision. If unblocked work remains, done=false.

(B) The most recent commit is a complete, coherent PR from that plan whose stated acceptance criteria are all met.

Verify by running commands:
1. `git log --oneline -3` and `git status --short` — work must be COMMITTED and the tree clean.
2. `./scripts/verify-fork-baseline.sh` — must print VERIFY PASS. Quote the final line.
3. `git show --stat HEAD` — flag any modification to an upstream file the plan did not call for. New files under backend/internal/{ports,domain,adapters/execution,storage/sqlite} are expected; edits to session_manager/manager.go are NOT expected before PR 12.
4. If migrations were added: confirm versions are in 0900-0949, no already-merged migration was modified, and the sessions.harness CHECK was not widened (`git show HEAD | grep -i harness`).
5. If .sql or schema changed: confirm backend/internal/storage/sqlite/gen/ was regenerated in the SAME commit.
6. If a port was added: confirm no Paseo type or import leaked into ports/ or domain/ (`grep -rn paseo backend/internal/ports backend/internal/domain`).

Report done=false with specific evidence if any check fails, and state plainly which one.'

exec paseo loop run \
    --name ao-paseo-prs \
    --provider codex/gpt-5.4 \
    --verify-provider claude/opus \
    --max-iterations 8 \
    --max-time 3h \
    --archive \
    --verify-check './scripts/verify-fork-baseline.sh' \
    --verify "${VERIFY_PROMPT}" \
    "${WORKER_PROMPT}"
