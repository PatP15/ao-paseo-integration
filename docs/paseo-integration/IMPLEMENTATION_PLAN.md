# Implementation plan

Small, independently reviewable pull requests. **PR 0 is this documentation set plus the
compatibility spike; no production code.** Work stops after PR 0 for review, because the spike may
change the transport decision in PR 10.

---

## Fork maintenance

Upstream runs **~16 commits/day and accelerating** (9.1 → 13.1 → 16.1 over 90/30/7 days), with linear
squash-merge history and zero merge commits. `session_manager/manager.go` alone took 65 commits in 90
days.

| Rule | Detail |
|---|---|
| **Rebase weekly, not monthly** | One week ≈ 27 backend commits; 90 days ≈ 347. Rebase, never merge — upstream history is linear. |
| **Keep the `go.mod` module path** | `github.com/aoagents/agent-orchestrator/backend` is stale (301s to an unrelated repo) but nothing fetches it — every build is in-place. Renaming touches **1,032 occurrences across 415 files** (68% of all Go files) plus 31 `sqlc.yaml` overrides. |
| **Never merge generated artifacts** | `openapi.yaml`, `frontend/src/api/schema.ts`, `storage/sqlite/gen/`. Take upstream, re-run `npm run api` / `npm run sqlc`. |
| **New packages, not edits** | Everything possible lives in new files. The seam PR lands **last** and stays ≤110 lines. |
| **Migration block `0900`–`0949`** | `0042` collides within ~1.6 days. See `DATA_MODEL.md` §1. |
| **Fix `releaseRepo`** | `backend/internal/cli/start.go:25` is `"AgentWrapper/agent-orchestrator"`, which 301s to upstream — an unmodified fork's `ao start` **downloads and launches upstream's desktop app**. Override via the documented ldflags path. |

### Baseline at `main` @ 742c77bc

Established before any feature code, so later regressions are unambiguous:

- `go build ./...` — **green** (19s)
- `go test ./...` — 49 packages ok, **1 pre-existing failure**:
  `TestWorkspaceFilesIncludeWorkspaceProjectChildRepoDiffs` (`service_test.go:673`)
- `go test -race ./...` — **3 pre-existing failures**: the above, plus
  `TestAuthStatusUnknownWhenKeyOnlyComesFromInteractiveShell` (kilocode) and
  `TestOpenCodeAuthStatusUnknownWithZeroCredentials` (opencode) — both exactly 3.00s, i.e. a 3s
  deadline that `-race` timing trips

All upstream. Recorded, not fixed.

---

## PR sequence

Ordered so the riskiest work (the seam, in the churniest file) lands **last**, and the mandatory
safety fix lands **before** any remote session can exist.

### PR 0 — `docs: paseo integration design, audit, and compatibility spike`

Seven design docs + `VULNERABILITIES.md` + ADR 0002 + the spike. No Go, no generated artifacts.

**Acceptance:** every capability claim cites a fixture path or a `file:line`; the `0900` numbering rule
and the do-not-widen-`sessions.harness` rule are stated with their evidence; the licensing hard rules
are stated; no Paseo source is pasted (paraphrase + citations only); spike scripts pass `shellcheck`
and are not wired into CI; `gitleaks` clean.

**Deps:** none.

### PR 1 — `feat(ports): ExecutionBackend port and fork-owned vocabulary`

`ports/execution.go`, `domain/execution.go`, `domain/execution_host.go`. Pure types and interfaces.

**Acceptance:** no Paseo types in core packages; `Alive` documents the must-error-on-unreachable
contract; compiles with no other change.

### PR 2 — `feat(storage): execution and work-graph tables (0900 block)`

Migrations `0900`–`0940`, queries, sqlc regeneration, store wrappers.

**Acceptance:** applies to a fresh DB **and** to one already at `0041` with a future version seeded;
`sessions.harness` untouched; no `board_column` column anywhere; the one-active-implementer unique
index is enforced by a test that tries to double-claim; `npm run sqlc` output committed in the same
commit.

**Deps:** PR 1.

### PR 3 — `fix(observe): reaper and runtime-capability safety` ⚠️ **blocking**

Not a Paseo feature — a prerequisite. Two independent defects:

1. The reaper probes every session every 5s, and `lifecycle.Manager` treats a repeated same-state
   observation as a **no-op that does not advance `LastActivityAt`**. A session that stays `running`
   freezes its activity clock; 60s later the recent-activity guard lapses and one `(false, nil)` sets
   `IsTerminated` and reaps containers. Make the reaper backend-aware.
2. Wrapping `runtimeselect` in a router **silently strips** `ports.SupervisedProcessInspector` and
   `ports.RuntimeRestarter` from **local** sessions, because both are discovered by type assertion
   (`reaper.go:76`, `manager.go:1339`) and `tmux.Runtime` declares neither. Add `var _` assertions and
   make the router delegate both.

**Acceptance:** a test proving a remote-shaped session is not terminated by an ambiguous probe; a test
proving both capabilities survive the router; existing local reaper tests unchanged.

**Deps:** PR 1.

### PR 4 — `feat(paseocli): Paseo CLI process wrapper, fixture-driven`

`adapters/execution/paseo/{client,cli_runner,commands,parser,mapping,version,secrets}.go` + fixtures
captured by the spike.

**Acceptance:** argv arrays only, never a shell string; validates labels (one `=`, non-empty key **and
value**) and host strings (must contain a colon) **before** exec; scrubs `PASEO_*` from the child env;
bans `--all` on `stop`/`delete`; always `-a -g`; strict JSON parsing; version recorded and unsupported
versions refused; redacts offer URLs and passwords in all output including errors; retry
classification distinguishes network / auth / invalid-request / unsupported-version / provider-
unavailable / workspace-error.

**Deps:** PR 1.

### PR 5 — `feat(execution/paseo): ExecutionBackend implementation`

Two-step create (`workspace create --json` → capture `workspaceId` → `run --workspace <id>`), because
`run --json` does not return a workspace ID.

**Acceptance:** exactly one remote worktree per attempt; external IDs persisted before use; a
timed-out create reconciles by `ao.intent` label rather than retrying blind; **treats a hard `run`
failure as possibly-created** and sweeps; verifies an adoption candidate via `inspect --json`
(`.Worktree`, `.Cwd`, `.Archived`, `.CreatedAt`) before binding; refuses a host whose
`desktopManaged` is true or whose `server_id` changed.

**Deps:** PR 2, PR 4.

### PR 6 — `test(execution): deterministic fake backend`

Following `adapters/agent/fake`'s established pattern.

**Acceptance:** covers every lifecycle op plus the failure modes — host unreachable, ambiguous empty
result, duplicate match, zombie-after-failure. Unit tests never require a live daemon.

**Deps:** PR 1.

### PR 7 — `feat(runtime): namespaced handles and runtime router`

`paseo:<hostID>/<agentID>` dispatch, so `Kill`, `Send`, `Cleanup`, and the reaper work on remote
sessions without every caller learning about backends.

**Acceptance:** local handles route unchanged; both type-asserted capabilities preserved (PR 3);
`TerminalHandleID` suppressed for namespaced handles at `service/session/service.go:711` so the
frontend does not attach-error-reconnect forever.

**Deps:** PR 3, PR 5.

### PR 8 — `feat(dispatch): durable outbox and host router`

Approval → one session, one dispatch command, committed **before** any Paseo call. Router filters on
trust zone, allowlist, registered repo path, capabilities, online state, concurrency; scores by
preferred host, load, capability specificity, stable tie-break.

**Acceptance:** AO killed at each of the four dispatch checkpoints creates no duplicate agent;
per-session FIFO ordering holds; a second claim on a held work item is rejected by the DB.

**Deps:** PR 2, PR 5.

### PR 9 — `feat(observe/paseo): polling observer and reconciliation`

**Acceptance:** status/permission/worktree observations map to AO facts; a host outage never
terminates a session; missed events are ingested after restart; duplicates ignored;
orphaned agent/workspace pairs surfaced; **`idle` never treated as completion**; polling cadence taken
from the spike's measured latency.

**Deps:** PR 3, PR 5.

### PR 10 — `feat(paseoevent): agent→control-plane event channel` — **shape set by the spike**

Rung 0 (`terminal capture` cursors) if S1f passes; rung 1 (sentinel, advisory) otherwise; rung 2
(no channel) always works.

**Acceptance:** immutable brief stored before launch; per-launch **nonce** so AO cannot ingest its own
example; emitter-minted `eventId` dedupe; `seq` gap **detection** only; raw line stored before apply;
malformed lines counted and dropped, never partially applied; events never authorize kill / cleanup /
merge / force-push / `permit allow`; **never** `--filter`, `--tail`, or `-f` for ingest.

**Deps:** PR 9. **Blocked on the spike.**

### PR 11 — `feat(api): hosts, dispatch, questions, permissions`

Controllers + spec + CLI + regenerated artifacts.

**Acceptance:** `npm run api` output committed together; permission decisions always send the **full**
request ID from `inspect`; UI granularity does not exceed what Paseo enforces.

**Deps:** PR 8, PR 9.

### PR 12 — `feat(session_manager): branch to an ExecutionBackend`

The seam. One `Deps` field, one `Spawn` branch, five more gates
(`Restore:1154`, `ResumeAgent:1198`, `Kill:912`, `Cleanup:2220`, `reconcileLive:1476`), plus a decision
on `SaveAndTeardownAll:1379`. Body in a new in-package file `spawn_external.go`.

**Acceptance:** local spawn/kill/restore/resume/cleanup byte-identically unchanged (existing tests
pass untouched); `WorkspacePath` stays `""` and `Branch` is set; `Kill` succeeds when the host is
down; ~60–110 lines across 7 hunks in `manager.go`.

**Deps:** PR 7, PR 8.

### PR 13 — `feat(policy): explicit refusals and non-goals`

Turn three silent misbehaviours into errors: `review/review.go` (reviewer engine is
local-worktree-bound), `service/shellterm` (empty `WorkspacePath` opens a shell in the operator's
**real main checkout**), `observe/activity` (harness-keyed, would run a tmux-pane regex over a Paseo
transcript and write a bogus `ActivityIdle`).

**Acceptance:** each returns a clear unsupported error; no silent degradation; non-goals documented.

**Deps:** PR 12.

### PR 14 — `feat(security): hardening from the audit`

The blocking items from `VULNERABILITIES.md` §6 that are code rather than deployment: fence and cap
review-comment nudges, `SanitizeControlChars` on the two missing paths, extend `domain/text.go` to
strip Cf and U+E0000–E007F, telemetry compiled off, codex permission default off bypass, narrow the
reviewer `gh` allowlist, patch the compiled-in push/PR instruction, `rm -rf skills/bug-triage/`.

**Acceptance:** prompt-injection fixtures prove fencing; a Cf/TAG test proves sanitization; telemetry
off survives a packaged Dock launch; trust-zone tests; secret-redaction tests; the restart / offline /
lost-agent / lost-workspace matrices from `RECOVERY.md` §10.

**Deps:** PR 12. Several items are deployment config, not code — see `SECURITY.md` §2–3.

### Deferred

Planner (natural-language goal → proposed work items), scheduler and policy profiles, portfolio and
host UI, host-registry credential management. All sit behind the execution path because none can be
validated until a remote session actually runs.

---

## Test matrix

| Scenario | Expected |
|---|---|
| Local AO session | behaves exactly as before |
| Remote session | one remote worktree, one agent |
| Same task dispatched twice | second claim rejected |
| AO dies before the Paseo call | outbox command executes after restart |
| AO dies after workspace create | reconciles by title; no duplicate |
| AO dies after agent create, before persisting the ID | found by `ao.intent`, verified by `.Worktree`, or escalated |
| `run` errors but created a labeled agent | swept on failure, not only on timeout |
| Host offline | unreachable; ownership retained; **not terminated** |
| Host returns | observation resumes from the stored ID |
| Paseo daemon restarts | IDs survive; transcript does not; events re-ingested once |
| Agent exits | workspace preserved; `idle` alone is not completion |
| Agent gone, workspace present | replacement in the same workspace |
| Workspace gone, branch pushed | new workspace via `checkout-branch` |
| Workspace gone, branch unpushed | **Needs Human**, no false resume |
| `server_id` changed | all agent IDs on that host invalid; escalate |
| Human answer while offline | stored, delivered on reconnect |
| Duplicate timeline / CI event | ingested once / one nudge |
| Permission request | blocked, correct scope, full ID |
| Work project | never auto-launches |
| Hobby project outside window | stays queued |
| Follow-up proposal | proposed, never auto-run |
| Reviewer | cannot modify files |
| Merge | observed, then archived |
| Prompt injection in repo / PR comment | cannot change policy, host, permission mode, scheduling, approval, or retry budget |
| Malformed Paseo JSON | adapter fails safely |
| Unsupported Paseo version | clear degraded/unsupported state |

Every row uses injected fakes and fixtures. **No unit test requires a live daemon.**

Per-PR: narrowest tests first, then `cd backend && go test ./...`, `go test -race ./...`,
`npm run lint`, `npm run frontend:typecheck`. `npm run sqlc` after schema/query changes and
`npm run api` after API changes, with generated artifacts committed in the same commit.
