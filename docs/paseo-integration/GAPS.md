# Post-integration gaps

The work-item list for closing the gaps the first end-to-end run exposed (see
[`END_TO_END.md`](END_TO_END.md)). Each is one PR. Do them in order: G1 unblocks
human use, G2 makes real-world failures recoverable, the rest harden.

The gate is `./scripts/verify-fork-baseline.sh` and must print VERIFY PASS.
After any `.sql`/schema change run `npm run sqlc`; after any HTTP change run
`npm run api`; commit generated artifacts in the SAME commit. Migrations live in
the `0900`–`0949` block. Never edit `session_manager/manager.go` beyond what a
gap explicitly requires. Prefer new files in new packages.

---

## G1 — a surface to create and approve work items

**Why:** `store.UpsertWorkItem` exists but nothing above it calls it. Dispatch
requires an *approved* work item (`execution_dispatch_store.go` enforces it), so
with no create/approve surface, dispatch is unusable without raw SQL.

**Build:**
- `service/workitem` (new package): `Create(ctx, in) (domain.WorkItem, error)`
  defaulting `approval_state='draft'`, `lifecycle_fact='open'`, generating the id;
  `Approve(ctx, id, approver)` flipping `draft`/`proposed` → `approved` and
  stamping `approved_by`/`approved_at`; `List(ctx, projectID)`; `Get(ctx, id)`.
- Store: `UpsertWorkItem` exists; add `SetWorkItemApproval` and
  `ListWorkItemsByProject` with sqlc queries.
- HTTP: `POST /api/v1/work-items`, `POST /api/v1/work-items/{id}/approval`,
  `GET /api/v1/work-items?projectId=`. Register in the spec generator
  (`specgen/build.go`) with path/query params declared, then `npm run api`.
- CLI: `ao work-item add|approve|ls`, registered in `telemetrymeta/cli.go`.

**Acceptance:** a work item can be created and approved through the CLI with no
SQL; `TestRouteSpecParity` and `TestTelemetryMetaClassifiesRegisteredCommandPaths`
pass; a created item defaults to `draft`; dispatch against an *unapproved* item
is still refused; generated artifacts committed together; gate green.

## G2 — automatic escalation on an ambiguous create

**Why:** when `Provision` cannot tell whether a worktree was created it refuses
to make a second one — correct — but the worker retries the *same* attempt and
stalls forever. `RECOVERY.md` §2.2 says escalate to `attempt+1` with a fresh
title, whose provable absence of a prior workspace makes creating unambiguous.

**Build:**
- Adapter: a typed sentinel (e.g. `ErrProvisionOutcomeUnknown`) returned from the
  ambiguous branch in `backend.go`, so the worker can tell "escalate" from
  "retry".
- Store: `EscalateExecutionAttempt(ctx, sessionID)` — one transaction that bumps
  `session_execution_bindings.attempt`, mints a fresh `workspace_title`
  (`ao:<session>:<n+1>`) and `intent_id`, and rewrites the pending command's
  payload to the new attempt.
- Worker (`service/dispatch/worker.go`): on the sentinel, escalate instead of
  plain retry; cap escalations (2–3) so a broken host does not escalate forever.

**Acceptance:** a test that injects an ambiguous-outcome backend drives exactly
one escalation then a clean create on the fresh title; escalation cap enforced;
a genuinely transient error still uses ordinary backoff, not escalation; gate
green.

## G3 — RETRACTED

Not a gap. `CreateExecutionDispatch` already verifies approval, lifecycle, and
project ownership. Left here so the numbering matches `END_TO_END.md`.

## G4 — wire report ingestion, then the host reporter

**Why:** the observer supports reports (`NewWithReports`) and calls
`reports.IngestSession` every tick, but the daemon wires it with plain
`paseoobserve.New`, so `reports == nil` and only status (rung 2) is observed.
The whole `paseoevent` decode path is dark.

**Build (two parts, land the first alone if the second grows):**
- **Read wiring:** construct a `ReportIngestor` backed by
  `paseoevent` + `Client.CaptureTerminal` and pass it to
  `paseoobserve.NewWithReports` in `execution_wiring.go`. Store and read a
  per-session `terminal_id`.
- **The reporter (rung 0 emitter):** a small AO-owned binary installed on the
  worker that writes NDJSON frames (the `paseoevent` frame format, 76-col lines,
  base64 chunks, crc32) to a PTY the read side captures. A launch step starts it
  in the workspace. No LLM in the byte path — this is the whole point of rung 0.

**Acceptance:** with the reporter running, an agent-authored `checkpoint`/
`result` event appears as a durable `execution_events` row with
`transport='terminal'`; ingest failures never terminate a session; without the
reporter the system still derives status from `inspect` (rung 2 floor holds);
gate green.

## G5 — catch self-targeting at registration

**Why:** `guardHost` only refuses a desktop-managed daemon when Paseo *reports*
it, and `GET /api/status` (the only remote surface) omits the field, so the
guard never fires remotely. `ServerID` comparison covers identity drift, but a
host pointed at the operator's own daemon on day one is not caught.

**Build:** at `ao remote register`, probe the target and refuse if its
`serverId` equals the local daemon's (`GET localhost:6767/api/status`) — the one
moment AO can see both. Additive; do not touch the runtime guard.

**Acceptance:** registering a host whose endpoint resolves to the operator's own
daemon is refused with a clear message; registering a genuinely remote host is
unaffected; gate green.
