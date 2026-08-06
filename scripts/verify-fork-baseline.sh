#!/usr/bin/env bash
#
# Fork verification gate: build, lint, typecheck, and "no NEW test failures".
#
# `go test ./...` cannot be used as a pass/fail gate directly, because this fork
# inherited failing tests from upstream. A naive check would never pass, and
# forcing them green would mean either fixing upstream's bugs or, worse, editing
# their tests. So this compares the SET of failing tests against a recorded
# baseline and fails only on a regression — a test that was passing and now is
# not.
#
# Baseline lives in scripts/known-failing-tests.txt, one test name per line.
# Update it only when you have confirmed upstream owns the failure.
#
# Usage:
#   ./scripts/verify-fork-baseline.sh              # everything
#   ./scripts/verify-fork-baseline.sh tests        # just the test gate
#   RACE=1 ./scripts/verify-fork-baseline.sh tests # with -race
#
set -uo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
readonly REPO
readonly BASELINE="${REPO}/scripts/known-failing-tests.txt"

export PATH="${PATH}:/opt/homebrew/bin:/usr/local/go/bin"

WHAT="${1:-all}"
rc=0

hdr() { printf '\n\033[1m== %s\033[0m\n' "$*"; }
ok()  { printf '  \033[32mOK\033[0m   %s\n' "$*"; }
bad() { printf '  \033[31mFAIL\033[0m %s\n' "$*"; rc=1; }

# --- build -----------------------------------------------------------------
if [ "${WHAT}" = "all" ] || [ "${WHAT}" = "build" ]; then
    hdr "go build"
    if (cd "${REPO}/backend" && go build ./... 2>&1 | tail -20); then
        ok "backend builds"
    else
        bad "backend build failed"
    fi
fi

# --- tests -----------------------------------------------------------------
if [ "${WHAT}" = "all" ] || [ "${WHAT}" = "tests" ]; then
    hdr "go test (regression gate)"
    out="$(mktemp)"
    flags=""
    [ -n "${RACE:-}" ] && flags="-race"
    # shellcheck disable=SC2086
    (cd "${REPO}/backend" && go test ${flags} ./... ) >"${out}" 2>&1

    # Collect failing test names, plus any package that failed to build.
    actual="$(grep -Eo '^--- FAIL: [A-Za-z0-9_/]+' "${out}" | sed 's/^--- FAIL: //' | sort -u)"
    buildfail="$(grep -E '\[build failed\]' "${out}" || true)"

    # Entries prefixed `flaky:` are expected to pass sometimes. They still
    # suppress a failure, but they do NOT trigger the "now passing, prune me"
    # note — otherwise a flaky test nags on every green run, and a gate that
    # cries wolf every time is a gate people stop reading.
    expected=""
    flaky=""
    if [ -f "${BASELINE}" ]; then
        expected="$(grep -Ev '^\s*(#|$)' "${BASELINE}" | sed 's/^flaky://' | sort -u)"
        flaky="$(grep -E '^flaky:' "${BASELINE}" | sed 's/^flaky://' | sort -u)"
    fi

    new="$(comm -23 <(printf '%s\n' "${actual}") <(printf '%s\n' "${expected}") | grep -v '^$' || true)"
    fixed="$(comm -13 <(printf '%s\n' "${actual}") <(printf '%s\n' "${expected}") | grep -v '^$' || true)"

    if [ -n "${buildfail}" ]; then
        bad "a package failed to build:"
        printf '%s\n' "${buildfail}" | sed 's/^/       /'
    fi
    if [ -n "${new}" ]; then
        bad "NEW test failures (not in baseline):"
        printf '%s\n' "${new}" | sed 's/^/       /'
        printf '\n  context:\n'
        for t in ${new}; do grep -A6 -- "--- FAIL: ${t}" "${out}" | sed 's/^/       /' | head -8; done
    elif [ -z "${buildfail}" ]; then
        ok "no new test failures"
    fi
    stale="$(comm -23 <(printf '%s\n' "${fixed}") <(printf '%s\n' "${flaky}") | grep -v '^$' || true)"
    if [ -n "${stale}" ]; then
        printf '  \033[33mNOTE\033[0m baseline entries now PASSING (prune them from %s):\n' \
            "scripts/known-failing-tests.txt"
        printf '%s\n' "${stale}" | sed 's/^/       /'
    fi
    rm -f "${out}"
fi

# --- lint ------------------------------------------------------------------
if [ "${WHAT}" = "all" ] || [ "${WHAT}" = "lint" ]; then
    hdr "golangci-lint"
    if (cd "${REPO}/backend" && go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.12.2 \
            run --path-mode=abs 2>&1 | tail -15 | grep -q '0 issues'); then
        ok "0 issues"
    else
        bad "lint issues (rerun manually for detail)"
    fi
fi

# --- typecheck -------------------------------------------------------------
if [ "${WHAT}" = "all" ] || [ "${WHAT}" = "typecheck" ]; then
    hdr "frontend typecheck"
    if (cd "${REPO}" && npm run frontend:typecheck >/dev/null 2>&1); then
        ok "tsc --noEmit clean"
    else
        bad "typecheck failed (run 'npm run frontend:typecheck' for detail)"
    fi
fi

# --- generated-artifact drift ---------------------------------------------
if [ "${WHAT}" = "all" ] || [ "${WHAT}" = "drift" ]; then
    hdr "generated artifact drift"
    # Regenerate and compare, rather than inspecting git state.
    #
    # The first version of this check was `git diff --quiet -- gen/`, which only
    # sees UNSTAGED changes. A worker that had staged everything sailed through
    # it vacuously — the check could not have caught stale generated code, which
    # was its entire purpose. Worse, checking against HEAD instead would fail
    # every legitimate in-progress storage PR, i.e. exactly when it needs to pass.
    #
    # Regenerating is the only formulation independent of staging state: if sqlc
    # output changes, gen/ was stale relative to the .sql sources, full stop. It
    # is idempotent when correct, and when it is not, it leaves the corrected
    # files in the tree, which is the fix anyway.
    before="$(git -C "${REPO}" status --porcelain -- backend/internal/storage/sqlite/gen)"
    if (cd "${REPO}" && npm run sqlc >/dev/null 2>&1); then
        after="$(git -C "${REPO}" status --porcelain -- backend/internal/storage/sqlite/gen)"
        if [ "${before}" = "${after}" ]; then
            ok "sqlc output matches queries (gen/ not stale)"
        else
            bad "gen/ was STALE — regenerating changed it. Review and commit gen/ with the query change."
            git -C "${REPO}" status --porcelain -- backend/internal/storage/sqlite/gen | sed 's/^/       /'
        fi
    else
        bad "npm run sqlc failed"
    fi
fi

printf '\n'
[ "${rc}" -eq 0 ] && printf '\033[32mVERIFY PASS\033[0m\n' || printf '\033[31mVERIFY FAIL\033[0m\n'
exit "${rc}"
