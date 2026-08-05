# shellcheck shell=bash
# Shared guards, logging, and capture helpers for the Paseo compatibility spike.
#
# Sourced by run-spike.sh and cleanup.sh. Not executable on its own.

# ---------------------------------------------------------------------------
# Safety guards
# ---------------------------------------------------------------------------

# The spike must never touch the operator's real daemon. That daemon is owned by
# the Paseo desktop app (`desktopManaged: true`) and restarting or mutating it
# would kill whatever the operator is running.
readonly OPERATOR_PORT=6767

# Refuse to run if SPIKE_PORT collides with the operator's daemon.
assert_not_operator_daemon() {
    if [ "${SPIKE_PORT}" = "${OPERATOR_PORT}" ]; then
        die "SPIKE_PORT=${SPIKE_PORT} is the default operator daemon port. Refusing."
    fi
    if [ "${SPIKE_HOME}" = "${HOME}/.paseo" ]; then
        die "SPIKE_HOME is the operator's PASEO_HOME. Refusing."
    fi
}

# Every paseo invocation in the spike goes through this wrapper. It:
#   - forces --host at the throwaway daemon
#   - scrubs Paseo's ambient env (a leaked PASEO_AGENT_ID silently reparents agents)
#   - refuses the catastrophic --all flag on destructive verbs
#   - refuses a colonless host (which silently falls through to the LOCAL daemon)
pz() {
    local verb="${1:-}"
    case "${verb}" in
        stop|delete|archive)
            local a
            for a in "$@"; do
                if [ "${a}" = "--all" ]; then
                    die "BANNED: '--all' on '${verb}'. Blast radius is the whole daemon."
                fi
            done
            ;;
    esac

    case "${SPIKE_HOST}" in
        *:*) : ;;
        *) die "SPIKE_HOST='${SPIKE_HOST}' has no colon; paseo would silently use the LOCAL daemon." ;;
    esac

    env -u PASEO_AGENT_ID -u PASEO_WORKSPACE_ID -u PASEO_HOME -u PASEO_LISTEN \
        -u PASEO_SERVER_ID -u PASEO_HOST \
        PASEO_PASSWORD="${SPIKE_PASSWORD}" \
        "${PASEO_BIN}" --host "${SPIKE_HOST}" "$@"
}

# ---------------------------------------------------------------------------
# Logging
# ---------------------------------------------------------------------------

log()  { printf '  %s\n' "$*"; }
step() { printf '\n\033[1m== %s\033[0m\n' "$*"; }
pass() { printf '  \033[32mPASS\033[0m %s\n' "$*"; record_verdict "$1" pass "${2:-}"; }
fail() { printf '  \033[31mFAIL\033[0m %s\n' "$*"; record_verdict "$1" fail "${2:-}"; }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; record_verdict "$1" skip "${2:-}"; }
die()  { printf '\n\033[31mfatal:\033[0m %s\n' "$*" >&2; exit 1; }

# ---------------------------------------------------------------------------
# Fixtures and the machine-readable capability report
# ---------------------------------------------------------------------------

# capture <name> <command...>
# Runs the command, writes stdout to fixtures/<name>.json and stderr to
# fixtures/<name>.stderr.txt, and returns the command's exit status.
# stderr is captured separately and deliberately: `paseo run` prints
# "Created workspace <id>" to stderr while --json goes to stdout.
capture() {
    local name="$1"; shift
    local out="${FIXTURES}/${name}.json"
    local err="${FIXTURES}/${name}.stderr.txt"
    local rc=0
    "$@" >"${out}" 2>"${err}" || rc=$?
    log "fixture: ${name}.json ($(wc -c <"${out}" | tr -d ' ') bytes, rc=${rc})"
    return "${rc}"
}

# record_verdict <step-id> <pass|fail|skip> [note]
record_verdict() {
    printf '%s\t%s\t%s\n' "$1" "$2" "${3:-}" >>"${VERDICTS}"
}

# Redact anything credential-shaped before a fixture is committed.
# Offer URLs and ?password= are bearer credentials (see VULNERABILITIES.md §2).
redact_fixtures() {
    local f
    for f in "${FIXTURES}"/*; do
        [ -f "${f}" ] || continue
        # macOS and GNU sed differ on -i; write to a temp file instead.
        sed -e 's/\(password=\)[^&"[:space:]]*/\1REDACTED/g' \
            -e 's/#offer=[A-Za-z0-9_-]*/#offer=REDACTED/g' \
            -e "s#${SPIKE_PASSWORD}#REDACTED#g" \
            "${f}" >"${f}.redacted" && mv "${f}.redacted" "${f}"
    done
    log "fixtures redacted"
}

# Wait until an agent leaves 'running', or time out. Paseo's status enum is
# initializing|idle|running|error|closed and there is NO completed state, so
# 'idle' here means "not currently running" and nothing more.
wait_for_idle() {
    local agent="$1" budget="${2:-120}" status=""
    local waited=0
    while [ "${waited}" -lt "${budget}" ]; do
        status=$(pz inspect "${agent}" --json 2>/dev/null \
                 | python3 -c 'import json,sys; print(json.load(sys.stdin).get("Status",""))' 2>/dev/null || true)
        case "${status}" in
            idle|error|closed) printf '%s' "${status}"; return 0 ;;
        esac
        sleep 3
        waited=$((waited + 3))
    done
    printf 'timeout:%s' "${status}"
    return 1
}
