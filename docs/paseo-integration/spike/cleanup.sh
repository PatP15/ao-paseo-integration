#!/usr/bin/env bash
#
# Idempotent teardown for the Paseo compatibility spike.
#
# Safe to run repeatedly, and safe to run when nothing is up. It only ever
# touches the THROWAWAY daemon and the disposable repo. It never uses `--all`,
# and it never stops, restarts, or reconfigures the operator's daemon.
#
set -uo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
readonly HERE

PASEO_BIN="${PASEO_BIN:-$(command -v paseo || true)}"
SPIKE_ROOT="${SPIKE_ROOT:-${TMPDIR:-/tmp}/ao-paseo-spike}"
SPIKE_HOME="${SPIKE_ROOT}/paseo-home"
SPIKE_PORT="${SPIKE_PORT:-6799}"
SPIKE_HOST="127.0.0.1:${SPIKE_PORT}"
SPIKE_PASSWORD="${SPIKE_PASSWORD:-}"
FIXTURES="${HERE}/fixtures"
VERDICTS="${SPIKE_ROOT}/verdicts.tsv"
readonly PASEO_BIN SPIKE_ROOT SPIKE_HOME SPIKE_PORT SPIKE_HOST SPIKE_PASSWORD
readonly FIXTURES VERDICTS

# shellcheck source=lib/common.sh
. "${HERE}/lib/common.sh"

printf '\n\033[1m== Cleanup\033[0m\n'

if [ "${SPIKE_PORT}" = "${OPERATOR_PORT}" ] || [ "${SPIKE_HOME}" = "${HOME}/.paseo" ]; then
    printf 'refusing: cleanup is pointed at the operator daemon\n' >&2
    exit 1
fi

# 1. Archive the spike's agents individually. Never `--all`.
if [ -n "${PASEO_BIN}" ] && curl -fsS "http://${SPIKE_HOST}/api/health" >/dev/null 2>&1; then
    ids=$(pz ls -a -g --json 2>/dev/null \
          | python3 -c 'import json,sys
try:
    print(" ".join(a["id"] for a in json.load(sys.stdin)))
except Exception:
    pass' 2>/dev/null || true)
    for id in ${ids}; do
        log "stopping ${id}"
        pz stop "${id}" >/dev/null 2>&1 || true
        pz archive "${id}" --force >/dev/null 2>&1 || true
    done

    wids=$(pz workspace ls --json 2>/dev/null \
           | python3 -c 'import json,sys
try:
    print(" ".join(w["workspaceId"] for w in json.load(sys.stdin)))
except Exception:
    pass' 2>/dev/null || true)
    for wid in ${wids}; do
        log "archiving workspace ${wid}"
        pz workspace archive "${wid}" >/dev/null 2>&1 || true
    done
fi

# 2. Stop the throwaway daemon by its own home. `daemon stop` takes --home,
#    not --host, so this cannot reach the operator's daemon.
if [ -n "${PASEO_BIN}" ]; then
    log "stopping throwaway daemon (--home ${SPIKE_HOME})"
    "${PASEO_BIN}" daemon stop --home "${SPIKE_HOME}" >/dev/null 2>&1 || true
fi

# 3. Remove the disposable tree.
case "${SPIKE_ROOT}" in
    /|"${HOME}"|"${HOME}/") printf 'refusing to remove %s\n' "${SPIKE_ROOT}" >&2; exit 1 ;;
    *ao-paseo-spike*) rm -rf "${SPIKE_ROOT}" && log "removed ${SPIKE_ROOT}" ;;
    *) printf 'refusing: SPIKE_ROOT=%s does not look like a spike dir\n' "${SPIKE_ROOT}" >&2 ;;
esac

# 4. Prove the operator's daemon is untouched.
before="${FIXTURES}/s0-operator-status-before.json"
if [ -f "${before}" ]; then
    # Write the "after" snapshot OUTSIDE SPIKE_ROOT: step 3 above just deleted
    # that directory, so `curl -o "${SPIKE_ROOT}/after.json"` cannot create its
    # output file and fails — which this block then reported as "operator daemon
    # not responding". It fired on all three spike runs while the daemon was
    # provably healthy, i.e. a false alarm on the one check whose entire job is
    # to prove we did no harm.
    after_file="$(mktemp -t ao-spike-after)"
    if curl -fsS "http://127.0.0.1:${OPERATOR_PORT}/api/status" -o "${after_file}" 2>/dev/null; then
        if python3 - "${before}" "${after_file}" <<'PY'
import json, sys
a = json.load(open(sys.argv[1])); b = json.load(open(sys.argv[2]))
same = a.get("serverId") == b.get("serverId")
print(("  OK   operator daemon unchanged (serverId %s)" % a.get("serverId"))
      if same else "  WARN operator daemon serverId CHANGED")
sys.exit(0 if same else 1)
PY
        then :; else printf '  investigate before trusting the fixtures\n'; fi
        rm -f "${after_file}"
    else
        printf '  NOTE operator daemon not responding now; it was up at S0\n'
    fi
fi

printf 'Cleanup complete. Fixtures kept in %s\n' "${FIXTURES}"
