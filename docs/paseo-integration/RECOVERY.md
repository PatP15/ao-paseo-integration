# Recovery

What AO does when something dies. Verified against Paseo **v0.2.5** and AO **`main` @ 742c77bc**.

---

## 0. The recovery contract

Recovery **may** depend on:

```
AO's SQLite facts  +  git branch and commits  +  PR/CI state  +  structured checkpoints
```

Recovery **may not** depend on:

- the original Claude/Codex conversation,
- Paseo's transcript,
- an open terminal,
- an in-memory scheduler object,
- a specific AO frontend instance.

**Paseo's transcript is explicitly outside the contract.** `durableTimelineStore` is optional and is
never constructed by any caller in v0.2.5, so the timeline is in-memory only: it does not survive a
daemon restart, and on reload it is rebuilt from `streamHistory()` under a fresh `randomUUID()` epoch
with different row granularity. Anything AO may later need must be written as a durable AO fact when
it happens.

---

## 1. The three facts that shape every procedure below

### 1.1 Absence is UNKNOWN, never "absent"

`paseo ls` is **not** an exhaustive index. Any agent whose project placement cannot be resolved is
dropped *after* label filtering, under every flag combination. Three reachable causes:

1. `agent.workspaceId` is null (which is what a leaked `PASEO_AGENT_ID` produces),
2. the workspace record is gone,
3. **the project record is gone** — removing a project in the desktop app deletes the project while
   leaving workspaces (archived) and agents (labeled), permanently hiding every agent under it.

A fourth path is provider-gated: persisted records are filtered by provider availability, so
removing or renaming a provider hides its agents too.

`workspace ls` is equally lossy: it never returns archived workspaces and has **no protocol field**
to ask for them, so archived / project-archived / deleted are one indistinguishable zero.

**Therefore:** "0 results" never authorizes a create. Treat it as UNKNOWN and require corroboration
(§2.2).

### 1.2 Creation is not idempotent, and failure is not proof of non-creation

`workspace create` is documented as *"never deduplicates … it always produces a fresh workspace."*
Nothing constrains agent labels. And there are two distinct races:

- **R1 — the label lags the process.** `createSession` spawns the provider **before**
  `registerSession` makes the agent visible. For hundreds of ms (seconds cold), a real agent is
  running in AO's worktree while `ls --label` returns 0. **Check-then-create is a TOCTOU.**
- **R3 — failure leaves a zombie.** `promptFailure: "throw"` fires *after* the labeled agent is
  persisted. The cleanup handler's `createdAgentId` is still `undefined` and it never deletes an
  agent anyway. AO receives an error while a labeled agent that never got its prompt lives on,
  reporting `idle` forever.

### 1.3 `idle` is not completion

The status enum is exactly `initializing | idle | running | error | closed`. There is **no
completed/exited state**; `idle` conflates *finished*, *never started*, and *awaiting prompt*. The
discriminator AO wants — `attentionReason` (`finished | error | permission`) — exists on the wire and
is **dropped by both `ls --json` and `inspect --json`**.

---

## 2. AO restart

### 2.1 Boot sequence

1. Load all non-terminated sessions with execution bindings.
2. Load `execution_commands` in `pending` or `delivering`.
3. Probe each host: `GET /api/status`. Record `serverId` and version; **compare against the stored
   `server_id`** — a mismatch means the daemon was rebuilt or replaced, and its agent IDs are
   meaningless.
4. For each bound session with a known `external_agent_id`, call `inspect --json <id>` **directly**.
   This is the primary path and it works even when `ls` would drop the agent.
5. Ingest events from the current transport, deduplicating on `(session_id, event_id)`.
6. Resume command delivery from the outbox.
7. Recompute board state from durable facts.

**Never create a new agent because a probe failed.** A failed probe is an observation, not proof of
death — and `reconcileLive` currently early-returns above the liveness probe for sessions with no
local workspace, so remote adoption is a fork change, not existing behavior (see `ARCHITECTURE.md`
§5.1).

### 2.2 The one case that needs label reconciliation

AO died between creating a remote resource and persisting its ID. Only then is the ID unknown, and
only then do labels matter.

```
# spawn — workspace pre-created, env scrubbed, workspace always explicit
paseo --host <h> workspace create --isolation worktree --mode branch-off \
      --path <host_repo_path> --new-branch ao/<work-item>/<slug> \
      --base <base> --title "ao:<session>:<attempt>" --json
paseo --host <h> run --workspace <wks_id> -d --json \
      --label ao.session=<uuid> --label ao.attempt=<n> --label ao.intent=<uuid> \
      --label paseo.worktree=<uuid>:<n> "<prompt>"

# reconcile — only when the id was never persisted
paseo --host <h> ls -a -g --json --label ao.intent=<uuid>
```

`-a -g` is mandatory: **`ls -a` alone can never return an archived agent**, because omitting
`--global` sets `scope: "active"`, which hard-filters them.

Resolution rules:

| Result | Action |
|---|---|
| **1 match** | `inspect --json` it and verify **all** of: `.Worktree == "<uuid>:<n>"`, `.Cwd` equals the bound workspace `cwd`, `.Archived`/`.ArchivedAt`, `.CreatedAt` plausible. Only then adopt. |
| **0 matches** | **UNKNOWN.** Require, in the same poll, a successful `/api/status`, N consecutive absences, *and* a successful `workspace ls` containing the bound `workspaceId`. Then escalate to `attempt+1` with a fresh workspace and fresh labels — never reuse a key whose index may be permanently broken. |
| **>1 matches** | **UNRESOLVED.** Refuse to adopt automatically. `inspect` each for `CreatedAt` (never order by `ls`'s `created`, which is a human string like `"2 minutes ago"`), adopt the oldest, `stop` + `archive` the rest, and **flag the worktree as possibly corrupted** — two agents shared it. |

`.Worktree` is the verification channel: `inspect --json` exposes
`labels["paseo.worktree"]`, and `paseo.worktree` is a **dead key in v0.2.5** (one occurrence
tree-wide, this read; never written by the server). That makes it a free AO-owned read-back string —
**pinned to v0.2.5** and re-validated by the spike on every version bump, with
`agent update --json`'s full label map as the fallback.

### 2.3 Guarding against fail-open

A malformed `--label` (no `=`) makes `ls` apply **zero** filters and return **every agent on the
daemon** — while `run` hard-errors on the identical input. Before any exec, AO validates in Go:
exactly one `=`, non-empty key, **non-empty value** (an unset shell variable yields `k=""`, which is
accepted by both sides and collides across sessions), no duplicate keys, no shell metacharacters.

After parsing, re-assert that every returned agent carries the expected label. **Never act on an
unexpectedly large result set** — bail and log. And note `ls` does not paginate (server default 200,
`pageInfo` discarded), so broad orphan sweeps silently truncate.

---

## 3. Host outage

1. Mark the host temporarily unreachable. **The task retains ownership.**
2. `Alive` returns a **non-nil error**, never `(false, nil)` — see `ARCHITECTURE.md` §5.2 for why
   this is a data-integrity requirement and not a style preference.
3. Retry with exponential backoff. Answers and messages stay queued in the outbox.
4. On reconnect, verify `serverId`, then `inspect` the stored agent ID.

Work-zone tasks never fail over. Hobby tasks may fail over only after a lease expires **and** only
if the branch was pushed.

---

## 4. Paseo daemon restart

`workspaceId` (`wks_<hex>`) and agent IDs are minted once and persisted to the JSON registry with
awaited atomic writes, so **both survive a daemon restart**. What does *not* survive is the
transcript (§0).

Procedure: probe → `serverId` match → `inspect` the stored agent ID.

- Agent still exists → resume observation. Re-ingest events; duplicates are free.
- Agent gone, workspace exists → replacement session in the **same** workspace (§5). **Do not create
  another worktree.**

---

## 5. Agent gone, workspace survives

Create a new AO session bound to the **same** work item and the **same** Paseo workspace. New AO
session ID, new `attempt`, new labels, new `launchId`.

Context pack for the replacement:

- the original immutable run brief and the latest accepted brief version,
- current branch and head commit,
- PR and CI state,
- all structured checkpoints,
- answered questions, plus any still outstanding,
- pending review comments and CI failures,
- remaining retry budget,
- an explicit instruction not to redo completed work.

`run --workspace <archived-id>` **fails loudly** (`WORKSPACE_NOT_FOUND`), not silently — so a
stale binding surfaces as an error rather than a wrong-place launch.

---

## 6. Workspace gone, branch pushed

`workspace create --isolation worktree --mode checkout-branch --branch <branch>`, then launch a
replacement with the §5 context pack.

Because a vanished workspace is ambiguous (§1.1), AO does **not** infer "archived" and reuse the old
title. It escalates to `attempt+1` with a fresh title.

---

## 7. Workspace gone, unpushed work

AO cannot reconstruct unpushed files from conversation history — and cannot read them from Paseo's
transcript either, which may not exist (§0).

Therefore:

1. Mark the attempt **unrecoverable**.
2. Preserve every checkpoint, message, and answered question.
3. Move the work item to **Needs Human**.
4. Never claim resumability.

This is why `mayPushAssignedBranch` matters: an unpushed branch is a single point of loss. Hobby
policy pushes on checkpoint; work policy makes it configurable and accepts the risk explicitly.

---

## 8. Duplicate suppression

| Duplicate | Key | Mechanism |
|---|---|---|
| Dispatch | `execution_commands.idempotency_key` | one command per `(session, attempt, command_type)`; committed before any Paseo call |
| Remote agent | `ao.intent` label | post-hoc reconciliation only (§2.2) — **never** a pre-create gate |
| Inbound event | `(session_id, event_id)` unique index | emitter-minted ID; full replay is free |
| Outbound message | `[AO_MESSAGE:<uuid>]` marker | best-effort transcript grep; **design every message to be safe twice** |
| CI / review nudge | `(repo, pr, check_or_comment_id, head_sha)` | AO's existing `observe/scm` idempotency; head SHA is what makes a re-run distinct |

Paseo supplies **no** idempotency key on any create or send path. `clientMessageId` exists on the
wire but no CLI flag exposes it and the server threads it only to a logger — there is no store and no
"already created" branch. Every key above is AO's own.

---

## 9. Cleanup after merge

1. A human merges. AO observes it via `observe/scm`.
2. The work item lifecycle becomes `done`.
3. AO enqueues `archive_workspace`.
4. Paseo archives the workspace, its agents, and its terminals; it removes an owned worktree only
   after the final active reference is archived.
5. Branch deletion is a separate, explicit decision.

Note the current `Cleanup` path returns early **before** `runtime.Destroy` for sessions with no local
workspace, so a remote agent is not stopped and appears in neither `Cleaned` nor `Skipped`. Fixing
that is a listed upstream edit (`ARCHITECTURE.md` §5.1).

---

## 10. Restart matrix

| # | Scenario | Expected |
|---|---|---|
| 1 | AO dies before any Paseo call | outbox command executes after restart |
| 2 | AO dies after `workspace create`, before persisting the ID | reconcile by title; **no** second worktree |
| 3 | AO dies after `run`, before persisting the agent ID | reconcile by `ao.intent`; verify via `.Worktree`; adopt or escalate |
| 4 | `run` returns an error but created a labeled agent (R3) | sweep **on failure**, not only on timeout; adopt or stop the zombie |
| 5 | Two AO instances dispatch the same task | second claim rejected by the DB; if both fired, `>1` rule applies |
| 6 | Host offline | session unreachable, ownership retained, **not** terminated |
| 7 | Host returns | observation resumes from the stored agent ID |
| 8 | Paseo daemon restarts | IDs survive; transcript does not; events re-ingested idempotently |
| 9 | Agent exits unexpectedly | workspace preserved; retry policy applies; `idle` alone is not completion |
| 10 | Agent gone, workspace present | replacement in the same workspace |
| 11 | Workspace gone, branch pushed | new workspace via `checkout-branch` |
| 12 | Workspace gone, branch unpushed | **Needs Human**, no false resume |
| 13 | Project removed in Paseo's desktop app | agents permanently unlistable; `inspect` by stored ID still works; escalate if it does not |
| 14 | Human answer submitted while host offline | stored first, delivered on reconnect |
| 15 | Duplicate timeline event | ingested once |
| 16 | Duplicate CI event | one nudge |
| 17 | `serverId` changed under a stored host | treat all agent IDs on that host as invalid; escalate |

Rows 4, 13, and 17 are new relative to the original brief and exist because of §1.
