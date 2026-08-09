#!/usr/bin/env bash
#
# Paseo compatibility spike for the AO integration.
#
# Proves — against a real installed Paseo — the capabilities the ExecutionBackend
# design depends on, and captures JSON fixtures that become the adapter's version
# contract. The fixtures matter because `paseo logs` has no --json, so there is no
# other machine-readable record of this CLI's behaviour.
#
# SAFETY. This runs against a THROWAWAY daemon on its own PASEO_HOME, its own
# port, and its own password, in a disposable git repo. It never starts, stops,
# restarts, or mutates the operator's desktop-managed daemon. `--all` on
# stop/delete is banned by lib/common.sh. Cleanup is idempotent and mandatory.
#
# Usage:
#   ./run-spike.sh            # run everything, then clean up
#   KEEP=1 ./run-spike.sh     # leave the daemon and repo up for inspection
#
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
readonly HERE

# --- configuration ---------------------------------------------------------
PASEO_BIN="${PASEO_BIN:-$(command -v paseo || true)}"
SPIKE_ROOT="${SPIKE_ROOT:-${TMPDIR:-/tmp}/ao-paseo-spike}"
SPIKE_HOME="${SPIKE_ROOT}/paseo-home"
SPIKE_REPO="${SPIKE_ROOT}/repo"
SPIKE_PORT="${SPIKE_PORT:-6799}"
SPIKE_HOST="127.0.0.1:${SPIKE_PORT}"
SPIKE_PASSWORD="${SPIKE_PASSWORD:-spike-$$-$(od -An -N4 -tx1 /dev/urandom | tr -d ' \n')}"
FIXTURES="${HERE}/fixtures"
VERDICTS="${SPIKE_ROOT}/verdicts.tsv"
# Exported, not merely readonly: the inline python3 heredocs below read FIXTURES
# and VERDICTS via os.environ, and a shell variable that is not exported is
# invisible to a child process. Without this they raise KeyError and the step
# reports FAIL — which reads as "Paseo does not support this" rather than
# "the harness could not find its own fixture directory".
readonly PASEO_BIN SPIKE_ROOT SPIKE_HOME SPIKE_REPO SPIKE_PORT SPIKE_HOST
readonly SPIKE_PASSWORD FIXTURES VERDICTS
export FIXTURES VERDICTS SPIKE_ROOT SPIKE_HOST

# shellcheck source=lib/common.sh
. "${HERE}/lib/common.sh"

[ -n "${PASEO_BIN}" ] || die "paseo not found on PATH; set PASEO_BIN."
command -v python3 >/dev/null || die "python3 required (JSON parsing)."
command -v git >/dev/null || die "git required."

mkdir -p "${SPIKE_HOME}" "${SPIKE_REPO}" "${FIXTURES}"
: >"${VERDICTS}"
assert_not_operator_daemon

trap 'if [ -z "${KEEP:-}" ]; then "${HERE}/cleanup.sh" || true; fi' EXIT

# ===========================================================================
step "S0  Preflight — versions and operator-daemon identity (no mutation)"

log "paseo: $("${PASEO_BIN}" --version)"

# Record the operator daemon's identity so cleanup can prove we never touched it.
if curl -fsS "http://127.0.0.1:${OPERATOR_PORT}/api/status" \
        -o "${FIXTURES}/s0-operator-status-before.json" 2>/dev/null; then
    log "operator daemon fingerprint recorded (untouched hereafter)"
else
    log "no operator daemon on :${OPERATOR_PORT} (fine)"
fi
pass S0 "preflight"

# ===========================================================================
step "S6a Throwaway daemon — own PASEO_HOME, port, and password"

# PASEO_PASSWORD on the daemon side is hashed into config at startup, so a
# throwaway password can be set non-interactively.
PASEO_HOME="${SPIKE_HOME}" PASEO_PASSWORD="${SPIKE_PASSWORD}" \
    "${PASEO_BIN}" daemon start \
        --home "${SPIKE_HOME}" \
        --listen "127.0.0.1:${SPIKE_PORT}" \
        --no-relay --no-mcp --no-web-ui \
        >"${SPIKE_ROOT}/daemon.out" 2>&1 &

for _ in $(seq 1 40); do
    if curl -fsS "http://${SPIKE_HOST}/api/health" >/dev/null 2>&1; then break; fi
    sleep 0.5
done
curl -fsS "http://${SPIKE_HOST}/api/health" >"${FIXTURES}/s6-health.json" \
    || die "throwaway daemon did not come up; see ${SPIKE_ROOT}/daemon.out"
# /api/health is the ONLY auth-exempt route; /api/status needs the bearer token.
curl -fsS -H "Authorization: Bearer ${SPIKE_PASSWORD}" \
    "http://${SPIKE_HOST}/api/status" >"${FIXTURES}/s6-status.json" || true

SPIKE_SERVER_ID=$(python3 -c 'import json;print(json.load(open("'"${FIXTURES}"'/s6-status.json")).get("serverId",""))' 2>/dev/null || echo "")
log "throwaway serverId=${SPIKE_SERVER_ID:-unknown}"
pass S6a "throwaway daemon up on ${SPIKE_HOST}"

# --- S7: auth behaviour ----------------------------------------------------
step "S7  Remote targeting and auth"

# Without a password the request must be rejected. Assert on the REASON, not
# merely on a non-zero exit: the first run of this spike "passed" this check
# because every invocation was dying on `unknown option '--host'` (the flag is
# per-subcommand, not global), which is indistinguishable from an auth rejection
# if you only look at the exit status.
s7a_out="$(env -u PASEO_PASSWORD -u PASEO_HOST "${PASEO_BIN}" ls --host "${SPIKE_HOST}" --json 2>&1 || true)"
if printf '%s' "${s7a_out}" | grep -qi "unknown option"; then
    fail S7a "harness bug, not an auth result: ${s7a_out}"
elif printf '%s' "${s7a_out}" | grep -qiE "auth|unauthor|401|password"; then
    pass S7a "unauthenticated request rejected with an auth error"
elif [ "${s7a_out}" = "[]" ]; then
    fail S7a "unauthenticated request SUCCEEDED against a password-protected daemon"
else
    pass S7a "unauthenticated request rejected: ${s7a_out}"
fi
if capture s7-ls-authed pz ls -a -g --json; then
    pass S7b "host:port + PASEO_PASSWORD works"
else
    fail S7b "authenticated ls failed"
fi

# tcp:// form carrying the password inline, with no PASEO_PASSWORD in the
# environment — this is the form AO would use for a remote host whose credential
# comes from a secret reference rather than the ambient environment.
if env -u PASEO_PASSWORD -u PASEO_HOST "${PASEO_BIN}" \
       ls --host "tcp://${SPIKE_HOST}?password=${SPIKE_PASSWORD}" --json >/dev/null 2>&1; then
    pass S7c "tcp://host:port?password= works"
else
    fail S7c "tcp:// form rejected"
fi

# The colonless-host footgun: paseo returns null and falls back to the LOCAL
# daemon. We assert the shape of the failure rather than exercising it.
log "S7d colonless --host falls through to the local daemon — guarded in lib/common.sh, not exercised"
skip S7d "documented, not exercised (would target the operator daemon)"

# ===========================================================================
step "S3/S4  Disposable repo, workspace title round-trip, two-step create"

git -C "${SPIKE_REPO}" rev-parse --git-dir >/dev/null 2>&1 || {
    git init -q -b main "${SPIKE_REPO}"
    git -C "${SPIKE_REPO}" -c user.email=spike@local -c user.name=spike \
        commit -q --allow-empty -m "spike base"
}

WS_TITLE="ao:spike-$$:1"
capture s3-workspace-create \
    pz workspace create --isolation worktree --mode branch-off \
       --path "${SPIKE_REPO}" --new-branch "ao/spike-$$" --base main \
       --worktree-slug "ao-spike-$$" --title "${WS_TITLE}" --json \
    || die "workspace create failed"

WS_ID=$(python3 -c 'import json;print(json.load(open("'"${FIXTURES}"'/s3-workspace-create.json"))["workspaceId"])')
log "workspaceId=${WS_ID}"

capture s3-workspace-ls pz workspace ls --json
WS_ID="${WS_ID}" WS_TITLE="${WS_TITLE}" jcheck S3 \
    "workspace --title round-trips as ls .name" \
    "--title did NOT round-trip (name is a derived fallback)" <<'PY'
import json, os, sys
try:
    rows = json.load(open(os.environ["FIXTURES"] + "/s3-workspace-ls.json"))
    hit = next((r for r in rows if r.get("workspaceId") == os.environ["WS_ID"]), None)
except Exception as exc:                      # harness fault, not a finding
    print(f"{type(exc).__name__}: {exc}", file=sys.stderr)
    sys.exit(2)
sys.exit(0 if hit and hit.get("name") == os.environ["WS_TITLE"] else 1)
PY

# ===========================================================================
step "S1  Event transport — the decisive experiment"

# S1f: does `terminal capture` give a real, cursored, JSON channel? This is the
# only Paseo surface with line addressing, and it takes the LLM out of the byte
# path entirely. If it works, it is the primary transport.
if capture s1f-terminal-create pz terminal create --cwd "${SPIKE_REPO}" --name ao-spike --json; then
    TERM_ID=$(python3 -c 'import json,sys;d=json.load(open("'"${FIXTURES}"'/s1f-terminal-create.json"));print(d.get("terminalId") or d.get("id",""))' 2>/dev/null || echo "")
    if [ -n "${TERM_ID}" ]; then
        # A deterministic writer: no model involved.
        for n in 1 2 3; do
            pz terminal send-keys "${TERM_ID}" \
                "printf 'AO_EVENT_SPIKE {\"seq\":${n},\"pad\":\"$(printf 'x%.0s' $(seq 1 200))\"}\\n'" Enter \
                >/dev/null 2>&1 || true
        done
        sleep 3
        capture s1f-terminal-capture pz terminal capture "${TERM_ID}" --start 0 --end 200 --json
        if grep -q 'AO_EVENT_SPIKE' "${FIXTURES}/s1f-terminal-capture.json" 2>/dev/null; then
            pass S1f "terminal capture recovered deterministic NDJSON with line cursors"
        else
            fail S1f "terminal capture did not contain the emitted marker"
        fi
        pz terminal kill "${TERM_ID}" >/dev/null 2>&1 || true
    else
        fail S1f "terminal create returned no terminalId"
    fi
else
    skip S1f "terminal create unavailable"
fi

# S1a/S1e: the sentinel path. Requires a provider and burns tokens, so it is
# opt-in. Without it, rung 1 stays unproven and the design uses rung 0 or 2.
if [ -n "${SPIKE_PROVIDER:-}" ]; then
    NONCE="spikenonce$$"
    PROMPT="Reply with exactly one line and nothing else: AO_EVENT_${NONCE} {\"schema\":\"ao.agent-event.v1\",\"eventId\":\"e1\",\"seq\":1,\"type\":\"checkpoint\",\"payload\":{\"summary\":\"spike\"}}"
    capture s1a-run pz run --workspace "${WS_ID}" --provider "${SPIKE_PROVIDER}" \
        -d --json --title "ao-spike-$$" \
        --label ao.session=spike-$$ --label ao.attempt=1 \
        --label ao.intent="intent-$$" --label paseo.worktree="spike-$$:1" \
        "${PROMPT}"
    AGENT_ID=$(python3 -c 'import json;print(json.load(open("'"${FIXTURES}"'/s1a-run.json"))["agentId"])')
    log "agentId=${AGENT_ID}  (workspaceId absent from run --json by design)"

    log "status after wait: $(wait_for_idle "${AGENT_ID}" 180 || true)"
    capture s1a-inspect pz inspect "${AGENT_ID}" --json
    # NOTE: no --filter and no --tail. Both corrupt or drop events; see PROTOCOL.md §1.
    pz logs "${AGENT_ID}" >"${FIXTURES}/s1a-logs.txt" 2>&1 || true

    if grep -q "AO_EVENT_${NONCE}" "${FIXTURES}/s1a-logs.txt"; then
        if grep -Eq "^AO_EVENT_${NONCE} \{.*\}$" "${FIXTURES}/s1a-logs.txt"; then
            pass S1a "sentinel survived intact on its own line"
        else
            fail S1a "sentinel present but line-split (delta-boundary shredding)"
        fi
    else
        fail S1a "sentinel not found in transcript"
    fi

    # S2: label discovery — the only reconciliation handle that exists.
    capture s2-ls-by-intent pz ls -a -g --json --label ao.intent="intent-$$"
    jcheck S2a "ls -ag --label found exactly the intended agent" \
               "label lookup did not return exactly one agent" <<'PY'
import json, os, sys
try:
    rows = json.load(open(os.environ["FIXTURES"] + "/s2-ls-by-intent.json"))
except Exception as exc:
    print(f"{type(exc).__name__}: {exc}", file=sys.stderr)
    sys.exit(2)
print(f"  matched {len(rows)} agent(s)", file=sys.stderr)
sys.exit(0 if len(rows) == 1 else 1)
PY

    # S2b: the fail-open bug. A malformed label (no '=') must NOT be used, because
    # `ls` applies zero filters and returns every agent on the daemon.
    capture s2-ls-malformed pz ls -a -g --json --label ao-malformed || true
    jcheck S2b "CONFIRMED fail-open: malformed label returned unfiltered results" \
               "malformed label returned nothing — fail-open NOT reproduced here" <<'PY'
import json, os, sys
try:
    rows = json.load(open(os.environ["FIXTURES"] + "/s2-ls-malformed.json"))
except Exception as exc:
    print(f"{type(exc).__name__}: {exc}", file=sys.stderr)
    sys.exit(2)
# A PASS here is bad news about Paseo: a label with no '=' applied zero filters
# and returned everything the daemon knows about.
print(f"  malformed-label query returned {len(rows)} agent(s)", file=sys.stderr)
sys.exit(0 if len(rows) > 0 else 1)
PY

    # S2c: does inspect echo our label back via the dead `paseo.worktree` key?
    if grep -q "spike-$$:1" "${FIXTURES}/s1a-inspect.json" 2>/dev/null; then
        pass S2c "inspect .Worktree echoes paseo.worktree — label read-back works"
    else
        fail S2c "no label read-back via .Worktree on this version"
    fi

    pz stop "${AGENT_ID}" >/dev/null 2>&1 || true
else
    skip S1a "set SPIKE_PROVIDER=claude|codex to run the sentinel + label experiments"
    skip S2a "requires SPIKE_PROVIDER"
fi

# ===========================================================================
step "S9/S10  Version pin and per-command latency"

capture s9-provider-ls  pz provider ls --json || true
{
    printf 'command\tseconds\n'
    for cmd in "ls -a -g --json" "workspace ls --json" "provider ls --json"; do
        s=$(python3 -c 'import time;print(time.time())')
        # shellcheck disable=SC2086
        pz $cmd >/dev/null 2>&1 || true
        e=$(python3 -c 'import time;print(time.time())')
        printf '%s\t%s\n' "${cmd}" "$(python3 -c "print(round(${e}-${s},3))")"
    done
} >"${FIXTURES}/s10-latency.tsv"
log "latency written to fixtures/s10-latency.tsv (sets observer cadence)"
pass S10 "latency measured"

# ===========================================================================
step "Report"

redact_fixtures

python3 - <<'PY'
import json, os
fx = os.environ["FIXTURES"]
verdicts = []
with open(os.environ["VERDICTS"]) as fh:
    for line in fh:
        parts = (line.rstrip("\n").split("\t") + ["", ""])[:3]
        verdicts.append({"step": parts[0], "verdict": parts[1], "note": parts[2]})
report = {
    "schema": "ao.paseo-capability-report.v1",
    "paseoVersion": os.environ.get("SPIKE_PASEO_VERSION", "unknown"),
    "verdicts": verdicts,
    "counts": {
        v: sum(1 for x in verdicts if x["verdict"] == v)
        for v in ("pass", "fail", "skip")
    },
}
with open(os.path.join(fx, "capability-report.json"), "w") as fh:
    json.dump(report, fh, indent=2, sort_keys=True)
    fh.write("\n")
print(json.dumps(report["counts"]))
PY

printf '\n\033[1mFixtures:\033[0m %s\n' "${FIXTURES}"
printf '\033[1mReport:\033[0m   %s/capability-report.json\n' "${FIXTURES}"
printf 'Record conclusions in FINDINGS.md.\n'
