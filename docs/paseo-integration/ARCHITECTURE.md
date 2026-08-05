# Architecture

How AO drives stock Paseo installations as remote execution backends.

Verified against AO **`main` @ 742c77bc** and Paseo **v0.2.5**. Line references are to those trees.

---

## 1. Why this is not an `Agent` adapter

AO's `ports.Agent` contract yields an **argv**:

```go
GetLaunchCommand(ctx context.Context, cfg LaunchConfig) (cmd []string, err error)
```

`session_manager.Spawn` then does, in order: create the seed session row → `ports.Workspace.Create`
(a local git worktree) → provision → resolve the `Agent` adapter → `GetLaunchCommand` → validate the
binary with `LookPath` → `lifecycle.PrepareLaunch` → `ports.Runtime.Create(argv)` (a local tmux or
conpty process) → `lifecycle.MarkSpawned`.

Every step after the seed row assumes **this machine**: a local worktree path, a local binary on
`PATH`, a local PTY. A Paseo host owns the worktree and the process itself, and exposes them only as
opaque remote IDs. There is no argv AO can return that makes that work.

Hence a new port. `ExecutionBackend` is the only port in AO whose implementations do not run
locally, which is why every one of its methods may fail transiently and must be safe to retry
through the command outbox.

---

## 2. Ownership

| Concern | AO | Paseo | Git / forge |
|---|---|---|---|
| Projects, work graph, dependencies | **owns** | — | — |
| Proposals and approval | **owns** | — | — |
| Policy, schedule, concurrency | **owns** | enforces the selected mode | — |
| Host selection | **owns** | reports availability | — |
| Provider / model selection | **owns** | validates and launches | — |
| Worktree creation (remote runs) | — | **owns** | object/branch history |
| Agent process lifecycle | commands | **executes** | — |
| Raw transcript | optional projection | **owns** (non-durable — see §6) | — |
| Human questions and answers | **owns** | delivers | — |
| Permission decision | **owns** | enforces | — |
| Branch and commits | tracks | agent creates | **owns** |
| PR / CI / review facts | observes and reacts | agent fixes | **owns** |
| Retry policy, completion, cleanup | **owns** | reports evidence | performs deletion |
| Merge | declares ready | — | **human performs** |

### Invariants

1. Only one system creates the worktree.
2. Only AO decides whether a task is approved or complete.
3. Only AO schedules retries.
4. Instructions and answers are persisted in AO **before** being sent to Paseo.
5. A task is not complete because a process exited. Paseo's `idle` conflates *finished*,
   *never started*, and *awaiting prompt*.
6. A failure to contact a host is **not** task failure.
7. No task state exists only in a prompt, terminal, or provider conversation.
8. No automatic merge.

---

## 3. Shape

```
┌──────────────────────────────────────────────────────────────┐
│ AO fork — control plane (Apache-2.0)                         │
│                                                              │
│  work graph · approvals · scheduler · host router            │
│  command outbox · observer · question inbox · audit log      │
│  existing: observe/scm · lifecycle nudges · service/pr       │
│                                                              │
│  ports.ExecutionBackend ──┬── local AO path (unchanged)      │
│                           └── paseo backend                  │
└───────────────────────────┬──────────────────────────────────┘
                            │ outbound only: os/exec `paseo --host …`
                            │ + GET /api/status
                  Tailscale │ (relay offer URL as fallback)
        ┌───────────────────┴───────────────────┐
┌───────▼──────────────────┐  ┌─────────────────▼────────────┐
│ Linux worker             │  │ Windows / Unity worker        │
│ paseo daemon             │  │ paseo daemon                  │
│   --no-inject-mcp        │  │   --no-inject-mcp             │
│   --no-relay             │  │   --no-relay                  │
│ claude / codex · worktrees  │ claude / codex · worktrees    │
└───────┬──────────────────┘  └─────────────────┬────────────┘
        └──────────── branches / PRs ───────────┘
                            │
                      GitHub / GitLab
```

**AO makes only outbound connections.** It adds no network-facing bind — see
`docs/adr/0002-outbound-execution-backends.md` for why the inbound-push alternative was rejected.

`--no-inject-mcp` is **mandatory, not hygiene.** Paseo injects a flat 38-tool MCP catalog into every
agent (including `create_agent`, `create_schedule`, `create_heartbeat`, `kill_agent`,
`respond_to_permission`), gated only by a daemon-wide boolean. One AO-owned daemon per trust zone;
never the operator's `desktopManaged` daemon.

---

## 4. The port

`backend/internal/ports/execution.go`:

```go
// ExecutionBackend is a remote agent execution substrate: it owns workspace
// materialisation and process launch on another host, so AO records handles
// instead of creating a local worktree and a local runtime.
type ExecutionBackend interface {
	// Provision materialises the remote workspace for a session that already
	// has an id. Idempotency is best-effort on req.WorkspaceTitle; callers MUST
	// treat a duplicate as possible and reconcile (see DECISIONS.md D21).
	Provision(ctx context.Context, req ExecutionProvisionRequest) (ExecutionWorkspace, error)

	// Launch starts the agent in an already-provisioned workspace, tagging it
	// with req.IntentID for post-hoc reconciliation.
	Launch(ctx context.Context, req ExecutionLaunchRequest) (ExecutionAgent, error)
}

// ExecutionRuntime is the post-launch control surface, consumed by the runtime
// router rather than by the session manager.
type ExecutionRuntime interface {
	Stop(ctx context.Context, handle RuntimeHandle) error

	// Alive MUST return a non-nil error when the host is unreachable.
	// Returning (false, nil) is read as death and would violate
	// "never treat failed probes as death".
	Alive(ctx context.Context, handle RuntimeHandle) (bool, error)

	Output(ctx context.Context, handle RuntimeHandle, lines int) (string, error)
	SendMessage(ctx context.Context, handle RuntimeHandle, message string) error
}

type ExecutionObserver interface {
	Status(ctx context.Context, hostID string) (ExecutionHostStatus, error)
	ListOwned(ctx context.Context, hostID string) ([]ExecutionAgentObservation, error)
	Inspect(ctx context.Context, hostID, agentID string) (ExecutionAgentDetail, error)
}
```

Core packages never import Paseo types. The adapter lives at
`backend/internal/adapters/execution/paseo/` and is the only place that knows `paseo` exists.

**Handles are namespaced:** `paseo:<hostID>/<agentID>`, stored in
`SessionMetadata.RuntimeHandleID`. The namespace is what lets a runtime router dispatch remote
sessions without every caller learning about backends.

---

## 5. The seam, honestly scoped

An earlier draft claimed this was two hunks and sixteen lines, and that the local path would be
untouched. Verification refuted both. The real floor:

### 5.1 `session_manager/manager.go` — six branches, ~60–110 lines across 7 hunks

One unexported `externalSpawner` field on `Deps` (matching the file's existing `runtimeController` /
`lifecycleRecorder` idiom), a six-line branch in `Spawn`, and edits at five more gates that hard-code
local assumptions:

| Site | Current behavior | Needed |
|---|---|---|
| `Spawn` | falls through to local harness + runtime | branch before `m.agents.Agent(cfg.Harness)` |
| `:1154` `RestoreWithMode` | `WorkspacePath == ""` → `ErrIncompleteHandle` → HTTP 409 | remote branch |
| `:1198` `ResumeAgentWithMode` | same | remote branch |
| `:912` `Kill` | hard-errors if `runtime.Destroy` fails — **a host being down blocks termination** | error-tolerant |
| `:2220` `Cleanup` | `continue` fires **before** `runtime.Destroy`, so the remote agent is never stopped and appears in neither `Cleaned` nor `Skipped` | reorder + remote branch |
| `:1476` `reconcileLive` | early-returns **above** the `IsAlive` probe → **no boot adoption** | remote branch |
| `:1379` `SaveAndTeardownAll` | skips → shutdown never stops remote agents | decide and document |

The body goes in a **new in-package file**, `session_manager/spawn_external.go`. In-package is
required, not stylistic: `seedRecord`, `buildSpawnTexts`, `rollbackSpawnSeedRow`,
`markSpawnFailedTerminated`, and all of `prompt.go` are unexported (`prompt.go` has *zero* exported
functions). A sibling package would have to reimplement the spawn invariants and keep them
behaviourally identical across ~16 upstream commits/day. That is drift by construction.

`meta.WorkspacePath` stays `""`. `meta.Branch` **is** set — that single field is what makes
`observe/scm.discoverSubjects` find the session's PRs, so AO's existing CI/review nudge machinery
works on remote sessions with no further change.

### 5.2 `observe/reaper` — mandatory, and it is a data-integrity fix

The reaper probes every non-terminated session every 5s. Its only opt-out — an empty
`RuntimeHandleID` — also breaks `send` and `resume`, so it is not an option.

The kill chain: `lifecycle.Manager` treats a repeated same-state observation as a **no-op that does
not advance `LastActivityAt`**. A remote agent that stays `running` for ten minutes writes one
`ActivityActive`; 60s later `runtimeClearlyDead`'s recent-activity guard lapses; one `(false, nil)`
then sets `IsTerminated` and reaps containers.

`(false, nil)` is trivially produced over this CLI — a malformed `--label`, a colonless `--host`
falling through to the local daemon, or an archived agent all return **exit 0 with an empty result**,
indistinguishable from death. Hence two requirements: `Alive` must error rather than return
`(false, nil)`, and the reaper must become backend-aware. There is no adapter-only formulation.

### 5.3 `adapters/runtime/runtimeselect` — prevents a silent *local* regression

`tmux.Runtime` implements `ports.SupervisedProcessInspector` and `ports.RuntimeRestarter` but
declares only `var _ ports.Runtime` and `var _ ports.Attacher`. Both capabilities are discovered by
**type assertion on the concrete value**:

```go
// observe/reaper/reaper.go:76
if workload, ok := runtime.(ports.SupervisedProcessInspector); ok { r.workload = workload }
// session_manager/manager.go:1339
if restarter, ok := m.runtime.(ports.RuntimeRestarter); ok { return restarter.Restart(...) }
```

So wrapping `runtimeselect` in a router **silently strips both from local sessions**: local agents
whose process dies inside a live tmux pane would render `working` forever, and every local
resume-agent would mint a new handle and drop attached terminals. No compile error, no test failure.
The router must re-declare and delegate both, and `runtimeselect` must gain `var _` assertions so
this cannot regress.

### 5.4 `service/session/service.go:711`

`TerminalHandleID: rec.Metadata.RuntimeHandleID` — a non-empty handle makes the frontend attach, the
attach errors, and the reconnect loop flaps forever. Suppress for namespaced handles.

### 5.5 Three explicit refusals

Each currently **misbehaves silently** rather than erroring, which is worse than being unsupported:

- **`review/review.go:183`** — the reviewer engine is local-worktree-bound. Remote review needs a
  separate path; today it returns `ErrInvalid`.
- **`service/shellterm`** — an empty `WorkspacePath` resolves to the project root, opening a shell in
  **the operator's real main checkout**, labeled as that session's shell. A destructive-git hazard.
- **`observe/activity`** — harness-keyed. Reusing `harness = "codex"` (chosen to avoid widening the
  `sessions.harness` CHECK) makes it poll `GetOutput` on a remote handle and run a **tmux-pane regex
  over a Paseo transcript**, then write a bogus `ActivityIdle`. Capability lookups must not infer
  local capabilities from the harness value.

### 5.6 Non-goals (MVP)

No terminal attach. No diff / file-tree / workspace-watch read model (`workspace_files.go` `os.Stat`s
the local path). No reviewer engine. No spawn attachments. **No AO system prompt** — it is written to
a local file the argv references. No `restore` / `resume-agent`. No `no_signal` for remote sessions
(`FirstSignalAt` is set on the first observation, making it unreachable).

Each is a documented refusal with an error, not a silent degradation.

---

## 6. Observation

Outbound polling only. Cadence is set by measured per-command latency (spike S10) — `paseo` is a
shell shim that execs an Electron helper, and `logs` re-renders the entire transcript every call.

| Fact | Source | Maps to |
|---|---|---|
| liveness, crash | `inspect --json .Status` | `ApplyRuntimeObservation` |
| pending permission | `inspect --json .PendingPermissions` (full IDs) | `ActivityBlocked` |
| worktree, parentage | `inspect --json .Worktree`, `.ParentAgentId` | placement facts |
| host liveness, identity | `GET /api/status` (`serverId`, version) | `ProbeFailed` on error |
| agent events | terminal capture, else sentinel | `execution_events` |
| PR / CI / review | **AO's existing `observe/scm`** | unchanged |

`Blocked` vs `WaitingInput` is deliberate and load-bearing against `sessionguard`: a Paseo permission
must be `ActivityBlocked` so AO physically cannot answer it with text and must call `permit allow`;
an agent question must be `ActivityWaitingInput` so `send` still works while nudges stay suppressed.

**Paseo's transcript is not durable.** `durableTimelineStore` is never constructed in v0.2.5, so the
timeline dies with the daemon and is rebuilt from `streamHistory()` under a fresh epoch with
different row granularity. Anything AO may need later must be captured as a durable AO fact when it
happens.

---

## 7. Host registry and routing

`execution_hosts` + `execution_host_capabilities` + `project_host_bindings` (see `DATA_MODEL.md`).
Repository paths differ per machine, so a project is bound to a host with an explicit
`host_repo_path`; the MVP requires the clone to pre-exist.

Routing filters on trust zone, project allowlist, registered repo path, required capabilities,
online state, and free concurrency; then scores by preferred host, lowest load, most specific
capability match, and a stable host-ID tie-break.

Credentials are stored as **secret references**, never raw. A relay offer URL is a bearer credential.
`--host` accepts more forms than documented (`host:port`, a bare port, `tcp://…?ssl=true&password=…`,
`unix://`, `pipe://`, and an offer URL) and **returns `null` for any string without a colon, silently
falling through to the local daemon** — so AO validates host strings in Go before exec.

Every `paseo` invocation runs with `PASEO_AGENT_ID`, `PASEO_WORKSPACE_ID`, `PASEO_HOST`,
`PASEO_PASSWORD`, `PASEO_HOME`, and `PASEO_LISTEN` scrubbed, and always passes `--workspace <id>`
explicitly. A leaked `PASEO_AGENT_ID` silently makes every `run` a child agent sharing the parent's
workspace, and an agent with a null `workspaceId` is invisible to `ls` under every flag combination.

---

## 8. UI boundary

AO owns the portfolio, plan board, host health, and session inspector. It does **not** reimplement
Paseo's interactive terminal.

Full live interaction is a deep link: `paseo:/h/<serverId>/agent/<agentId>`, or
`https://app.paseo.sh/h/<serverId>/agent/<agentId>`, with `serverId` from `GET /api/status`.

Rationale: a faithful terminal replica needs the `/ws` protocol via `@getpaseo/client`, which is
explicitly *"not a stable public SDK"*, is AGPL-3.0-or-later, and would put a second runtime and an
unstable dependency on the critical path — for a view Paseo already renders well. The MVP shows
status, questions, permissions, checkpoints, and recent events, and hands off for the rest.

---

## 9. Licensing boundary

AO is Apache-2.0; Paseo is AGPL-3.0-or-later. The fork keeps them separate programs:

- Interact **only** via `os/exec` on the installed `paseo` binary, its `--json` output, and
  `GET /api/status`.
- **Never patch Paseo.** Running a modified AGPL daemon that serves clients over a network engages
  AGPL §13 directly. Gaps get upstreamed as PRs, not local patches.
- No `@getpaseo/*` dependency in `go.mod` or `package.json`; CI greps for it.
- No vendored or transcribed Paseo schemas — paraphrase with `file:line` citations.
- Any future `@getpaseo/client` sidecar lives in a **separate AGPL repo** with arms-length IPC.

Not legal advice; get counsel before distributing a combined product.
