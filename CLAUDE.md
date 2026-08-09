# CLAUDE.md

Read and follow [`AGENTS.md`](AGENTS.md) for repository layout, commands, coding conventions, and hard rules.

## App state lives under `~/.ao` only

All app state, the daemon's data dir, `running.json`, worktrees, and the Electron
supervisor's `userData` (Chromium cache, cookies, local/session storage, crash
dumps), must resolve under `~/.ao` (overridable via `AO_DATA_DIR`/`AO_RUN_FILE`).
Never write to or read from `~/Library/Application Support` or any other OS-default
app-data location. `frontend/src/main.ts` pins Electron's `userData` to
`~/.ao/electron`; do not remove that override. See the hard rule in `AGENTS.md`.

## Paseo loops must set thinking effort explicitly

`paseo loop run` cannot carry thinking effort — a protocol limitation (the loop
RPC schema has no thinking field), so loop-managed agents silently run at each
model's default, which is **low** for claude-opus-5 and medium for gpt-5.6-sol.
Never accept that for orchestration: rebuild the loop on `paseo run --thinking`
instead, per [`scripts/effort-loop.sh`](scripts/effort-loop.sh). Efforts come
from `~/.paseo/orchestration-preferences.json` (Opus verifiers at `xhigh`,
codex workers at `high`). Preflight-validate model + thinking IDs via
`paseo provider models <provider> --json` before launching, and guard against
usage-limit launch failures (two consecutive failures → stop or fall back;
`paseo loop` burns its entire iteration budget in minutes against that error).

## Design System

Always read [`DESIGN.md`](DESIGN.md) before making any visual or UI decision —
**start with the "clone agent-orchestrator verbatim" banner at the top**, which
governs the current look.

The renderer **clones the agent-orchestrator web app verbatim**
(`~/Projects/agent-orchestrator/packages/web/src`) in looks and design, with a
refined-blue accent and the terminal keeping its own palette. This **supersedes the
older design-reference framing** in DESIGN.md (per explicit user decision 2026-06-10).
Build new UI from shadcn primitives (`components/ui/*`) where a component fits. Do not
deviate without explicit user approval. In QA/review, flag any renderer code that
diverges from **agent-orchestrator** — do **not** re-flag old design-reference mismatches.

When showing or demoing frontend changes, run `ao preview [url]` from inside the
session so the change renders in the desktop browser panel (the inspector rail's
Browser tab); do not just describe it.
