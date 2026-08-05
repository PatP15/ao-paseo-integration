# Decisions

Ownership and policy decisions for the AO × Paseo integration.

**Status of this document.** Every decision below is **provisional**. The recommended default is
in force until the owner overrides it; development does not wait on confirmation. To change one,
reply with just the IDs — e.g. `D4=B, D11=A`.

Decisions carrying **[EVIDENCE]** have been checked against Paseo `v0.2.5` and AO `main` source.
Where the source constrains or contradicts the original recommendation, that is called out
explicitly rather than smoothed over.

Tested versions of record: Paseo **0.2.5** (`@getpaseo/cli`, AGPL-3.0-or-later),
AO **`main` @ 742c77bc** (Apache-2.0).

---

## D1 — Which system is authoritative for task state?

**Default: AO.** Paseo executes tasks but does not own their lifecycle.

**[EVIDENCE] This is now forced, not merely preferred.** Paseo's agent timeline is held in
`InMemoryAgentTimelineStore`; the optional `durableTimelineStore` is **never constructed by any
caller** in v0.2.5, so the transcript does not survive a daemon restart. On reload the timeline is
rebuilt from `streamHistory()` under a fresh `randomUUID()` epoch with different row granularity.
Paseo therefore cannot be authoritative for anything AO needs to remember.

**Alternatives.** (B) Paseo authoritative — not viable, per above. (C) Split ownership — rejected;
two authorities over one lifecycle is the failure mode this design exists to avoid.

---

## D2 — Who creates worktrees for Paseo-backed work?

**Default: Paseo only.** AO must not create a second worktree.

**[EVIDENCE] Consequence for AO's data model:** `SessionMetadata.WorkspacePath` stays `""` for
remote sessions. Faking a local path would make `Kill`/`Cleanup` invoke the local `gitworktree`
adapter against a directory that does not exist. `Branch` **is** set, which is what lets
`observe/scm.discoverSubjects` find the session's PRs so AO's existing CI/review machinery works
unchanged.

---

## D3 — Who starts, stops, and resumes the coding agent?

**Default: Paseo, acting on AO commands.**

**[EVIDENCE] Caveat on "stops".** `session_manager.Kill` returns a hard error if
`runtime.Destroy` fails. For a remote host that happens whenever the host is unreachable, so
**a user currently cannot terminate a Paseo session while its host is down.** The fork must make
this path error-tolerant. Tracked in `ARCHITECTURE.md` as a required upstream edit.

---

## D4 — Who chooses the computer?

**Default: AO**, using project restrictions, capabilities, trust zone, online state, and capacity.

---

## D5 — Who chooses Claude versus Codex, model, and permission mode?

**Default: AO policy, with a manual per-task override.** Paseo validates what is installed.

**[EVIDENCE] Vocabulary correction.** Paseo has **no `--permission-mode`**. The flag is
`--mode <mode>`, described as "Provider-specific mode (e.g. plan, default, bypass)", and the
available values differ per provider (`provider ls --json` reports Claude: *Plan Mode, Always Ask,
Accept File Edits, Auto mode, Bypass*; Codex: *Default Permissions, Auto-review, Full Access*).
AO must therefore validate mode strings **per provider**, not against one global enum. Discovery is
via `provider ls --json` and `provider models <p> --json`.

---

## D6 — May AO-managed agents use Paseo to spawn subagents or schedules?

**Default: No.** AO owns decomposition and scheduling.

**[EVIDENCE] ⚠️ This cannot be enforced per session, and one part cannot be detected at all.**
`createPaseoToolCatalog` registers a flat, unconditional **38-tool** MCP catalog into *every*
agent — including `create_agent`, `create_schedule`, `create_heartbeat`, `kill_agent`, and
`respond_to_permission`. The only gate anywhere is a daemon-wide boolean
(`mcp.injectIntoAgents`, set by `--no-mcp` / `--no-inject-mcp` at `daemon start`). An agent creates
schedules and heartbeats with **zero operator involvement and no permission prompt**.

Therefore:

1. **AO must run a dedicated daemon started with `--no-inject-mcp`, one per trust zone**, and must
   never drive the operator's `desktopManaged` daemon. This is a deployment constraint, not a
   per-run flag.
2. Schedules remain observable (`schedule ls`). **Heartbeats do not** — `paseo heartbeat` has no
   `ls` and no `inspect`, so heartbeats created by an agent are unenumerable. This is an accepted,
   documented blind spot.

---

## D7 — Where are questions and answers stored?

**Default: AO.** AO persists the answer, then forwards it to Paseo.

---

## D8 — May the owner message an AO-managed agent directly inside Paseo?

**Default: emergency use only.** Normal instructions go through AO so they are durable.

**[EVIDENCE] Out-of-band detection is best-effort and may be impossible.** Importing an
out-of-band message requires reading it back off the timeline, and `paseo logs` emits a rendered
human transcript with no JSON, no event IDs, and no cursor (`--since` is a **dead flag** — declared
but referenced zero times). A `[User] ` prefix is applied only to line 1 of a multi-line message.
Treat detection as advisory.

---

## D9 — Should the complete transcript be copied into AO?

**Default: No.** AO stores instructions, decisions, structured checkpoints, questions, and results.

**[EVIDENCE] Amended rationale.** The original reason was storage economy. The real reason is that
**Paseo's transcript is not durable** (see D1), so "Paseo retains the raw transcript" is only true
until the daemon restarts. Anything AO may later need must be captured as a durable AO fact at the
time it happens, not retrieved from Paseo afterwards.

---

## D10 — How are task proposals handled?

**Default: planner-created work enters `proposed` and requires human approval.**

---

## D11 — Can agents automatically execute follow-up work they discover?

**Default: No.** They may create follow-up proposals, approved separately.

---

## D12 — What happens after failure?

**Default:** hobby — two bounded repair attempts, then escalate. Work — no autonomous retry unless
the task policy explicitly allows it.

**[EVIDENCE] "Failure" is harder to detect than assumed.** Paseo's status enum has exactly five
values (`initializing | idle | running | error | closed`) and **no completed/exited state** —
`idle` conflates *finished*, *never started*, and *awaiting prompt*. The real discriminator,
`attentionReason` (`finished | error | permission`), exists on the wire and is **dropped by both
`ls --json` and `inspect --json`**. Retry logic must never treat `idle` as proof of completion or
of failure.

---

## D13 — What happens when a worker computer goes offline?

**Default: preserve ownership and wait.** Hobby work may fail over only after a lease expires and
only if the branch was pushed. Work never automatically fails over.

**[EVIDENCE] ⚠️ There is a data-integrity bug to fix before this default is even reachable.**
AO's reaper probes every non-terminated session every 5s. `lifecycle.Manager` treats a repeated
same-state observation as a **no-op that does not advance `LastActivityAt`**, so a remote agent
that stays `running` freezes its activity clock; 60s later the recent-activity guard lapses, and a
single `(false, nil)` liveness result marks the session terminated and reaps its containers.

`(false, nil)` is trivially produced by this CLI: a malformed `--label`, a colonless `--host`
falling through to the local daemon, or an archived agent all yield **exit 0, empty result** —
indistinguishable from death. Mitigations, both required:

- `ExecutionRuntime.Alive` **must** return a non-nil error when a host is unreachable. Returning
  `(false, nil)` reads as death and violates AO's "never treat failed probes as death" rule.
- The reaper must become backend-aware. There is no adapter-only formulation.

Note also that AO's `no_signal` display status is **unreachable** for remote sessions
(`FirstSignalAt` is set on the first observation), so a session whose host dies renders a confident
`idle` forever until the above is fixed.

---

## D14 — Who merges pull requests?

**Default: a human only.** AO may declare work ready; it never merges.

---

## D15 — Where does full live session interaction happen?

**Default:** AO shows status, questions, summaries, and recent events; "Open in Paseo" for the
complete session.

**[EVIDENCE] Buildable.** The deep link is `paseo:/h/<serverId>/agent/<agentId>`, also servable as
`https://app.paseo.sh/h/<serverId>/agent/<agentId>`. `serverId` comes from
`GET http://<host>/api/status`. AO must persist `server_id` per host — it also detects a daemon
that has been rebuilt or replaced.

---

## D16 — How do worker computers connect?

**Default: direct connection through Tailscale first; Paseo's encrypted relay as fallback.**

**[EVIDENCE] Both paths confirmed.** `--host` accepts more forms than documented:
`host:port`, a **bare port** (→ `127.0.0.1:<port>`), `tcp://host:port?ssl=true&password=…`,
`unix:///path`, `pipe://…`, and a **relay offer URL** (any string containing `#offer=<base64url>`,
which connects through the public relay with E2EE and no password). Credentials resolve from the
URI's `?password=` first, then `PASEO_PASSWORD`.

Two safety notes: **a `--host` string without a colon returns `null` and silently falls through to
the local daemon** — AO must validate host strings in Go before exec. And the relay offer URL is a
**bearer credential** (it carries the daemon's public key); store it as a secret reference, never in
a task row or log.

---

## D17 — How are repositories made available to worker computers?

**Default: each eligible host has a pre-existing clone registered with AO.** Automatic cloning
later.

---

## D18 — Should standalone sessions created manually in Paseo appear in AO?

**Default: not in the MVP.** AO manages only sessions it launched.

**[EVIDENCE] Discovery would be unreliable anyway.** `paseo ls` is **not** an exhaustive index:
any agent whose project placement cannot be resolved is dropped after filtering, under every flag
combination. Removing a project in the desktop app deletes the project record while leaving
workspaces and agents — permanently hiding every agent under it. So "0 results" means UNKNOWN, never
"absent."

---

## New decisions arising from the source audit

These were not in the original brief.

### D19 — Is the Paseo relay enabled on AO-owned worker daemons?

**Default: No — start AO-owned daemons with `--no-relay`.**

The relay defaults to **enabled** (`daemon.relay.enabled: true`), pointing at
`wss://relay.paseo.sh:443`. AO's connectivity model is Tailscale-first (D16), so the relay is
unnecessary for AO-managed hosts and removes a third party from the data path. Keep it available as
the documented D16 fallback, enabled deliberately per host rather than by default. See
`VULNERABILITIES.md` for exactly what the relay can and cannot observe.

### D20 — Is AO's telemetry enabled in the fork?

**Default: disabled by default in the fork.**

AO ships PostHog telemetry. A fork whose purpose includes work-restricted projects should not
inherit an upstream analytics default. See `VULNERABILITIES.md` for the payload inventory and the
exact opt-out mechanism.

### D21 — Which idempotency handles does AO write, and are they trusted?

**Default: write `ao.session`, `ao.attempt`, and `ao.intent` labels plus a workspace `--title`, and
treat all of them as *hints*, never as keys.**

Paseo enforces no uniqueness on either: `workspace create` is explicitly documented as
*"never deduplicates … it always produces a fresh workspace"*, and nothing constrains labels.
Creation must be serialized in AO's own database (seed row + `PrepareLaunch` generation fence), with
label lookup used strictly for *post-hoc* reconciliation. Never gate creation on a lookup — the
provider session is spawned **before** the label exists, making check-then-create a TOCTOU.

Additionally, AO writes an authoritative `.ao/binding.json` into the workspace at creation time
(`session_id`, `attempt`, `launch_id`) as the recovery authority of last resort, because `ls` is not
exhaustive (D18).

### D22 — Does AO trust Paseo's permission gate as a security boundary?

**Default: No. Paseo permissions are UX; AO's own gating is the boundary.**

Three verified weaknesses: `respond_to_permission` accepts an **arbitrary** `agentId` unscoped to
the caller; `list_pending_permissions` is explicitly cross-agent; and `callerAgentId` is read from
the **query string**, unbound to the auth token. With the MCP injected, any co-resident agent can
enumerate and self-approve permissions, or impersonate a peer. `--no-inject-mcp` (D6) is what makes
this tolerable.

### D23 — What environment does AO pass to `paseo` invocations?

**Default: scrub Paseo's ambient variables unconditionally, and always pass `--workspace <id>`
explicitly.**

`PASEO_AGENT_ID` is injected into every agent process and **cannot be overridden by `run --env`**
(it is spread last). If an AO daemon ever inherits one, every `paseo run` silently becomes a *child*
agent sharing the parent's workspace — invisible in `--json` output — and an agent with a null
`workspaceId` is invisible to `ls` under every flag combination. Scrub at minimum
`PASEO_AGENT_ID`, `PASEO_WORKSPACE_ID`, `PASEO_HOST`, `PASEO_PASSWORD`, `PASEO_HOME`,
`PASEO_LISTEN`.
