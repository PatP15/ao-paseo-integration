# Remote execution in the dashboard

The UI plan for the Paseo integration: everything after initial worker setup
happens in the desktop app, no terminal. Grounded in what is implemented on
`paseo-integration` as of 2026-08-08 (G1/G2/G4 landed, G5 open).

**The one allowed terminal moment** is starting the Paseo daemon *on the
worker machine* — AO cannot reach a machine that isn't running anything yet.
Everything AO-side (register, bind, create, approve, dispatch, answer, decide)
must work from the dashboard.

---

## 1. Ground truth

### API that exists today (all registered in specgen, present in `frontend/src/api/schema.ts`)

| Surface | Route |
|---|---|
| List hosts (with `reachable`, probe timestamps, `activeSessions`, `paseoVersion`, `serverId`) | `GET /api/v1/execution/hosts` |
| Register / replace host (`enabled` flag → disable is a PUT) | `PUT /api/v1/execution/hosts/{hostId}` |
| Bind project → host (host path, base branch, priority) | `PUT /api/v1/execution/projects/{projectId}/hosts/{hostId}` |
| Dispatch (returns `commandId`, `commandState`, `sessionId`, `attempt`, `intentId`) | `POST /api/v1/execution/dispatch` |
| Human inbox: open questions + permission requests | `GET /api/v1/execution/questions` |
| Answer an agent question | `POST /api/v1/execution/questions/{questionId}/answer` |
| Allow/deny a host permission request | `POST /api/v1/execution/permissions/{questionId}/decision` |
| Create work item / approve / list | `POST /api/v1/work-items`, `POST /api/v1/work-items/{id}/approval`, `GET /api/v1/work-items?projectId=` |
| Notifications + SSE stream (existing) | `GET /api/v1/notifications`, `GET /api/v1/notifications/stream` |

### Renderer that exists today

- TanStack Router routes under `frontend/src/renderer/routes/` — including
  `_shell.settings.tsx` (global settings) and
  `_shell.projects.$projectId_.settings.tsx` (project settings).
- `openapi-fetch` client (`renderer/lib/api-client.ts`) typed off the generated
  `schema.ts` — the execution/work-item types are **already generated**, nothing
  consumes them.
- shadcn primitives in `components/ui/`: table, sheet, dialog, select, switch,
  badge, tabs, card, dropdown-menu, tooltip — enough for every screen below.
- `NotificationCenter`, `GlobalNewTaskDialog`/`NewTaskDialog`,
  `ProjectSettingsForm`, `GlobalSettingsForm`, `ConnectMobileModal` (precedent
  for a settings-launched modal with a copy-paste step).
- DESIGN.md: clone agent-orchestrator verbatim, refined-blue accent, build
  from `components/ui/*`. Demo via `ao preview`.

### Holes that force backend work (each blocks a UI screen; none are deferred)

1. **No secrets API.** `RegisterExecutionHostRequest` deliberately refuses
   inline credentials; refs resolve to files under `$AO_DATA_DIR/secrets`
   (`daemon/execution_wiring.go:55`). Today only `printf > file` creates one.
   The Add Computer dialog needs `POST` a credential → get back a ref.
2. **No probe-on-demand.** `reachable` is derived from the observer's last
   tick. "Test connection" in a dialog needs a synchronous probe. This is also
   the natural home for **G5** (refuse a host whose `serverId` equals the local
   daemon's) — do both in one endpoint and at register time.
3. **No bindings read.** The bind PUT exists; nothing lists bindings, so
   neither the Computers pane nor Project Settings can show what is bound.
4. **Sessions don't say they're remote.** `ControllersSessionView` has no
   `hostId`/backend field, so the board cannot badge a remote session or link
   to its host.
5. **No command status read.** Dispatch returns a `commandId` and
   `commandState` snapshot; the UI cannot watch `pending → delivering →
   acknowledged` (or a G2 escalation) afterwards.
6. **No per-host provider/model/mode discovery.** D5: mode strings are
   provider-specific and must be validated per provider via
   `provider ls --json` / `provider models <p> --json`. Without an endpoint the
   dispatch form is guess-the-string.
7. **Approval endpoint cannot reject.** `approvalState` already includes
   `rejected`; the service only flips to `approved`.
8. **No execution-events read.** Remote sessions have no local PTY
   (`WorkspacePath` stays `""`, D2), so the session detail page needs the
   durable `execution_events` (G4 reports) as its timeline. Rows exist; no
   endpoint.
9. **Questions don't notify.** The inbox fills silently; the existing
   notification pipeline (store + SSE + `NotificationCenter`) isn't wired to
   execution questions or permission requests.
10. **Skills and orchestration preferences are host-local files with no RPC.**
    The operator's skills (`paseo-loop`, `paseo-advisor`, `paseo-committee`,
    `paseo-handoff`, …) live in `~/.claude/skills/` on each host;
    per-user orchestration preferences live in
    `~/.paseo/orchestration-preferences.json` (a `providers` role map —
    `impl`/`ui`/`research`/`planning`/`audit` — plus freeform `preferences`
    strings that Paseo skills read before choosing a provider). Paseo exposes
    **no API for either** — they are files. AO does, however, already own a
    deterministic exec channel on the host: the G4 reporter is an AO-owned
    binary started in a Paseo terminal (`CreateTerminal` / `SendTerminalKeys`
    in `adapters/execution/paseo`), with checksummed `paseoevent` frames read
    back via `CaptureTerminal`. Extending that binary is the channel; no new
    network surface, no LLM in the byte path.
11. **Host schedules are invisible to AO.** D6 says AO-managed agents must not
    create Paseo schedules/heartbeats, and cannot be per-session enforced —
    only the daemon-wide `--no-inject-mcp` posture prevents the MCP path.
    Schedules remain enumerable (`paseo schedule ls`); **heartbeats are not**
    (documented blind spot). Nothing in AO reads either today, so a policy
    violation on a worker would be silent.
12. **Agent instruction files are shown nowhere.** CLAUDE.md / AGENTS.md exist
    at three scopes, and no AO surface displays any of them:
    - **Machine scope:** `~/.claude/CLAUDE.md` on each host (and on the AO
      machine) — the user-level instructions every Claude session there reads.
    - **Project scope (canonical):** `CLAUDE.md` / `AGENTS.md` at the repo
      root, versioned in git. The canonical copy is whatever the base branch
      says; AO can read it locally because every project has a local path.
    - **Host-checkout copies:** each bound host's checkout at
      `host_repo_path` carries its own copy, which is *stale git state*, not
      an independent document — a worktree branched from a stale base gets the
      stale instructions.
    The same three-scope split applies to skills: `~/.claude/skills/` per
    machine and `.claude/skills/` per repo. Today the only code touching any
    of this is write-side: agent adapters materialize harness-specific
    instruction files into worktrees at spawn (`adapters/agent/{kimi,copilot,
    cursor}`), and `skillassets` installs AO's own skill. Nothing reads them
    for display.

---

## 2. Target UX

### The headline path: "Run on" in New Task

The single most important change for ease of use. `GlobalNewTaskDialog` gains a
**Run on** select: *This computer* (default, existing spawn path, unchanged) or
any bound, online, enabled host — labeled with trust zone and load
("Unity workstation · work · 1/4").

Choosing a remote host makes submit do, in order: `POST /work-items` (title
from the task name, body from the prompt) → `POST /work-items/{id}/approval`
(the human filling in the dialog *is* the approval; `approver` from the
operator identity setting) → `POST /execution/dispatch` (branch defaulted to
`ao/<work-item-id>`, provider/model/mode from the host's discovery endpoint).
The dialog then shows command progress (`pending → delivering → acknowledged`)
and lands on the session. The approval invariant is never bypassed — the UI
just performs all three persisted steps explicitly.

Hosts that are bound but unreachable appear disabled with their
`lastProbeError` as tooltip — invariant 6: an unreachable host is a fact about
the host, never about its sessions.

### Settings → Computers (global settings, new tab)

A table (shadcn `table`) of registered hosts: status dot (reachable / last
probe error), name, endpoint, transport, trust zone, `activeSessions`/max,
Paseo version, enabled `switch`. Row actions: Edit, Test connection, Remove.

**Add Computer** (shadcn `sheet`, three steps — `ConnectMobileModal` is the
in-repo precedent for "modal with a copy-paste setup step"):

1. **Prepare the worker** — the one terminal step, shown as a copy-paste block
   with the mandatory posture baked in and a generated password:

   ```
   paseo daemon start --home ~/.paseo-ao --listen 0.0.0.0:6780 \
     --no-relay --no-mcp --no-web-ui
   ```

   with `PASEO_PASSWORD` set, plus the checklist: `claude`/`codex` installed,
   repo cloned, Tailscale up. Copy button. The dialog states *why* `--no-mcp`
   is non-negotiable (the 38-tool injected catalog).
2. **Connect** — name, id (auto-slugged), endpoint, transport (`tailscale`
   default), trust zone, max sessions, password field. Submitting stores the
   password via the secrets endpoint (U1) and registers with the returned ref.
   The password never appears in the register payload — the existing API shape
   is preserved.
3. **Verify** — synchronous probe (U2): success shows `serverId` +
   `paseoVersion`; failure shows the probe error verbatim; the G5 self-target
   case gets its own message ("this endpoint is this AO daemon's own Paseo —
   pick the worker's address").

### Project Settings → Computers section

In `ProjectSettingsForm`: list this project's bindings (host, path on host,
base branch, priority, enabled) via U3; **Bind a computer** dialog with host
select (registered hosts only), host repo path, base branch, priority. This is
the step the E2E run proved people will miss — so the Computers pane also
shows a per-host "bound projects" count, and a project with zero bindings
shows an inline hint in the New Task "Run on" select ("no computers bound —
bind one in project settings", linking there).

### Project → Work Items tab

On the project page: a work-item list (status chips for
`draft/proposed/approved/rejected` × `open/in_progress/done/cancelled`,
priority, risk). Create dialog (title, body, acceptance criteria, risk,
priority). Approve / Reject buttons stamping the operator identity.
**Dispatch** button on approved+open items opens the dispatch dialog
(same component the New Task path uses, minus the create/approve steps).
This is the "planner's surface" G1 built the API for; power users get the
full graph, the New Task path keeps the common case at one dialog.

### Inbox

Extend `NotificationCenter` + a dedicated Inbox view fed by
`GET /execution/questions`:

- `source: agent_event` → question text, options as buttons if present,
  free-text answer box → answer endpoint.
- `source: paseo_permission` → Allow / Deny + optional note → decision
  endpoint, `requestId` passed through for the full-id confirmation the API
  supports.

U9 wires both into the existing notification stream so the bell badges and the
OS notification fires; the board's needs-you column picks these sessions up.

### Board and session detail for remote sessions

- Session cards get a host badge (needs U4's session-view fields), tooltip with
  trust zone and reachability.
- Remote session detail: no xterm (there is no local PTY, by design). Instead:
  the durable execution-events timeline (U8) — checkpoints, results, questions,
  state transitions with `transport` provenance — plus branch/PR panels, which
  already work because `Branch` is set and `observe/scm` discovers PRs
  unchanged. Command/escalation state (attempt number, G2 escalations) shows in
  the inspector.
- Kill/cleanup buttons stay, backed by the error-tolerant remote path (D3);
  an unreachable host renders as "host unreachable — sessions preserved",
  never as session failure.

### Host detail: skills, preferences, schedules

Clicking a row in the Computers table opens a host detail view (tabs, shadcn
`tabs`), fed by the maintenance channel (U9) and provider discovery (U5):

- **Overview** — probe facts (serverId, version, reachability history),
  bindings, active sessions, trust zone.
- **Skills** — the skills installed in the host's `~/.claude/skills/`, listed
  with the name and description parsed from each skill's frontmatter, plus a
  staleness timestamp ("as of last inventory"). Cross-host comparison rows
  ("this host lacks paseo-advisor; its paseo-loop is older than host Y's")
  each carry a **"Sync to host"** action (Q3 = B): AO pushes the skill
  directory from the source machine through the U9 channel, per-file
  sha256-verified on arrival, then re-inventories to confirm. Skills that
  orchestrate *through Paseo* (loop, handoff, committee — anything that spawns
  agents or schedules) are badged **policy-gated** per D6, with the tooltip
  explaining that AO owns scheduling and the daemon runs `--no-inject-mcp`.
- **Preferences** — an editor for the host's
  `~/.paseo/orchestration-preferences.json`. Two sections mirroring the file:
  the role→provider map as five selects (`impl`, `ui`, `research`, `planning`,
  `audit`) whose options come from that host's live provider catalog (U5) so a
  stale or misspelled provider string is impossible to save — the same
  diff-against-catalog behaviour the `paseo-prefs` skill performs; and the
  freeform `preferences` strings as an editable list. Save writes through the
  maintenance channel, re-reads the file to confirm, and stores the confirmed
  copy + hash in AO. A **drift badge** shows when the hash on the host no
  longer matches what AO last wrote (someone edited it locally — surface it,
  don't silently overwrite; offer "pull from host" / "push AO's copy").
  A "copy from another host" action fills the form from another host's stored
  copy. Preferences are per-user on the machine, so the editor states they
  affect every daemon on that host, not just the AO-owned one.
- **Instructions** — the host's machine-scope `~/.claude/CLAUDE.md`, rendered
  as markdown with an edit mode. Writes go through the same U9
  write → confirm-read → persist cycle as preferences, with the same drift
  badge and pull/push resolution. The tab states the blast radius honestly:
  this file affects every Claude session on that machine, not just AO's.
- **Schedules** — `paseo schedule ls` for this host (U10). On an AO-owned
  daemon this list **should be empty** (D6): any row renders as a policy
  warning with the schedule's name, cadence, and a delete action through the
  existing schedule surface. The tab states the heartbeat blind spot plainly:
  heartbeats cannot be enumerated, so an empty schedules list is necessary,
  not sufficient.

### Project Settings → Instructions & skills

A section in `ProjectSettingsForm` showing the project-scope documents and
skills, with the canonical/copy distinction made visible instead of papered
over:

- **Canonical** — the repo-root `CLAUDE.md` and `AGENTS.md` read from the
  project's local path at the base branch, rendered as markdown, plus the
  project's `.claude/skills/` listed like the host inventory. Read-only in
  the dashboard (Q2, confirmed): these are versioned code, and the honest
  edit path is a branch — an **"Edit via task"** button pre-fills the New
  Task dialog with the file path so the change arrives as a normal reviewed
  branch, not a silent write to someone's checkout.
- **Per-host copies** — one row per binding: does the checkout at
  `host_repo_path` (base branch) carry the same content hash as canonical?
  A stale host shows a drift badge with the plain-language consequence
  ("worktrees created on this host get the old instructions") and the fix
  ("pull the base branch on the host" — a one-click U9 channel action,
  `git -C <host_repo_path> pull --ff-only`, refused if it would not
  fast-forward).

### Skills at dispatch time

The dispatch dialog shows the chosen host's skill inventory as insertable
prompt affordances: picking "advisor" or "committee" appends the corresponding
invocation to the prompt text (visible, editable — skills are triggered by
prompt content, so this is honest about the mechanism; nothing is injected
behind the operator's back). Policy-gated skills are present but off by
default and require the same per-task override posture as D5's manual
overrides; choosing one records the override in the dispatch audit trail.
This is visibility and convenience only — it does not and cannot re-enable
the MCP catalog, and enforcement remains what D6 says it is: the daemon
posture.

Operator identity (`approver` / `answeredBy` / `decidedBy`, default `"human"`)
becomes one field in global settings, sent by every mutating call.

---

## 3. Work plan

Backend rules carried from GAPS.md: gate is `./scripts/verify-fork-baseline.sh`
→ VERIFY PASS; `npm run sqlc` after schema changes, `npm run api` after HTTP
changes, generated artifacts in the same commit; migrations in `0900–0949`;
never touch `session_manager/manager.go` beyond a stated need; prefer new
files in new packages. Frontend rules: DESIGN.md (agent-orchestrator verbatim,
shadcn primitives); demo every PR with `ao preview`.

Each item is one PR. Backend first (U-series), then UI (F-series); F-series
starts as soon as its listed dependencies land.

### U1 — secrets API
`POST /api/v1/execution/secrets` `{name, value}` → `{ref}`: validates the name
against the resolver's bare-name rule, writes `$AO_DATA_DIR/secrets/<name>`
0600 (dir 0700), refuses overwrite without `replace: true`.
`GET /api/v1/execution/secrets` → names only. The value is never returned,
logged, or stored in a row — same posture as the resolver
(`execution_wiring.go`). Register in specgen; `npm run api`.
**Accept:** a credential created via HTTP resolves during a real dispatch;
value absent from logs/telemetry/DB; spec parity tests pass.

### U2 — probe endpoint + G5
`POST /api/v1/execution/hosts/{hostId}/probe` → fresh `ExecutionHostResponse`;
performs the real `GET /api/status` probe, persists the outcome (same rows the
observer writes). Implements **G5** here *and* inside register: refuse when the
target's `serverId` equals the local daemon's own. Closes the last open gap in
GAPS.md.
**Accept:** probe of the local daemon's own endpoint is refused with a clear
message at both register and probe; a genuine remote host returns
serverId/version; failure updates `lastProbeError` without touching sessions.

### U3 — bindings read
`GET /api/v1/execution/bindings?projectId=&hostId=` (both optional) returning
host id, project id, host repo path, base branch, priority, enabled. sqlc query
over the existing table; no schema change.
**Accept:** bindings created via the existing PUT round-trip through the list;
spec parity passes.

### U4 — remote facts on sessions + command status
Add to `ControllersSessionView`: `executionHostId?`, `executionBackend?`
(`local` when absent), `workspaceTitle?`, `attempt?` — read from
`session_execution_bindings`, additive and optional so local sessions are
byte-identical. New `GET /api/v1/execution/commands/{commandId}` →
`{commandState, attempts, lastError?, sessionId}` for dispatch progress and
G2 escalation visibility.
**Accept:** a remote session lists with its host id; a local session's JSON is
unchanged (golden test); command transitions observable across a real outbox
drain.

### U5 — provider discovery per host + dispatch settings pass-through
`GET /api/v1/execution/hosts/{hostId}/providers` → providers with models,
per-provider mode enums, thinking options, and feature IDs, from
`provider ls --json` / `provider models <p> --json` / `inspect_provider`
through the existing fenced CLI client, cached briefly per host. Q12 = B:
`DispatchExecutionRequest` gains an optional `settings` object
(`thinkingOptionId?`, `features?` — e.g. Codex `fast_mode`), validated
against what discovery reports for that host+provider; an ID discovery did
not return is refused, never forwarded.
**Accept:** against a live worker, response distinguishes Claude's modes from
Codex's (D5); a dispatch carrying a discovered feature ID launches with it
and one carrying an undiscovered ID is refused with the valid set in the
error; an unreachable host returns a typed error, not a 500.

### U6 — reject + work-item read
Extend the approval body with `decision: "approved" | "rejected"` (absent →
`approved`, so the G1 CLI keeps working). Add `GET /api/v1/work-items/{id}`.
**Accept:** reject flips state and stamps identity; dispatch of a rejected
item is refused (existing store check); CLI approve unchanged.

### U7 — execution-events read
`GET /api/v1/sessions/{sessionId}/execution-events?after=` returning the
durable rows G4 ingests (kind, payload, transport, timestamp), paginated.
Read-only projection; no schema change.
**Accept:** a reporter-emitted `checkpoint` on a live run appears through the
endpoint with `transport='terminal'`; rung-2 observer transitions appear for
sessions without a reporter.

### U8 — questions → notifications
When paseoevent/observer opens a question or permission request, write a
notification through the existing notification service so the SSE stream and
`NotificationCenter` carry it. Include session id, work item id, and question
id for deep-linking; answering/deciding resolves it.
**Accept:** a live agent question raises a notification within one observer
tick; answering it marks it resolved; no notification is ever the trigger for
lifecycle action (invariants: notifications are advisory).

### U9 — host maintenance channel: skills inventory + preferences read/write
The mechanism the plan's host-detail view stands on, built entirely from
pieces that exist: extend the worker-side AO binary (`paseoreporter`) with
three subcommands — `inventory` (list `~/.claude/skills/*` with frontmatter
name/description), `prefs read`, and `prefs write` (atomic write of
`~/.paseo/orchestration-preferences.json`, `prefs read` back for
confirmation) — each emitting output as checksummed `paseoevent` frames, so
the existing decode path (crc32, 76-col, base64) is the parser and there is
no terminal scraping. AO side: a **maintenance workspace** per host
(`isolation: local`, cwd = home, created on demand, archived after) hosting a
terminal via the existing `CreateTerminal`/`SendTerminalKeys`/
`CaptureTerminal` client methods — the same pattern
`PrepareReportTransport` already uses for the rung-0 reporter. Persist the
inventory and the confirmed preferences copy + content hash per host
(migration in the `0900` block). Endpoints:
`GET /api/v1/execution/hosts/{hostId}/inventory` (cached, with `asOf`;
`refresh=true` runs the channel live) and
`PUT /api/v1/execution/hosts/{hostId}/preferences` (write → confirm-read →
persist; refused when the host is unreachable — a config write may never be
ambiguous). No LLM anywhere in the byte path, matching G4's rule.
**Accept:** against a live worker, the skills list round-trips with correct
frontmatter; a preferences write is confirmed by re-read and survives an AO
restart; a locally edited file is detected as drift (hash mismatch) rather
than overwritten; an unreachable host yields a typed refusal, not a partial
write; gate green.

### U9a — instruction files through the maintenance channel
Rides on U9's binary and channel; separate PR because the surface grows.
Worker subcommands: `file read <allowlisted-path>`, `file write`
(machine-scope `~/.claude/CLAUDE.md` only), `repo status <path>` (content
hashes of `CLAUDE.md`/`AGENTS.md`/`.claude/skills/*` at the checkout's base
branch), `repo ff <path>` (fast-forward pull, refuse otherwise), and — Q3 = B
— `skill push` (receive a skill directory as multi-file base64 frames into
`~/.claude/skills/<name>`, each file sha256-verified before the staged
directory is atomically renamed into place; a failed verify leaves the
existing skill untouched). Paths are an explicit allowlist, never
caller-supplied patterns — same posture as the secret resolver's bare-name
rule; a pushed skill name obeys the same bare-name check. AO side: canonical
project reads are plain local file reads off the project's path. Endpoints:
`GET /api/v1/projects/{id}/instructions` (canonical docs + skills +
per-binding drift), `GET/PUT /api/v1/execution/hosts/{hostId}/instructions`
(machine-scope CLAUDE.md, write → confirm-read → persist),
`POST /api/v1/execution/bindings/{projectId}/{hostId}/sync` (the ff pull),
`POST /api/v1/execution/hosts/{hostId}/skills/{name}/sync` (body names the
source: another host id, or `local` for the AO machine's own
`~/.claude/skills`; push → re-inventory → confirm).
**Accept:** canonical render matches the file in git; a deliberately stale
host checkout shows drift and one sync click clears it; a non-ff host state
is refused with the git error verbatim; machine-scope write round-trips with
drift detection; a path outside the allowlist is refused at the worker; a
skill push is byte-identical on arrival (hash check) and an interrupted push
leaves the prior version intact; gate green.

### U10 — schedule visibility per host
`GET /api/v1/execution/hosts/{hostId}/schedules` wrapping
`paseo schedule ls --json` through the fenced CLI client, plus
`DELETE .../schedules/{scheduleId}` wrapping schedule delete. The response
carries a `policyViolation` flag on every row — D6 says an AO-owned daemon
should have none — and the endpoint documentation states the heartbeat blind
spot (heartbeats have no `ls`; absence of schedules proves nothing about
heartbeats). Read-only plus delete; AO offers no schedule *create*, because
AO owns scheduling.
**Accept:** a schedule created directly on the worker appears flagged within
one refresh; deleting it through the endpoint removes it on the host;
a host with none returns an empty list, not an error; gate green.

### F1 — Settings → Computers pane *(needs U1–U3)*
Route/tab under `_shell.settings.tsx`; host table, Add Computer 3-step sheet,
edit, enable/disable switch (PUT replace), Test connection (U2). Remove =
disable; row deletion is deliberately not offered until a host has zero
bindings and zero sessions (shown as tooltip).
**Accept:** a fresh user with a running worker daemon adds it end-to-end with
zero terminal use on the AO machine; self-target and wrong-password failures
render actionably; `ao preview` demo.

### F2 — Project Settings bindings *(needs U3)*
Bindings section in `ProjectSettingsForm` + Bind dialog; unbound-project hints
in New Task and the Computers pane.
**Accept:** bind → dispatchable without CLI; the E2E "registered but unbound"
trap is impossible to hit silently.

### F3 — Work Items tab *(needs U6)*
List, create dialog, approve/reject, dispatch entry point on eligible items.
**Accept:** create-approve-list round trip with no SQL and no CLI; state chips
match `approvalState`×`lifecycleFact` exactly.

### F4 — Dispatch dialog + "Run on" in New Task *(needs U4, U5; F2 for hints)*
The shared dispatch dialog (trust zone from the chosen host, provider/model/
mode selects plus thinking-option and feature toggles from U5 discovery —
Q12 = B, only IDs discovery returned are offered — branch default
`ao/<work-item-id>`, prompt) and the `GlobalNewTaskDialog` "Run on" select
doing create→approve→dispatch on one submit (Q1). Progress via U4 command
polling; terminal state links to the session.
**Accept:** the full E2E scenario (END_TO_END.md) reproduced entirely from the
dashboard except the worker's `paseo daemon start`; a dispatch to an unbound
project is impossible to attempt (select is empty with a link, not a 4xx).

### F5 — Inbox *(needs U8)*
Inbox view + `NotificationCenter` items for questions and permission
requests; answer and allow/deny inline; operator identity from settings.
**Accept:** a live agent question is answerable from the bell within seconds;
a permission request allow/deny round-trips with the audit identity recorded.

### F6 — remote sessions on the board and in detail *(needs U4, U7)*
Host badge on cards, remote session detail with the execution-events timeline
in place of xterm, attempt/escalation in the inspector, unreachable-host
banner wording per invariant 6.
**Accept:** a remote session is visually distinguishable, inspectable, and
killable from the UI; a local session's card and detail are pixel-unchanged.

### F7 — host detail view *(needs U5, U9, U9a, U10)*
The tabbed host detail described in §2: Overview, Skills (inventory with
cross-host comparison, "Sync to host" push actions, policy-gated badges),
Preferences (role→provider selects validated against
the host's live catalog, freeform preferences editor, drift badge with
pull/push resolution, copy-from-host), Instructions (machine-scope CLAUDE.md
view/edit with drift), Schedules (violation-flagged list with delete). Save
paths go through U9's confirm-read cycle and render its typed refusals
verbatim.
**Accept:** editing the `impl` role to a provider the host actually has and
saving is confirmed against the file on the worker; a provider string not in
the catalog is unselectable; local edits on the host show as drift with both
resolutions working; syncing a skill a host lacks makes it appear in that
host's inventory after the confirming re-read; a schedule planted on the
worker shows flagged and is deletable from the UI; `ao preview` demo.

### F8 — skills at dispatch *(needs U9, F4)*
The dispatch dialog's skill affordances: inventory-driven insertable prompt
snippets, policy-gated skills off by default with the per-task override
recorded in the audit trail alongside the dispatch.
**Accept:** inserting "advisor" produces a visible, editable prompt addition
and nothing else; selecting a policy-gated skill requires the explicit
override and the override is queryable in the audit log; a host with no
inventory yet degrades to a plain prompt box, never a blocked dispatch.

### F9 — project Instructions & skills section *(needs U9a)*
The `ProjectSettingsForm` section from §2: canonical CLAUDE.md/AGENTS.md +
project skills rendered read-only, "Edit via task" pre-filling New Task,
per-binding drift rows with one-click sync.
**Accept:** a stale host checkout is visible and fixable from the project
settings page; the canonical view updates after a merged instruction change;
no path exists in the UI that writes directly to a checkout.

### Done means
A new user, given one copy-paste command on the worker, completes *everything
else* — add computer, bind project, create+approve work, dispatch, answer
questions, decide permissions, watch progress, kill, inspect a host's skills
and instructions, edit its orchestration preferences, and audit its schedules
— in the dashboard.
Every mutating call carries an operator identity into the audit log. No
invariant is weakened: approval still gates dispatch in the store, `idle`
still never completes anything, unreachable never kills anything.

---

## 4. Decisions — CONFIRMED by the owner, 2026-08-08

All twelve were put to the owner and answered. **Defaults confirmed on Q1, Q2,
Q4–Q11. Overridden: Q3 = B (skill push-sync ships in v1) and Q12 = B (dispatch
carries a settings pass-through now).** The work items above reflect the
confirmed state. The original options are preserved below for the record.

**Q1 — Does New Task's "Run on" auto-approve?** The headline flow does
create → approve → dispatch on one submit; the human filling in the dialog is
the approver. **Default A: yes, one submit** — the approval is still a
distinct persisted fact with their identity on it. Alternative B: remote
dispatch always shows a second explicit "Approve & dispatch" confirmation.
Alternative C: B, but only for the `work` trust zone.

**Q2 — Are project-scope CLAUDE.md / AGENTS.md / `.claude/skills` editable in
the dashboard?** They are versioned code. **Default A: read-only + "Edit via
task"** — changes arrive as a reviewed branch. Alternative B: direct edit that
commits straight to the base branch (fast, but an unreviewed instruction
change reaches every future worktree). Alternative C: direct edit writes
uncommitted to the local checkout only (rejected as a recommendation: silent
drift is the exact failure this plan surfaces elsewhere).

**Q3 — Are host-scope skills installable/syncable from AO, or inventory-only?**
**CONFIRMED: B — push-sync ships in v1.** AO can copy a skill directory from
one host (or the AO machine itself) to another through the U9 channel
(multi-file base64 frames, per-file sha256 verified on the receiving side).
The inventory comparison ("host X lacks paseo-advisor; host Y has an older
copy") becomes actionable: each gap row gets a "Sync to host" button. Folded
into U9a and F7.

**Q4 — May the dashboard edit machine-scope `~/.claude/CLAUDE.md` and
`~/.paseo/orchestration-preferences.json`, given both affect every agent on
that machine, not just AO's?** **Default A: yes, with the blast-radius notice
and drift detection.** Alternative B: read-only for CLAUDE.md, writable only
for preferences. Alternative C: both read-only (breaks the stated goal for
preferences).

**Q5 — Where may credentials be written from?** The secrets endpoint (U1) will
accept a password over whatever surface serves the app API — including the
plaintext LAN listener if Connect Mobile is on. **Default A: loopback +
LAN-listener alike (the LAN bridge is authenticated and documented
home-network/Tailscale-only; register/bind are equally sensitive and nobody
proposed gating those).** Alternative B: secrets POST is loopback-only like
daemon-control routes — safer, but then a laptop using the dashboard over the
LAN bridge cannot add a computer, violating "no terminal after setup" for
that topology.

**Q6 — Operator identity: one global setting or per-action entry?**
**Default A: one "Operator name" field in global settings** (default
`"human"`), stamped into approvals/answers/decisions. Alternative B: prompt on
each approval. A is one identity for the audit log on a single-operator tool;
B only earns its friction if a second person ever shares the dashboard.

**Q7 — Can a host be deleted?** **Default A: disable-only until a host has
zero bindings and zero sessions, then hard delete appears.** Alternative B:
always allow delete with cascade warnings. A preserves audit/session history
by construction.

**Q8 — Maintenance workspace lifetime (U9).** **Default A: one persistent,
named maintenance workspace per host, created on first use, archived when the
host is removed** — cheap, and repeated config operations don't churn
workspace creation, which is the code path all the ambiguity guards protect.
Alternative B: create + archive around every operation (cleaner host state,
slower, and more trips through workspace creation — the exact code path all
the ambiguity guards protect; G2 exists because ambiguous creates are the
dangerous moment, so doing fewer of them is worth more than a tidy workspace
list).

**Q9 — Inventory freshness.** **Default A: collect on register, on manual
"Refresh", and opportunistically at each dispatch to that host**; everything
shows an "as of" timestamp. Alternative B: also poll on the observer tick
(steady background load on every host for data that changes rarely).

**Q10 — Where does the Work Items surface live?** **Default A: a tab on the
project page** (work items are project-scoped; `GET /work-items` requires
`projectId`). Alternative B: a global board across projects — requires a new
cross-project list endpoint, and the board's kanban already serves the
cross-project "what's running" view.

**Q11 — Skill insertion phrasing at dispatch (F8).** Skills trigger on prompt
content; there is no API to "enable" one. **Default A: insert a plain-English
instruction naming the skill** ("Use the paseo-advisor skill to get a second
opinion before finalizing."), visible and editable in the prompt box.
Alternative B: slash-command syntax (`/paseo-advisor`) — terser, but only
meaningful if the harness treats a mid-prompt slash token as an invocation,
which is not guaranteed across harnesses.

**Q12 — Does dispatch surface Paseo `thinkingOptionId` / provider features
(e.g. Codex `fast_mode`)?** **CONFIRMED: B — pass-through now.**
`DispatchExecutionRequest` gains an optional `settings` object
(`thinkingOptionId?`, `features?`), validated against what U5 discovery
reports for the chosen host+provider (only feature IDs `inspect_provider`
returns are accepted — never free-typed). The dispatch dialog renders these
as discovery-driven controls. Folded into U5 and F4.

The remaining ten answers, for the record: Q1 one-submit approves; Q2
read-only + "Edit via task"; Q4 both machine-scope files editable with
blast-radius notice; Q5 secrets writable on loopback and the authenticated
LAN listener alike; Q6 one global operator-name setting; Q7 disable-first,
delete only when empty; Q8 persistent maintenance workspace per host; Q9
refresh on register + manual + dispatch; Q10 work items as a project-page
tab; Q11 plain-English skill insertion.
