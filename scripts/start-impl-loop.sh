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

WORKER_PROVIDER="${WORKER_PROVIDER:-codex/gpt-5.6-sol}"
VERIFY_PROVIDER="${VERIFY_PROVIDER:-claude/claude-opus-5}"

# Fallback worker for when the primary provider runs out of credit or hits a
# usage limit. `paseo loop run` has no built-in provider failover — a usage
# error simply fails the iteration — so this is a re-launch target, not an
# automatic switch:
#
#   WORKER_PROVIDER="${FALLBACK_WORKER_PROVIDER}" ./scripts/start-impl-loop.sh
#
# Work already committed by earlier iterations is picked up automatically: the
# worker prompt reads git log and IMPLEMENTATION_PLAN.md to choose the next
# unblocked PR, so a re-launch resumes rather than restarts.
#
# Note this makes worker and verifier the same provider, losing the
# cross-provider blind-spot coverage. Acceptable as a fallback; not a default.
FALLBACK_WORKER_PROVIDER="${FALLBACK_WORKER_PROVIDER:-claude/claude-opus-5}"

# Bounds. The first run hit --max-time mid-iteration with PR 2 uncommitted;
# nothing was lost (the files stay in the tree) but the iteration's work was
# unverified and unattributed until the next run picked it up. Storage and
# adapter PRs take well over an hour each, so budget generously and rely on the
# verifier's done=true to exit early when no unblocked work remains.
MAX_ITERATIONS="${MAX_ITERATIONS:-12}"
MAX_TIME="${MAX_TIME:-6h}"

# Preflight: an unknown model wedges the loop instead of failing.
#
# The first run of this script used codex/gpt-5.4, which does not exist. Paseo
# minted a worker agent id, recorded it on iteration 1, and then reported
# "Agent not found" for that same id — leaving the loop in `running` with no
# worker, no outcome, and no error, where it would have sat until --max-time.
# Exactly the orphan class documented in docs/paseo-integration/RECOVERY.md §1.2:
# an id exists in one system and not the other, and nothing reconciles them.
#
# So validate before launching. `paseo provider models <p>` is the authority;
# hardcoded model names in skills and docs go stale.
check_model() {
    local spec="$1" label="$2" provider model
    provider="${spec%%/*}"
    model="${spec#*/}"
    if [ "${provider}" = "${model}" ]; then
        printf 'refusing: %s provider %q has no model; use provider/model\n' "${label}" "${spec}" >&2
        exit 1
    fi
    if ! paseo provider models "${provider}" --json 2>/dev/null \
         | python3 -c 'import json,sys
rows = json.load(sys.stdin)
rows = rows if isinstance(rows, list) else rows.get("models", [])
sys.exit(0 if sys.argv[1] in {r.get("id") for r in rows} else 1)' "${model}"; then
        printf 'refusing: %s model %q is not available from provider %q.\n' \
            "${label}" "${model}" "${provider}" >&2
        printf 'available:\n' >&2
        paseo provider models "${provider}" 2>/dev/null | sed 's/^/  /' >&2
        exit 1
    fi
    printf '  ok  %s: %s\n' "${label}" "${spec}"
}

printf 'Preflight\n'
check_model "${WORKER_PROVIDER}" worker
check_model "${VERIFY_PROVIDER}" verifier

# Single-quoted on purpose: the backticks below are literal prompt text naming
# shell commands for the agent to run, not command substitutions for this script.
# shellcheck disable=SC2016
WORKER_PROMPT='You are implementing the AO x Paseo integration in /Users/patrickpuma/Github/ao-paseo-integration on branch paseo-integration. Work autonomously. Do exactly ONE pull request from the plan this iteration, then stop.

START by reading, in this order:
  docs/paseo-integration/IMPLEMENTATION_PLAN.md   (the PR sequence and acceptance criteria)
  docs/paseo-integration/ARCHITECTURE.md          (the ExecutionBackend port design)
  docs/paseo-integration/DATA_MODEL.md            (exact DDL, if you are doing storage)
  AGENTS.md                                       (repo hard rules)

STATE OF PLAY. Landed: PR 0 (docs + spike), a runtime-capability fix (8cc3fe71), the verification gate (d9de2578), PR 1 (1898cc85, the ExecutionBackend port), PR 2 (d80eb1fc, the 0900 migration block), and the EXECUTED spike (49f062ec). Run `git log --oneline -12` to confirm before assuming.

The spike has run, so docs/paseo-integration/spike/fixtures/ is now POPULATED and PR 4 onward is UNBLOCKED. Read docs/paseo-integration/spike/FINDINGS.md before writing the Paseo adapter — it contains empirically verified behaviour that contradicts naive expectations.

PICK the smallest unblocked PR not yet done. A PR is BLOCKED only if it needs a human decision.

SPIKE FINDINGS THE ADAPTER MUST HONOUR (all verified against a real Paseo 0.2.5, fixtures in spike/fixtures/):
- `--host` is a PER-SUBCOMMAND option, not global. `paseo --host X ls` fails with "unknown option". Emit `paseo ls --host X`, or set PASEO_HOST in the environment.
- A malformed `--label` (no `=`) makes `ls` return the FULL UNFILTERED agent list. Confirmed on a real daemon. Validate exactly one `=`, non-empty key AND value, in Go before exec.
- `terminal capture --start/--end --json` returns {terminalId, lines[], totalLines} with a real monotonic cursor, but `lines[]` are SCREEN lines hard-wrapped at 80 columns. A 200-byte payload came back as three lines. Reassembly via 76-char base64 chunks with a k/n header is mandatory.
- `inspect --json .Worktree` echoes labels["paseo.worktree"], which is how an orphan is verified before adoption.
- Each paseo invocation costs ~0.9s (shell shim to an Electron helper). Budget polling accordingly; prefer one inspect per session over ls-then-inspect fan-out.

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

(A) No unblocked code work remains in docs/paseo-integration/IMPLEMENTATION_PLAN.md. The spike has already run, so spike fixtures exist and PR 4 onward is unblocked; a PR is blocked only if it needs a human decision. If unblocked work remains, done=false.

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
    --provider "${WORKER_PROVIDER}" \
    --verify-provider "${VERIFY_PROVIDER}" \
    --max-iterations "${MAX_ITERATIONS}" \
    --max-time "${MAX_TIME}" \
    --archive \
    --verify-check './scripts/verify-fork-baseline.sh' \
    --verify "${VERIFY_PROMPT}" \
    "${WORKER_PROMPT}"
