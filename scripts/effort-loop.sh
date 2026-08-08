#!/usr/bin/env bash
#
# Worker/verifier loop with CONTROLLABLE REASONING EFFORT.
#
# `paseo loop run` cannot set thinking effort. This is a protocol limitation,
# not a missing flag: packages/protocol/src/loop/rpc-schemas.ts carries model,
# modeId, workerModel, verifierModel and verifierModeId, and no thinking field.
# Loop agents therefore run at each model's default (gpt-5.6-sol => medium, of
# low/medium/high/xhigh/max/ultra).
#
# `paseo run` DOES accept --thinking. This script rebuilds the loop on top of
# `paseo run`, so effort becomes a knob. Two other things improve as a result:
#
#   1. The verifier returns a SCHEMA-VALIDATED verdict via --output-schema
#      rather than prose we have to interpret. --output-schema cannot combine
#      with --background, but the verifier is a bounded foreground call, so the
#      restriction does not bind. (This is the "repurposed --output-schema"
#      pattern from docs/paseo-integration/PROTOCOL.md.)
#   2. We own the exit condition, so "stop when the gate fails twice running"
#      is expressible, which `paseo loop` cannot express.
#
# Cost: the worker and verifier agents are ordinary agents, so unlike
# loop-managed ones they ARE visible in `paseo ls` and the desktop app. That is
# a feature here — loop-managed agents are created internal:true and are hidden
# from every listing, which made debugging the first loop run considerably
# harder than it needed to be.
#
# Usage:
#   ./scripts/effort-loop.sh                          # defaults below
#   WORKER_THINKING=xhigh ./scripts/effort-loop.sh
#   MAX_ITERATIONS=3 ./scripts/effort-loop.sh
#   DRY_RUN=1 ./scripts/effort-loop.sh                # print the plan, launch nothing
#
set -uo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.." || exit 1
REPO="$(pwd)"
readonly REPO

# --- knobs -----------------------------------------------------------------
WORKER_PROVIDER="${WORKER_PROVIDER:-codex}"
WORKER_MODEL="${WORKER_MODEL:-gpt-5.6-sol}"
WORKER_THINKING="${WORKER_THINKING:-high}"
# auto-review, not auto: both are approvalPolicy=on-request + sandbox=
# workspace-write, but auto-review additionally routes eligible approvals
# through the auto_review subagent (codex-app-server-agent.ts MODE_PRESETS).
# Under plain `auto`, an unattended worker stalls on an approval nobody is
# there to answer. Set WORKER_MODE=auto to override.
WORKER_MODE="${WORKER_MODE:-auto-review}"

VERIFY_PROVIDER="${VERIFY_PROVIDER:-claude}"
VERIFY_MODEL="${VERIFY_MODEL:-claude-opus-5}"
VERIFY_THINKING="${VERIFY_THINKING:-xhigh}"
VERIFY_MODE="${VERIFY_MODE:-auto}"

MAX_ITERATIONS="${MAX_ITERATIONS:-12}"
WORKER_TIMEOUT_S="${WORKER_TIMEOUT_S:-5400}"   # 90m per iteration
GATE="${GATE:-./scripts/verify-fork-baseline.sh}"
LOGDIR="${LOGDIR:-${REPO}/.effort-loop}"

hdr()  { printf '\n\033[1m== %s\033[0m\n' "$*"; }
info() { printf '  %s\n' "$*"; }
ok()   { printf '  \033[32mOK\033[0m   %s\n' "$*"; }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*"; }
die()  { printf '\n\033[31mfatal:\033[0m %s\n' "$*" >&2; exit 1; }

command -v paseo >/dev/null || die "paseo not on PATH"
command -v python3 >/dev/null || die "python3 required"
mkdir -p "${LOGDIR}"

# --- preflight -------------------------------------------------------------
# Validate model AND thinking id against the daemon. A bad --thinking is worth
# catching in one second rather than one iteration.
preflight() {
    local provider="$1" model="$2" thinking="$3" label="$4"
    paseo provider models "${provider}" --json 2>/dev/null \
        | python3 -c '
import json, sys
provider, model, thinking, label = sys.argv[1:5]
rows = json.load(sys.stdin)
rows = rows if isinstance(rows, list) else rows.get("models", [])
hit = next((r for r in rows if r.get("id") == model), None)
if hit is None:
    print(f"  {label}: model {model!r} not available from {provider}", file=sys.stderr)
    print("  available: " + ", ".join(sorted(r.get("id", "?") for r in rows)), file=sys.stderr)
    sys.exit(1)
opts = hit.get("thinkingOptionIds") or []
if thinking not in opts:
    joined = ", ".join(opts)
    print(f"  {label}: thinking {thinking!r} not valid for {model}", file=sys.stderr)
    print(f"  available: {joined}", file=sys.stderr)
    sys.exit(1)
default = hit.get("defaultThinkingOptionId")
print(f"  ok  {label}: {provider}/{model} thinking={thinking} (model default is {default})")
' "${provider}" "${model}" "${thinking}" "${label}" || die "preflight failed for ${label}"
}

hdr "Preflight"
preflight "${WORKER_PROVIDER}" "${WORKER_MODEL}" "${WORKER_THINKING}" worker
preflight "${VERIFY_PROVIDER}" "${VERIFY_MODEL}" "${VERIFY_THINKING}" verifier
info "worker mode=${WORKER_MODE}  verifier mode=${VERIFY_MODE}"
info "bounds: ${MAX_ITERATIONS} iterations, ${WORKER_TIMEOUT_S}s per worker"
[ -x "${GATE}" ] || die "gate not executable: ${GATE}"

# --- prompts ---------------------------------------------------------------
# shellcheck disable=SC2016  # literal backticks: prompt text, not substitution
read -r -d '' WORKER_PROMPT <<'PROMPT' || true
You are implementing the AO x Paseo integration in this repository, on branch
paseo-integration. Work autonomously. Do exactly ONE pull request from the plan
this iteration, then stop.

Read first, in order:
  docs/paseo-integration/GAPS.md                   (the work list — G1..G5, in order)
  docs/paseo-integration/END_TO_END.md             (why these gaps exist)
  docs/paseo-integration/spike/FINDINGS.md         (empirically verified Paseo behaviour)
  docs/paseo-integration/ARCHITECTURE.md
  AGENTS.md

FIRST run BOTH `git log --oneline -12` AND `git status --short`.

If `git status` shows uncommitted work, a previous iteration was cut off
mid-PR — by a usage limit, a timeout, or an error. FINISH THAT WORK. Do not
start a different gap on top of it and do not discard it. Run
./scripts/verify-fork-baseline.sh to see exactly what is broken; a half-finished
storage change typically needs `npm run sqlc` plus whatever the build reports.
Only once the tree is clean and committed should you pick new work.

If the tree IS clean, do the LOWEST-NUMBERED gap in GAPS.md that is not yet
done (check git log — a gap is done when a commit implements it and the gate
passed). Do exactly one gap this iteration. G3 is retracted; skip it. G4 has
two parts — if the second (the host reporter binary) is large, land the first
part (read wiring) as its own commit and stop.

HARD RULES:
- Prefer new files in new packages. This fork rebases weekly against an upstream
  doing ~16 commits/day; every edit to an existing upstream file is future conflict.
- Do NOT edit backend/internal/session_manager/manager.go. That is PR 12, last.
- Migrations go in the 0900-0949 block, four digits. Never modify a merged
  migration. Never widen the sessions.harness CHECK.
- After any schema or .sql change run `npm run sqlc` and commit gen/ in the SAME commit.
- No Paseo types or imports in backend/internal/ports or backend/internal/domain.
- ExecutionRuntime.Alive must return a non-nil error when a host is unreachable;
  (false, nil) is read as death by the reaper and would kill a live remote session.

BEFORE FINISHING: run ./scripts/verify-fork-baseline.sh — it must print VERIFY
PASS. Three upstream tests already fail and are listed in
scripts/known-failing-tests.txt; those are expected and not yours to fix.
Then commit atomically with a conventional-commit message naming the gap (e.g. "feat(workitem): G1 ...") and explaining WHY.

DO NOT: push, run any paseo command, start/stop any daemon, or run the spike.
If the only remaining work is blocked on a human, say which PR and why, and stop.
PROMPT

read -r -d '' VERIFY_PROMPT <<'PROMPT' || true
Verify the most recent commit on branch paseo-integration in this repository.
Check facts by running commands. Do not suggest fixes. Do not make changes.

Run and consider:
1. `git log --oneline -3` and `git status --short` — work must be COMMITTED, tree clean.
2. `./scripts/verify-fork-baseline.sh` — must print VERIFY PASS.
3. `git show --stat HEAD` — flag any upstream file the plan did not call for.
   Edits to session_manager/manager.go are NOT expected before PR 12.
4. If migrations were added: versions in 0900-0949, no merged migration modified,
   sessions.harness CHECK not widened.
5. If .sql or schema changed: gen/ regenerated in the SAME commit.
6. If a port was added: no Paseo type or import in ports/ or domain/.

Then read docs/paseo-integration/GAPS.md and decide whether any gap (G1, G2,
G4, G5 — G3 is retracted) is not yet implemented. done=true only when every
non-retracted gap has a commit and the gate passes.
PROMPT

VERDICT_SCHEMA='{"type":"object","properties":{"gate_passed":{"type":"boolean"},"commit_sound":{"type":"boolean"},"unblocked_work_remains":{"type":"boolean"},"done":{"type":"boolean"},"reason":{"type":"string"}},"required":["gate_passed","commit_sound","unblocked_work_remains","done","reason"],"additionalProperties":false}'

if [ -n "${DRY_RUN:-}" ]; then
    hdr "DRY RUN — nothing launched"
    info "worker:   paseo run -d --provider ${WORKER_PROVIDER} --model ${WORKER_MODEL} --mode ${WORKER_MODE} --thinking ${WORKER_THINKING}"
    info "verifier: paseo run --provider ${VERIFY_PROVIDER} --model ${VERIFY_MODEL} --mode ${VERIFY_MODE} --thinking ${VERIFY_THINKING} --output-schema <verdict>"
    exit 0
fi

# --- the loop --------------------------------------------------------------
consecutive_gate_failures=0
consecutive_launch_failures=0
for (( i = 1; i <= MAX_ITERATIONS; i++ )); do
    hdr "Iteration ${i}/${MAX_ITERATIONS}"
    head_before="$(git -C "${REPO}" rev-parse --short HEAD)"

    info "launching worker (thinking=${WORKER_THINKING}, mode=${WORKER_MODE})"
    run_json="$(paseo run -d --json \
        --provider "${WORKER_PROVIDER}" --model "${WORKER_MODEL}" \
        --mode "${WORKER_MODE}" --thinking "${WORKER_THINKING}" \
        --title "ao-effort-loop iter ${i}" \
        --label ao.loop=effort --label "ao.iteration=${i}" \
        "${WORKER_PROMPT}" 2>"${LOGDIR}/worker-${i}.err")" || {
            bad "worker launch failed; see ${LOGDIR}/worker-${i}.err"; break; }

    agent_id="$(printf '%s' "${run_json}" | python3 -c 'import json,sys
try: print(json.load(sys.stdin).get("agentId",""))
except Exception: print("")' 2>/dev/null)"
    if [ -z "${agent_id}" ]; then
        # A launch that never produced an agent is usually a usage limit or an
        # unavailable provider — a condition that will not clear by retrying in
        # a tight loop. `paseo loop` has no such guard and burned 12 iterations
        # in two minutes against exactly this error.
        bad "no agent created (usage limit? provider down?)"
        sed -n '1,4p' "${LOGDIR}/worker-${i}.err" 2>/dev/null | sed 's/^/       /'
        consecutive_launch_failures=$(( consecutive_launch_failures + 1 ))
        if [ "${consecutive_launch_failures}" -ge 2 ]; then
            bad "worker failed to launch twice — stopping rather than burning iterations"
            exit 1
        fi
        continue
    fi
    consecutive_launch_failures=0
    info "worker agent ${agent_id:0:12} (visible in paseo ls, unlike loop-managed agents)"

    paseo wait "${agent_id}" --timeout "${WORKER_TIMEOUT_S}" >/dev/null 2>&1
    info "worker finished (status $(paseo inspect "${agent_id}" --json 2>/dev/null \
        | python3 -c 'import json,sys; print(json.load(sys.stdin).get("Status","?"))' 2>/dev/null || echo '?'))"

    head_after="$(git -C "${REPO}" rev-parse --short HEAD)"
    if [ "${head_before}" = "${head_after}" ]; then
        info "no new commit this iteration"
    else
        ok "new commit ${head_after}: $(git -C "${REPO}" log --oneline -1)"
    fi

    info "running gate: ${GATE}"
    if "${GATE}" >"${LOGDIR}/gate-${i}.log" 2>&1; then
        ok "gate PASS"
        consecutive_gate_failures=0
    else
        bad "gate FAIL (see ${LOGDIR}/gate-${i}.log)"
        consecutive_gate_failures=$(( consecutive_gate_failures + 1 ))
        if [ "${consecutive_gate_failures}" -ge 2 ]; then
            bad "gate failed twice consecutively — stopping rather than compounding"
            exit 1
        fi
    fi

    info "launching verifier (thinking=${VERIFY_THINKING}, schema-validated verdict)"
    verdict="$(paseo run --json \
        --provider "${VERIFY_PROVIDER}" --model "${VERIFY_MODEL}" \
        --mode "${VERIFY_MODE}" --thinking "${VERIFY_THINKING}" \
        --output-schema "${VERDICT_SCHEMA}" \
        --title "ao-effort-loop verify ${i}" \
        "${VERIFY_PROMPT}" 2>"${LOGDIR}/verify-${i}.err")" || {
            bad "verifier failed; see ${LOGDIR}/verify-${i}.err"; continue; }

    printf '%s\n' "${verdict}" >"${LOGDIR}/verdict-${i}.json"
    printf '%s' "${verdict}" | python3 -c '
import json, sys
raw = sys.stdin.read()
try:
    d = json.loads(raw)
except json.JSONDecodeError:
    print("  verdict was not valid JSON; see the log", file=sys.stderr); sys.exit(3)
d = d if isinstance(d, dict) else {}
for k in ("gate_passed", "commit_sound", "unblocked_work_remains", "done"):
    print(f"    {k:24s} {d.get(k)}")
print(f"    reason                   {str(d.get('"'"'reason'"'"',''))[:160]}")
sys.exit(0 if d.get("done") is True else 1)
' && { hdr "Verifier reports done"; exit 0; }
done

hdr "Loop finished"
info "iterations exhausted or stopped; logs in ${LOGDIR}"
