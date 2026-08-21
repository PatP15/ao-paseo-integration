#!/usr/bin/env bash
#
# Bring up an AO-owned Paseo daemon on THIS machine — a "computer" AO can
# dispatch approved work to — in the posture docs/paseo-integration/SECURITY.md
# §3 requires, then print the exact `ao remote register` line to run on the AO
# machine.
#
# A host needs Paseo (a Node CLI) and the agent CLIs it will run. It does NOT
# need Go, this repository, or the `ao` binary: those belong to the AO machine.
# The daemon started here is headless by design — no web UI, no relay, no MCP.
#
# Usage:
#   ./scripts/paseo-host-setup.sh                     # loopback only (default)
#   AO_HOST_LISTEN=100.64.0.5:6780 ./scripts/…        # reachable on one address
#   AO_HOST_ID=office-linux ./scripts/…               # id used in the register line
#   ./scripts/paseo-host-setup.sh status
#   ./scripts/paseo-host-setup.sh stop
#   ./scripts/paseo-host-setup.sh systemd-unit        # print a user unit
#
# Environment:
#   AO_HOST_HOME     PASEO_HOME for this daemon   (default ~/.paseo-ao)
#   AO_HOST_LISTEN   listen address               (default 127.0.0.1:6780)
#   AO_HOST_ID       host id for `ao remote`      (default <hostname>)
#
set -euo pipefail

# AO pins the CLI contract to one version: adapters/execution/paseo/version.go
# compares `paseo --version` for EQUALITY and refuses anything else, because the
# JSON shapes it parses are fixture-verified against this build alone.
readonly PINNED_PASEO_VERSION="0.2.5"

HOST_HOME="${AO_HOST_HOME:-${HOME}/.paseo-ao}"
LISTEN="${AO_HOST_LISTEN:-127.0.0.1:6780}"
HOST_ID="${AO_HOST_ID:-$(hostname | tr '[:upper:]' '[:lower:]')}"
PW_FILE="${HOST_HOME}/daemon-password"

die() { printf '\033[31merror\033[0m %s\n' "$*" >&2; exit 1; }
note() { printf '\033[36m--\033[0m %s\n' "$*"; }
ok() { printf '\033[32mok\033[0m %s\n' "$*"; }

# The default home belongs to the Paseo desktop app, whose daemon reports
# desktopManaged=true. AO refuses to drive that daemon outright
# (adapters/execution/paseo/backend.go), so pointing this script at ~/.paseo
# would produce a host that registers and then refuses every dispatch — and
# would fight the app over the same state. Keep the two separate.
[ "$(cd "$(dirname "${HOST_HOME}")" && pwd)/$(basename "${HOST_HOME}")" = "${HOME}/.paseo" ] &&
    die "refusing to use ~/.paseo: that is the desktop app's home, and AO refuses desktop-managed daemons. Pick another AO_HOST_HOME."

require_paseo() {
    command -v paseo >/dev/null 2>&1 ||
        die "paseo not on PATH. Install it with: npm install -g @getpaseo/cli@${PINNED_PASEO_VERSION}"
    local found
    found="$(paseo --version 2>&1 | tr -d '[:space:]')"
    [ "${found}" = "${PINNED_PASEO_VERSION}" ] ||
        die "paseo ${found} is installed but AO pins ${PINNED_PASEO_VERSION} exactly (it will report the host unsupported). Fix with: npm install -g @getpaseo/cli@${PINNED_PASEO_VERSION}"
    ok "paseo ${found}"
}

# Every AO invocation of the CLI scrubs Paseo's ambient variables (DECISIONS
# D23): a leaked PASEO_AGENT_ID silently reparents every new agent into the
# caller's workspace, where `ls` cannot see it. Do the same when starting the
# daemon so an inherited value cannot poison it.
scrubbed() {
    env -u PASEO_AGENT_ID -u PASEO_WORKSPACE_ID -u PASEO_HOST -u PASEO_SERVER_ID "$@"
}

write_config() {
    # Belt and braces for the flags below. These are PERSISTED defaults, and the
    # stock ones fail open: relay.enabled defaults true and dials out at boot,
    # and cors.allowedOrigins defaults to ["https://app.paseo.sh"] — any JS on
    # that origin gets a scopes:["*"] session on this daemon with no password.
    # A future `paseo daemon start` without the flags must not silently restore
    # either one.
    cat > "${HOST_HOME}/config.json" <<JSON
{
  "version": 1,
  "daemon": {
    "listen": "${LISTEN}",
    "cors": { "allowedOrigins": [] },
    "relay": { "enabled": false },
    "mcp": { "enabled": false, "injectIntoAgents": false },
    "browserTools": { "enabled": false }
  }
}
JSON
    chmod 600 "${HOST_HOME}/config.json"
}

start() {
    require_paseo
    mkdir -p "${HOST_HOME}"
    chmod 700 "${HOST_HOME}"

    if [ ! -f "${PW_FILE}" ]; then
        (umask 077; openssl rand -hex 24 > "${PW_FILE}")
        note "generated a new daemon password at ${PW_FILE}"
    fi
    chmod 600 "${PW_FILE}"
    local pw
    pw="$(cat "${PW_FILE}")"

    write_config

    case "${LISTEN}" in
        127.0.0.1:*|localhost:*|\[::1\]:*) ok "listening on loopback only" ;;
        0.0.0.0:*|:::*|\[::\]:*)
            printf '\033[33mwarning\033[0m %s\n' \
                "listening on ALL interfaces. SECURITY.md §3 says never a LAN interface: the daemon is plaintext HTTP with no TLS, and the password buys terminal write access. Bind one address (a Tailscale IP) instead." ;;
        *) printf '\033[33mwarning\033[0m %s\n' \
                "listening on ${LISTEN} — reachable off-box. Only do this over Tailscale or an equally private network; there is no TLS." ;;
    esac

    # PASEO_PASSWORD lands in the daemon's environment and stock Paseo 0.2.5
    # strips only five runtime-control keys before spawning an agent, so every
    # agent on this host can read it with `printenv` — and thus so can that
    # agent's model vendor (SECURITY.md §6). We do NOT patch the installed
    # Paseo (AGPL §13). Treat this password as compromised-by-design: scope it
    # to this daemon, never reuse it, and rotate it by deleting the file above.
    scrubbed \
        env PASEO_HOME="${HOST_HOME}" PASEO_PASSWORD="${pw}" \
        paseo daemon start --home "${HOST_HOME}" --listen "${LISTEN}" \
            --no-relay --no-mcp --no-inject-mcp --no-web-ui >/dev/null

    local probe_host="${LISTEN}"
    case "${LISTEN}" in 0.0.0.0:*|:::*|\[::\]:*) probe_host="127.0.0.1:${LISTEN##*:}" ;; esac

    local i
    for i in $(seq 1 45); do
        curl -sf -m 2 "http://${probe_host}/api/health" >/dev/null 2>&1 && break
        sleep 1
    done
    local status
    status="$(curl -sf -m 5 -H "Authorization: Bearer ${pw}" "http://${probe_host}/api/status" || true)"
    [ -n "${status}" ] || die "daemon did not answer /api/status. Logs: ${HOST_HOME}/daemon.log"
    ok "daemon up: ${status}"

    printf '\n\033[1mOn the AO machine\033[0m, store the password as a secret ref and register this computer:\n\n'
    cat <<EOF
  mkdir -p ~/.ao/secrets && chmod 700 ~/.ao/secrets
  printf '%s' '<the contents of ${PW_FILE} on this host>' > ~/.ao/secrets/${HOST_ID}-pw
  chmod 600 ~/.ao/secrets/${HOST_ID}-pw

  ao remote register ${HOST_ID} --name "${HOST_ID}" \\
    --transport tailscale --endpoint ${LISTEN} \\
    --secret-ref ${HOST_ID}-pw --trust-zone hobby --max-sessions 3

  ao remote bind <project-id> ${HOST_ID} \\
    --host-path <absolute path of that repo ON THIS HOST> --base-branch main
EOF
    printf '\nCopy the password over a private channel; never paste it into --endpoint or a command AO records.\n'
}

status_cmd() {
    [ -f "${PW_FILE}" ] || die "no daemon password at ${PW_FILE}; run 'start' first"
    local probe_host="${LISTEN}"
    case "${LISTEN}" in 0.0.0.0:*|:::*|\[::\]:*) probe_host="127.0.0.1:${LISTEN##*:}" ;; esac
    curl -sf -m 5 -H "Authorization: Bearer $(cat "${PW_FILE}")" "http://${probe_host}/api/status" ||
        die "no answer from http://${probe_host}/api/status"
    printf '\n'
}

stop_cmd() {
    require_paseo
    # Addressed by --home, which cannot reach the desktop app's daemon.
    scrubbed env PASEO_HOME="${HOST_HOME}" paseo daemon stop --home "${HOST_HOME}"
}

systemd_unit() {
    # WSL needs systemd=true in /etc/wsl.conf for this, and
    # `loginctl enable-linger $USER` for it to survive logout.
    cat <<EOF
# ~/.config/systemd/user/paseo-ao.service
[Unit]
Description=AO-owned Paseo daemon (headless execution backend)
After=network-online.target

[Service]
Type=forking
Environment=PASEO_HOME=${HOST_HOME}
EnvironmentFile=${HOST_HOME}/daemon.env
# PATH must be explicit: a unit does not read your shell profile, so an
# nvm-installed paseo and the agent CLIs are invisible without it.
Environment=PATH=$(dirname "$(command -v paseo 2>/dev/null || echo /usr/bin/paseo)"):/usr/local/bin:/usr/bin:/bin
ExecStart=$(command -v paseo 2>/dev/null || echo paseo) daemon start --home ${HOST_HOME} --listen ${LISTEN} --no-relay --no-mcp --no-inject-mcp --no-web-ui
ExecStop=$(command -v paseo 2>/dev/null || echo paseo) daemon stop --home ${HOST_HOME}
Restart=on-failure

[Install]
WantedBy=default.target
EOF
    printf '\n# Write %s/daemon.env with mode 600 first:\n#   PASEO_PASSWORD=<contents of %s>\n' \
        "${HOST_HOME}" "${PW_FILE}"
    printf '# Then: systemctl --user daemon-reload && systemctl --user enable --now paseo-ao\n'
}

case "${1:-start}" in
    start) start ;;
    status) status_cmd ;;
    stop) stop_cmd ;;
    systemd-unit) systemd_unit ;;
    *) die "unknown command '${1}'. Use: start | status | stop | systemd-unit" ;;
esac
