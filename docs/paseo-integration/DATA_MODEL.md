# Data model

Durable schema for the integration. Follows AO's rules: facts in SQLite, **display status is never
stored**, migrations are append-only, and generated artifacts are regenerated rather than hand-edited.

---

## 1. Migration rules for this fork

### 1.1 Use the `0900`–`0949` block

Upstream is at `0041` and adds **0.63 migrations/day** (19 in the last 30 days; six on a single day;
one added and reverted two days later). Taking `0042` collides in **~1.6 days**.

A high offset is legal, verified four ways:

- `migrate_unique_version_test.go` checks only for **duplicate** parsed versions, never contiguity.
- `storage/sqlite/db.go` invokes `goose.Up(db, "migrations", goose.WithAllowMissing())`, with a comment
  explicitly permitting out-of-order history.
- `migrate_missing_test.go` already tests this shape: `upTo(t, db, 27)`, then seeds
  `goose_db_version` with version `46`, then asserts `28`–`31` still apply.
- The tree already contains a gap — `0022` does not exist — and CI is green.

Four digits, zero-padded, so goose's integer ordering and sqlc's lexicographic file ordering agree.

### 1.2 Never widen the `sessions.harness` CHECK

Upstream widens it by exact-substring `replace()` against `sqlite_master`. A drifted needle
**succeeds with zero effect** — a silent no-op. Upstream has done this twice in 65 days and already
broke its own "don't chain" comment.

Consequence: remote sessions reuse an existing harness value. That interacts badly with
`observe/activity`, which is harness-keyed — see `ARCHITECTURE.md` §5.5. Capability lookups must not
infer *local* capabilities from the harness string.

### 1.3 Fork-owned tables only, `ADD COLUMN` only

No `PRAGMA writable_schema`. No modification of upstream tables beyond additive columns. Every
migration must apply cleanly both to a fresh database and to one already at `0041` with a future
version seeded.

---

## 2. Execution hosts — `0900`

```sql
-- +goose Up
CREATE TABLE execution_hosts (
    id                       TEXT PRIMARY KEY,
    name                     TEXT NOT NULL UNIQUE,
    backend_type             TEXT NOT NULL CHECK (backend_type IN ('local','paseo')),

    -- How to reach it. `--host` accepts host:port, a bare port, tcp://…?ssl=true&password=…,
    -- unix://, pipe://, or a relay offer URL. It returns null for any string WITHOUT a colon and
    -- silently falls through to the local daemon, so this is validated in Go before exec.
    transport                TEXT NOT NULL CHECK (transport IN ('local','tailscale','lan','paseo_relay')),
    endpoint                 TEXT NOT NULL DEFAULT '',
    endpoint_secret_ref      TEXT NOT NULL DEFAULT '',  -- reference, never a credential

    trust_zone               TEXT NOT NULL CHECK (trust_zone IN ('hobby','work','mixed')),
    enabled                  INTEGER NOT NULL DEFAULT 1,
    max_concurrent_sessions  INTEGER NOT NULL DEFAULT 1,

    -- Identity, from GET /api/status. A changed server_id means the daemon was rebuilt or
    -- replaced and every remote id recorded against this host is meaningless.
    server_id                TEXT NOT NULL DEFAULT '',
    paseo_version            TEXT NOT NULL DEFAULT '',

    -- Required posture, asserted at probe time (see VULNERABILITIES.md §6).
    requires_no_mcp          INTEGER NOT NULL DEFAULT 1,
    requires_no_relay        INTEGER NOT NULL DEFAULT 1,

    last_successful_probe_at TEXT NOT NULL DEFAULT '',
    last_failed_probe_at     TEXT NOT NULL DEFAULT '',
    last_probe_error         TEXT NOT NULL DEFAULT '',
    created_at               TEXT NOT NULL,
    updated_at               TEXT NOT NULL
);

CREATE TABLE execution_host_capabilities (
    host_id    TEXT NOT NULL REFERENCES execution_hosts(id) ON DELETE CASCADE,
    capability TEXT NOT NULL,
    PRIMARY KEY (host_id, capability)
);
CREATE INDEX idx_ehc_capability ON execution_host_capabilities(capability);
```

Normalized, not a JSON blob, because routing queries it. Values are free-form strings:
`linux`, `windows`, `unity-6000`, `cuda`, `docker`, `visual-studio`, `python`, `node`.

**`endpoint_secret_ref` is a reference, never a value.** A relay offer URL is a bearer credential —
it contains the daemon's public key and grants `scopes:["*"]` without consulting `PASEO_PASSWORD`.
It never appears in a task row, a log line, or an error message.

## 3. Project→host bindings — `0901`

```sql
CREATE TABLE project_host_bindings (
    project_id     TEXT NOT NULL,
    host_id        TEXT NOT NULL REFERENCES execution_hosts(id) ON DELETE CASCADE,
    host_repo_path TEXT NOT NULL,          -- differs per machine; C:\Projects\X vs /home/u/x
    base_branch    TEXT NOT NULL DEFAULT 'main',
    priority       INTEGER NOT NULL DEFAULT 100,
    enabled        INTEGER NOT NULL DEFAULT 1,
    setup_profile  TEXT NOT NULL DEFAULT '',
    created_at     TEXT NOT NULL,
    updated_at     TEXT NOT NULL,
    PRIMARY KEY (project_id, host_id)
);
```

The MVP requires the clone to pre-exist (`DECISIONS.md` D17). No global path assumption anywhere.

## 4. Work graph — `0910`

Conceptually aligned with upstream issue **#2764**, which is *open, unassigned, has no DDL, and was
declared "on halt" by its author on 2026-07-26*. There is nothing to adopt, so this is built from
scratch and "schema compatible" can only mean it uses the same three table names and the same
facts-not-status rule.

```sql
CREATE TABLE work_items (
    id                      TEXT PRIMARY KEY,
    project_id              TEXT NOT NULL,
    parent_work_item_id     TEXT REFERENCES work_items(id) ON DELETE SET NULL,
    title                   TEXT NOT NULL,
    body                    TEXT NOT NULL DEFAULT '',
    acceptance_criteria_json TEXT NOT NULL DEFAULT '[]',
    allowed_scope_json      TEXT NOT NULL DEFAULT '[]',
    excluded_scope_json     TEXT NOT NULL DEFAULT '[]',
    risk_level              TEXT NOT NULL DEFAULT 'normal',
    policy_profile_id       TEXT NOT NULL DEFAULT '',

    approval_state          TEXT NOT NULL CHECK (approval_state IN ('draft','proposed','approved','rejected')),
    lifecycle_fact          TEXT NOT NULL CHECK (lifecycle_fact IN ('open','in_progress','done','cancelled')),

    priority                INTEGER NOT NULL DEFAULT 100,
    created_by_type         TEXT NOT NULL,   -- human | planner | agent
    created_by_id           TEXT NOT NULL DEFAULT '',
    approved_by             TEXT NOT NULL DEFAULT '',
    approved_at             TEXT NOT NULL DEFAULT '',
    created_at              TEXT NOT NULL,
    updated_at              TEXT NOT NULL
);

-- Edge direction, stated explicitly because #2764's commenters left it unresolved and its
-- design doc is a private Notion page:
--   relationship='blocks'  =>  work_item_id BLOCKS related_work_item_id
--                              (related cannot start until work_item is done)
--   relationship='parent'  =>  work_item_id IS THE PARENT OF related_work_item_id
--   relationship='related' =>  symmetric, advisory only
CREATE TABLE work_item_deps (
    work_item_id         TEXT NOT NULL REFERENCES work_items(id) ON DELETE CASCADE,
    related_work_item_id TEXT NOT NULL REFERENCES work_items(id) ON DELETE CASCADE,
    relationship         TEXT NOT NULL CHECK (relationship IN ('blocks','parent','related')),
    PRIMARY KEY (work_item_id, related_work_item_id, relationship),
    CHECK (work_item_id <> related_work_item_id)
);

CREATE TABLE work_item_sessions (
    work_item_id    TEXT NOT NULL REFERENCES work_items(id) ON DELETE CASCADE,
    session_id      TEXT NOT NULL,
    role            TEXT NOT NULL CHECK (role IN ('planner','implementer','reviewer','verifier')),
    attempt_number  INTEGER NOT NULL DEFAULT 1,
    is_active_owner INTEGER NOT NULL DEFAULT 0,
    created_at      TEXT NOT NULL,
    released_at     TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (work_item_id, session_id)
);

-- At most one active implementer per work item. This is the claim that makes duplicate
-- dispatch impossible, and it is enforced by the database, not by application logic.
CREATE UNIQUE INDEX idx_wis_one_active_implementer
    ON work_item_sessions(work_item_id)
    WHERE is_active_owner = 1 AND role = 'implementer';
```

**No `board_column`.** Board state is derived in the service layer, per AO's load-bearing rule.
`Ideas`/`Proposed`/`Approved` come from `approval_state`; `Queued` from an existing dispatch command
with no external start; `Running` from an active execution with no unresolved question;
`Needs Input` from an open question or pending permission or `activity_state='blocked'`;
`Review` from a received result plus PR/diff; `Done` from `lifecycle_fact='done'`; `Failed` from a
last attempt that ended without completion and no scheduled retry.

## 5. Session→execution binding — `0902`

The foreign-key boundary of the whole integration.

```sql
CREATE TABLE session_execution_bindings (
    session_id                 TEXT PRIMARY KEY,
    work_item_id               TEXT REFERENCES work_items(id) ON DELETE SET NULL,
    backend_type               TEXT NOT NULL CHECK (backend_type IN ('local','paseo')),
    host_id                    TEXT NOT NULL REFERENCES execution_hosts(id),

    -- Remote-minted, durable. wks_<hex> and the agent uuid are persisted by Paseo with
    -- awaited atomic writes, so both survive a daemon restart and are safe as keys.
    external_workspace_id      TEXT NOT NULL DEFAULT '',
    external_agent_id          TEXT NOT NULL DEFAULT '',
    external_parent_agent_id   TEXT NOT NULL DEFAULT '',
    bound_server_id            TEXT NOT NULL DEFAULT '',

    -- AO-owned idempotency handles. HINTS, NOT KEYS: Paseo enforces no uniqueness on either,
    -- workspace create "always produces a fresh workspace", and the provider session is spawned
    -- BEFORE the label exists. See DECISIONS.md D21 and RECOVERY.md §1.2.
    workspace_title            TEXT NOT NULL DEFAULT '',   -- "ao:<session>:<attempt>"
    intent_id                  TEXT NOT NULL DEFAULT '',   -- label ao.intent
    attempt                    INTEGER NOT NULL DEFAULT 1,
    labels_written_json        TEXT NOT NULL DEFAULT '{}', -- what we SET; labels are not readable

    branch_name                TEXT NOT NULL DEFAULT '',
    host_workspace_path        TEXT NOT NULL DEFAULT '',
    provider                   TEXT NOT NULL DEFAULT '',
    model                      TEXT NOT NULL DEFAULT '',
    mode                       TEXT NOT NULL DEFAULT '',   -- provider-specific; no global enum
    dispatch_generation        INTEGER NOT NULL DEFAULT 1,
    launch_id                  TEXT NOT NULL DEFAULT '',   -- mirrors SessionMetadata.RuntimeLaunchID

    -- Transcript cursor. Nullable and advisory: `--since` is a dead flag and every `logs` call
    -- is a full replay, so correctness never depends on this. Purely an optimization.
    transcript_bytes           INTEGER NOT NULL DEFAULT 0,
    transcript_prefix_sha256   TEXT NOT NULL DEFAULT '',
    terminal_id                TEXT NOT NULL DEFAULT '',
    terminal_lines_consumed    INTEGER NOT NULL DEFAULT 0,

    last_observed_at           TEXT NOT NULL DEFAULT '',
    created_at                 TEXT NOT NULL,
    archived_at                TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_seb_host   ON session_execution_bindings(host_id) WHERE archived_at = '';
CREATE INDEX idx_seb_intent ON session_execution_bindings(intent_id);
```

`terminal_lines_consumed` is the **real** cursor — `terminal capture --start N --end M --json`
returns `{terminalId, lines[], totalLines}`, and `totalLines` is monotonic. That is the only
cursored surface in the Paseo CLI.

## 6. Briefs, outbox, events — `0920` / `0921`

```sql
CREATE TABLE session_briefs (
    id                 TEXT PRIMARY KEY,
    session_id         TEXT NOT NULL,
    version            INTEGER NOT NULL,
    schema_version     TEXT NOT NULL,
    brief_json         TEXT NOT NULL,
    brief_sha256       TEXT NOT NULL,
    report_nonce       TEXT NOT NULL,   -- per-launch; prevents AO ingesting its own example
    created_at         TEXT NOT NULL,
    supersedes_brief_id TEXT REFERENCES session_briefs(id),
    UNIQUE (session_id, version)
);
```

Immutable. A correction creates a new version; nothing is ever overwritten.

```sql
CREATE TABLE execution_commands (
    id              TEXT PRIMARY KEY,
    session_id      TEXT NOT NULL,
    host_id         TEXT NOT NULL REFERENCES execution_hosts(id),
    command_type    TEXT NOT NULL CHECK (command_type IN (
                        'prepare_workspace','start_agent','send_message',
                        'answer_permission','deny_permission','checkpoint',
                        'stop_agent','archive_workspace')),
    payload_json    TEXT NOT NULL,
    idempotency_key TEXT NOT NULL UNIQUE,
    sequence        INTEGER NOT NULL,      -- per-session FIFO
    state           TEXT NOT NULL CHECK (state IN ('pending','delivering','acknowledged','failed')),
    attempt_count   INTEGER NOT NULL DEFAULT 0,
    next_attempt_at TEXT NOT NULL DEFAULT '',
    last_error      TEXT NOT NULL DEFAULT '',
    created_at      TEXT NOT NULL,
    acknowledged_at TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_ec_due ON execution_commands(next_attempt_at)
    WHERE state IN ('pending','delivering');
```

`idempotency_key` derivation, per type:

| Type | Key |
|---|---|
| `prepare_workspace`, `start_agent` | `<session>:<attempt>:<type>` |
| `send_message` | `<session>:<message_uuid>` |
| `answer_permission`, `deny_permission` | `<session>:<full_request_id>` — the **full** ID from `inspect`, never `permit ls`'s 8-char prefix |
| `checkpoint`, `stop_agent` | `<session>:<attempt>:<type>:<reason>` |
| `archive_workspace` | `<session>:<external_workspace_id>` |

**Ordering is per-session FIFO.** A `send_message` must not overtake the `start_agent` it depends on.
State machine: `pending → delivering → acknowledged`, or `→ failed` after budget exhaustion. The row
is **committed before** any Paseo call, so a crash mid-dispatch replays rather than loses.

```sql
CREATE TABLE execution_events (
    id                TEXT PRIMARY KEY,
    session_id        TEXT NOT NULL,
    host_id           TEXT NOT NULL,
    launch_id         TEXT NOT NULL DEFAULT '',

    -- Emitter-minted, because Paseo supplies no event id and no sequence.
    protocol_event_id TEXT NOT NULL DEFAULT '',
    protocol_seq      INTEGER,
    event_type        TEXT NOT NULL,
    transport         TEXT NOT NULL CHECK (transport IN ('terminal','sentinel','inspect','output_schema')),

    payload_json      TEXT NOT NULL,
    payload_sha256    TEXT NOT NULL,
    raw_line          TEXT NOT NULL DEFAULT '',   -- stored BEFORE being applied
    observed_at       TEXT NOT NULL,
    ingested_at       TEXT NOT NULL,
    applied           INTEGER NOT NULL DEFAULT 0
);

-- Agent-authored events dedupe on the emitter-minted id. Full replay is therefore free.
CREATE UNIQUE INDEX idx_ee_protocol
    ON execution_events(session_id, protocol_event_id)
    WHERE protocol_event_id <> '';

-- Observations have no id, so they dedupe on content within a session.
CREATE UNIQUE INDEX idx_ee_observation
    ON execution_events(session_id, event_type, payload_sha256)
    WHERE protocol_event_id = '';
```

Two indexes because the two classes have different identity. The same logical event seen via two
transports lands twice under different `transport` values; the reader prefers
`output_schema > terminal > sentinel` when reconciling a `result`.

## 7. Questions, checkpoints, audit — `0930`

```sql
CREATE TABLE human_questions (
    id                   TEXT PRIMARY KEY,
    session_id           TEXT NOT NULL,
    work_item_id         TEXT REFERENCES work_items(id) ON DELETE SET NULL,
    source               TEXT NOT NULL CHECK (source IN ('agent_event','paseo_permission')),
    external_question_id TEXT NOT NULL DEFAULT '',  -- FULL permission request id
    question             TEXT NOT NULL,
    recommendation       TEXT NOT NULL DEFAULT '',
    options_json         TEXT NOT NULL DEFAULT '[]',
    state                TEXT NOT NULL CHECK (state IN ('open','answered','cancelled')),
    answer               TEXT NOT NULL DEFAULT '',
    answered_by          TEXT NOT NULL DEFAULT '',
    answered_at          TEXT NOT NULL DEFAULT '',
    delivery_command_id  TEXT REFERENCES execution_commands(id),
    created_at           TEXT NOT NULL
);

CREATE TABLE session_checkpoints (
    id                    TEXT PRIMARY KEY,
    session_id            TEXT NOT NULL,
    sequence              INTEGER NOT NULL,
    summary               TEXT NOT NULL,
    completed_steps_json  TEXT NOT NULL DEFAULT '[]',
    remaining_steps_json  TEXT NOT NULL DEFAULT '[]',
    test_evidence_json    TEXT NOT NULL DEFAULT '[]',
    commit_sha            TEXT NOT NULL DEFAULT '',
    branch_pushed         INTEGER NOT NULL DEFAULT 0,
    created_at            TEXT NOT NULL,
    UNIQUE (session_id, sequence)
);

CREATE TABLE audit_events (
    id          TEXT PRIMARY KEY,
    event_type  TEXT NOT NULL,
    actor_type  TEXT NOT NULL,   -- human | scheduler | observer | agent
    actor_id    TEXT NOT NULL DEFAULT '',
    subject_type TEXT NOT NULL,
    subject_id  TEXT NOT NULL,
    detail_json TEXT NOT NULL DEFAULT '{}',
    created_at  TEXT NOT NULL
);
CREATE INDEX idx_audit_subject ON audit_events(subject_type, subject_id);
CREATE INDEX idx_audit_created ON audit_events(created_at);
```

`source` on `human_questions` is load-bearing: a `paseo_permission` maps to `ActivityBlocked` so
`sessionguard` refuses a text answer and forces `permit allow` with the full ID; an `agent_event`
maps to `ActivityWaitingInput` so `send` still works while nudges stay suppressed.

Audit is append-only. Every consequential control-plane action gets a row: proposal, approval,
rejection, host selection, launch, permission decision, human answer, retry scheduling, stop,
archive, completion.

## 8. CDC triggers — `0940`

AO drives its UI from SQLite triggers appending to `change_log`, tailed by a poller with sequence
watermarking and fanned out over SSE. Mirror the existing trigger shape for the tables the UI reads
live:

`work_items`, `work_item_sessions`, `session_execution_bindings`, `human_questions`,
`session_checkpoints`, and `execution_hosts`.

**Deliberately excluded:** `execution_events` and `execution_commands`. Both are high-volume
machine-to-machine traffic; a full-replay poll can re-touch many event rows per tick, and mirroring
that into the SSE stream would flood every connected client. The UI reads derived state instead.

## 9. sqlc and store mechanics

Queries go in new files under `backend/internal/storage/sqlite/queries/` — `execution.sql`,
`work_items.sql` — never appended to upstream query files, which would guarantee rebase conflicts.

After any schema or query change: `npm run sqlc`, and commit `storage/sqlite/gen/` **in the same
commit**. Never hand-edit `gen/`. On a rebase conflict in a generated file, take upstream's version
and re-run the generator rather than resolving it by hand.

Hand-written wrappers go in `storage/sqlite/store/`, following the existing pattern.
