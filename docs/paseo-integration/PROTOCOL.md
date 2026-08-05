# Protocol

Wire formats and delivery rules between AO (control plane) and a Paseo-backed agent.

Three schemas, all AO-owned: `ao.run-brief.v1` (AO → agent, immutable), `ao.message.v1`
(AO → agent, follow-ups), `ao.agent-event.v1` (agent → AO, structured reports).

Verified against Paseo **v0.2.5**. Claims marked **[SPIKE]** are settled by
[`spike/`](spike/) rather than by source reading.

---

## 0. Why this document does not look like the original design

The original design assumed AO could poll Paseo's timeline for structured events and dedupe on a
Paseo-supplied `(host_id, external_event_id, external_sequence)`. **No part of that triple exists.**

`paseo logs` has no `--json` (it is the only agent subcommand not wrapped in `withOutput`, so
`--json` parses and is silently ignored), no event IDs, no sequence numbers, and **`--since` is a
dead flag** — declared at `logs.ts:20` and referenced zero times. Every call fetches the entire
timeline (`fetchAgentTimeline(…, {direction:"tail", limit:0})` → `selectAll`) and renders it as
human-readable prose.

Two consequences, one bad and one good:

- **Bad:** there is no cursor, so identity and ordering must move into the AO-owned payload.
- **Good:** full replay on every poll means **at-least-once delivery with idempotent apply and zero
  cursor state**. AO cannot miss an event that is still on the timeline. The absence of `--since` is
  what makes this safe rather than what breaks it.

And one hard limit that reshapes everything below: **assistant text is not stored as a message, it
is stored as one row per provider delta.** Codex emits one row per `agent_message_delta`; Claude
emits `state.assistantText.slice(state.emittedAssistantLength)`. Re-joining is conditional on
strict seq adjacency (`previous.seqEnd + 1 === entry.seqStart`) and **any intervening row breaks
it** — a `reasoning` delta, a `tool_call`, a `todo`. Both providers interleave exactly those. When
the merge does not fire, `appendText` joins the fragments with `\n` **and trims whitespace on both
sides of the split**, at an arbitrary byte offset that may land inside a JSON payload.

So a sentinel line printed by a model is **not** a reliable frame. It is a best-effort hint.

---

## 1. Transport ladder

| Rung | Mechanism | Role |
|---|---|---|
| **0** | `paseo terminal capture --start N --end M --json` → `{terminalId, lines[], totalLines}` | **PRIMARY** |
| 1 | `AO_EVENT` sentinel in assistant text | advisory hint only |
| 2 | No event channel — status + permissions + AO's own SCM observer | **the floor; always available** |
| 3 | AGPL sidecar on `@getpaseo/client` (`/ws`, real cursors) | separate repo, counsel only |
| 4 | Inbound POST to AO | **rejected** |

### Rung 0 — terminal capture (primary)

The only surface in the entire Paseo CLI with a real cursor. `--start`/`--end` are line addresses
and `totalLines` is monotonic, so AO reads forward deterministically with no re-parsing and no
heuristics. Critically, **no LLM is in the byte path**: an AO-installed reporter writes NDJSON to
the PTY, so framing does not depend on model compliance.

Constraints, all **[SPIKE]**: it is a PTY, so `lines` are *screen* lines hard-wrapped at `COLS`
(mitigated by emitting 76-char base64 chunks with a `k/n` header); scrollback depth is bounded; and
the terminal is a workspace resource that dies with the workspace.

### Rung 1 — the `AO_EVENT` sentinel (advisory only)

Retained because it costs one regex and works without a worker-side install. Demoted because of the
delta-boundary shredding above. Rules, all mandatory:

- **Payload is a pointer, never data.** `{v, nonce, sid, seq, eventId, state, crc32}`, ≤2 KB,
  base64 body, explicit terminator. Detail is fetched out-of-band by `eventId`. A shredded pointer
  costs one poll; a shredded payload loses information.
- **Never `--filter`.** Filtering happens *before* the curator, and `projectForCuration` renumbers
  seqs densely — so `--filter text` makes previously non-adjacent assistant messages adjacent and
  `mergeAssistantChunks` joins them with **empty string**
  (`` text: `${previousAssistant.text}${entryAssistant.text}` ``), splicing the next message onto the
  end of a sentinel line. `--filter permissions` is dead code besides (no item type contains that
  substring).
- **Never `--tail` for ingest.** `slice(-maxItems)` drops the **oldest** items and counts projected
  entries, not lines, so it can silently discard unseen events.
- **Never `-f` for ingest.** It discards all history on a 2s timeout
  (`LIVE_HISTORY_FETCH_TIMEOUT_MS = 2_000`, then `console.warn` and proceed with `[]`).
- **Per-launch nonce is required**, see §5.

### Rung 2 — the floor

Worth stating plainly so the team does not over-invest in transport: **~13 of AO's 14 derived
display statuses need no event channel at all.**

| Status family | Source |
|---|---|
| `working` / `idle` / `exited` / `terminated` | `inspect --json .Status` + AO's `LastActivityAt` + the reaper reduction |
| `needs_input` | `inspect --json .PendingPermissions != []` → `ActivityBlocked` |
| `pr_open` `draft` `ci_failed` `review_pending` `changes_requested` `approved` `mergeable` `merge_conflict` `merged` | **AO's existing `observe/scm` + `lifecycle/reactions.go`, unchanged**, as soon as `sessions.branch` is populated |

What rung 2 loses is tool-level granularity and structured worker→control-plane messages. It does
**not** lose the PR lifecycle. Rung 2 requires the reaper fix (see `DECISIONS.md` D13).

### `--output-schema` — repurposed, not dropped

It hard-errors with `--background` (`"--output-schema cannot be used with --background"`), so it
cannot carry a long-running run. It is ideal for a **bounded foreground second call** in an
already-provisioned workspace to harvest the terminal `result`/`failure` as a schema-validated
object once status goes `idle`. Because that call only *reads* committed workspace state, it is
idempotent and freely retryable after an AO restart — which is exactly why its
non-survivability does not matter here.

---

## 2. `ao.run-brief.v1` — the immutable instruction package

Constructed and persisted **before** any Paseo call. Never mutated; a correction creates a new
version with `supersedes_brief_id` set. Stored with its `sha256`.

```json
{
  "schema": "ao.run-brief.v1",
  "briefId": "brief_uuid",
  "briefSha256": "…",
  "sessionId": "ao_session_uuid",
  "workItemId": "work_item_uuid",
  "attempt": 1,
  "launchId": "launch_uuid",
  "reportNonce": "nonce_uuid",

  "project":  { "id": "…", "name": "Unity RPG", "baseBranch": "main",
                "branch": "ao/task-42-inventory" },
  "role": "implementer",
  "goal": "Implement persistent inventory storage.",
  "acceptanceCriteria": ["Inventory survives application restart.", "…"],
  "scope": { "allowed": ["Assets/Scripts/Inventory/**"], "excluded": ["Authentication"] },
  "context": [{ "type": "repository_file", "path": "AGENTS.md", "required": true }],

  "execution": { "host": "unity-windows", "provider": "codex",
                 "mode": "provider-specific-mode-id", "maxRuntimeMinutes": 90 },
  "policy":   { "maySpawnPaseoAgents": false, "mayCreatePaseoSchedules": false,
                "mayInstallDependencies": false, "mayPushAssignedBranch": true,
                "mayCreateDraftPullRequest": true, "mayMerge": false,
                "mustAskBeforeExternalServiceChanges": true },
  "verification": { "requiredCommands": ["…supplied by repository instructions…"],
                    "requiredEvidence": ["test command and exit status", "commit SHA"] },
  "reporting": { "transport": "terminal|sentinel", "eventSchema": "ao.agent-event.v1",
                 "checkpointAfterMinutes": 30, "questionsMustBlock": true,
                 "followUpTasksMustBeProposedOnly": true }
}
```

Note `execution.mode` is a **provider-specific** string, not a global enum — Paseo has no
`--permission-mode` and each provider exposes its own mode list (`DECISIONS.md` D5).

**No secrets, ever.** Not credentials, not tokens, not the daemon password, not a relay offer URL.
Anything the agent needs to authenticate with must arrive by another path.

---

## 3. `ao.message.v1` — outbound follow-ups

```json
{
  "schema": "ao.message.v1",
  "messageId": "message_uuid",
  "sessionId": "ao_session_uuid",
  "sequence": 7,
  "kind": "human_answer | feedback | checkpoint_request | correction",
  "createdAt": "2026-08-04T12:00:00+09:00",
  "relatedEventId": "question_uuid",
  "body": "Use JSON files for the first release."
}
```

**Persistence-before-delivery is absolute.** AO must:

1. Insert the envelope into `execution_commands`.
2. **Commit the transaction.**
3. Only then invoke `paseo send`.
4. Mark `acknowledged` only on a successful Paseo response.
5. Retry undelivered commands after restart.

The visible text is prefixed with `[AO_MESSAGE:<messageId>]`.

### Idempotency, honestly

Paseo has **no idempotency key on `send`**. `clientMessageId` exists on the wire but no CLI flag
exposes it, and server-side it is threaded to a logger — `client-message-id.ts` is 16 lines with no
store and no "already delivered" branch.

So the marker is a **best-effort duplicate check, not a guarantee**. Before resending, AO greps the
transcript for `[AO_MESSAGE:<uuid>]`. This works better than sentinel ingest does, because
`user_message` items are rendered `[User] <text>` and AO controls the exact marker — but it inherits
the same weaknesses: the transcript is non-durable across daemon restarts, and only line 1 of a
multi-line message carries the `[User] ` prefix.

**Design rule:** make every message safe to deliver twice. Answers and feedback are idempotent by
construction (restating an answer is harmless). Never send a message whose second delivery would
cause a second irreversible action.

---

## 4. `ao.agent-event.v1` — agent → AO

Types: `checkpoint`, `question`, `blocked`, `result`, `failure`, `follow_up_proposal`.

```json
{
  "schema": "ao.agent-event.v1",
  "eventId": "event_uuid",
  "sessionId": "ao_session_uuid",
  "launchId": "launch_uuid",
  "seq": 3,
  "type": "question",
  "payload": {
    "question": "Should corrupt saves be preserved before resetting?",
    "recommendation": "Preserve the corrupt file and create a clean save.",
    "options": ["Delete and reset", "Preserve and reset", "Block startup"],
    "blocking": true
  }
}
```

**Identity and ordering are emitter-minted**, because Paseo supplies neither:

- `eventId` — agent-minted UUID. **The dedupe key.** Full replay + unique index on
  `(session_id, event_id)` makes re-ingestion free.
- `seq` — monotonic per `launchId`. Used for **gap detection only**; a hole means "ask again", never
  "reconstruct". Repair is a `paseo send` requesting re-emission.
- `launchId` — from the brief. Scopes `seq` and lets AO discard events from a superseded launch,
  mirroring the `PrepareLaunch` generation fence in `lifecycle`.

### Parser rules

1. Require the exact prefix and terminator.
2. Validate against the schema; reject unknown `type`.
3. Reject payloads over the size cap.
4. Verify `crc32` (rung 1) before parsing.
5. **Store the raw line before applying it.**
6. Deduplicate on `(session_id, event_id)`.
7. Discard events whose `launchId` is not current.
8. A malformed line is **counted and logged, then dropped.** It must never crash the observer, and
   it must never be partially applied.
9. **Never execute anything embedded in an event field.** No path, command, URL, or host from an
   event is ever passed to a shell, used to select a host, or used to resolve a credential.

---

## 5. The two rules that make this channel safe

These matter more than the schemas.

### 5.1 Events are advisory — they may never authorize an irreversible action

A scraped event **may** set activity state, create a work item, record a checkpoint, raise a
question, and notify a human. It **may never** trigger: `kill`, `archive`, `cleanup`, force-push,
merge, `permit allow`, host reassignment, policy change, approval-state change, or retry-budget
change.

Rationale: `get_agent_activity` is **cross-agent and unscoped**, so any co-resident agent can read
another agent's transcript and replay its sentinel. The channel is forgeable by design. Anything
irreversible must be gated on a durable AO fact or a human decision.

### 5.2 The channel is self-poisoning without a nonce

The curator prefixes only **line 1** of a multi-line blob (`[User] ${item.text.trim()}`,
`[Thought] …`). The run brief that *teaches* the event format necessarily contains an example
`AO_EVENT` line — which then appears in the transcript **unprefixed and byte-identical to a real
event**. Without mitigation, **AO ingests its own instructions as events on the first poll.** The
same applies to reasoning: a model musing *"I should emit AO_EVENT {…}"* is ingested too.

Mitigation, mandatory: the sentinel token embeds a **per-launch nonce**
(`AO_EVENT_<nonce> {…}`). The brief's example uses a literal `<NONCE>` placeholder that can never
match. AO accepts only the nonce for the current launch.

---

## 6. What AO reads, and from where

| Fact | Source | Notes |
|---|---|---|
| liveness / crash | `inspect --json .Status` | 5 values; `idle` ≠ complete |
| pending permission | `inspect --json .PendingPermissions` | **full** request IDs — `permit ls` truncates to 8 chars |
| worktree, parentage | `inspect --json .Worktree`, `.ParentAgentId` | `.Worktree` reads `labels["paseo.worktree"]`, a **dead key in v0.2.5** → usable as an AO read-back channel, version-pinned |
| host liveness / identity | `GET /api/status` (`serverId`, version), `/api/health` | `/api/health` is the only auth-exempt route |
| agent-authored events | rung 0, else rung 1 | advisory |
| PR / CI / review | **AO's existing `observe/scm`** | not Paseo's concern |

Two mappings are load-bearing against `sessionguard`:

- A Paseo **permission** → `ActivityBlocked`, so AO physically cannot try to answer it with text and
  must use `permit allow` with the full request ID.
- An agent-authored **question** → `ActivityWaitingInput`, so AO can still answer with `send` while
  automated nudges stay suppressed.

And `permit allow <agent>` with **no** request ID approves **everything** (`--all` is a no-op alias
checked *before* the positional). AO always passes the full ID.
