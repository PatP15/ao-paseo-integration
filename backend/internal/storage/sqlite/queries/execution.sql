-- name: UpsertExecutionHost :exec
INSERT INTO execution_hosts (
    id, name, backend_type, transport, endpoint, endpoint_secret_ref,
    trust_zone, enabled, max_concurrent_sessions, server_id, paseo_version,
    requires_no_mcp, requires_no_relay, last_successful_probe_at,
    last_failed_probe_at, last_probe_error, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (id) DO UPDATE SET
    name = excluded.name,
    backend_type = excluded.backend_type,
    transport = excluded.transport,
    endpoint = excluded.endpoint,
    endpoint_secret_ref = excluded.endpoint_secret_ref,
    trust_zone = excluded.trust_zone,
    enabled = excluded.enabled,
    max_concurrent_sessions = excluded.max_concurrent_sessions,
    server_id = excluded.server_id,
    paseo_version = excluded.paseo_version,
    requires_no_mcp = excluded.requires_no_mcp,
    requires_no_relay = excluded.requires_no_relay,
    last_successful_probe_at = excluded.last_successful_probe_at,
    last_failed_probe_at = excluded.last_failed_probe_at,
    last_probe_error = excluded.last_probe_error,
    updated_at = excluded.updated_at;

-- name: GetExecutionHost :one
SELECT * FROM execution_hosts WHERE id = ?;

-- name: SetExecutionHostMaintenanceHome :exec
UPDATE execution_hosts SET maintenance_home = ? WHERE id = ?;

-- name: ListExecutionHosts :many
SELECT * FROM execution_hosts ORDER BY name, id;

-- name: CountActiveSessionExecutionBindingsByHost :one
SELECT COUNT(*) FROM session_execution_bindings
WHERE host_id = ? AND archived_at = '';

-- name: DeleteExecutionHostCapabilities :exec
DELETE FROM execution_host_capabilities WHERE host_id = ?;

-- name: InsertExecutionHostCapability :exec
INSERT INTO execution_host_capabilities (host_id, capability) VALUES (?, ?);

-- name: ListExecutionHostCapabilities :many
SELECT capability FROM execution_host_capabilities WHERE host_id = ? ORDER BY capability;

-- name: UpsertProjectHostBinding :exec
INSERT INTO project_host_bindings (
    project_id, host_id, host_repo_path, base_branch, priority, enabled,
    setup_profile, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (project_id, host_id) DO UPDATE SET
    host_repo_path = excluded.host_repo_path,
    base_branch = excluded.base_branch,
    priority = excluded.priority,
    enabled = excluded.enabled,
    setup_profile = excluded.setup_profile,
    updated_at = excluded.updated_at;

-- name: ListProjectHostBindings :many
SELECT * FROM project_host_bindings WHERE project_id = ? ORDER BY priority, host_id;

-- name: ListAllProjectHostBindings :many
SELECT * FROM project_host_bindings ORDER BY project_id, priority, host_id;

-- name: GetProjectHostBinding :one
SELECT * FROM project_host_bindings WHERE project_id = ? AND host_id = ?;

-- name: UpsertSessionExecutionBinding :exec
INSERT INTO session_execution_bindings (
    session_id, work_item_id, backend_type, host_id, external_workspace_id,
    external_agent_id, external_parent_agent_id, bound_server_id,
    workspace_title, intent_id, attempt, labels_written_json, branch_name,
    host_workspace_path, provider, model, mode, dispatch_generation, launch_id,
    transcript_bytes, transcript_prefix_sha256, terminal_id,
    terminal_lines_consumed, last_observed_at, created_at, archived_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (session_id) DO UPDATE SET
    work_item_id = excluded.work_item_id,
    backend_type = excluded.backend_type,
    host_id = excluded.host_id,
    external_workspace_id = excluded.external_workspace_id,
    external_agent_id = excluded.external_agent_id,
    external_parent_agent_id = excluded.external_parent_agent_id,
    bound_server_id = excluded.bound_server_id,
    workspace_title = excluded.workspace_title,
    intent_id = excluded.intent_id,
    attempt = excluded.attempt,
    labels_written_json = excluded.labels_written_json,
    branch_name = excluded.branch_name,
    host_workspace_path = excluded.host_workspace_path,
    provider = excluded.provider,
    model = excluded.model,
    mode = excluded.mode,
    dispatch_generation = excluded.dispatch_generation,
    launch_id = excluded.launch_id,
    transcript_bytes = excluded.transcript_bytes,
    transcript_prefix_sha256 = excluded.transcript_prefix_sha256,
    terminal_id = excluded.terminal_id,
    terminal_lines_consumed = excluded.terminal_lines_consumed,
    last_observed_at = excluded.last_observed_at,
    archived_at = excluded.archived_at;

-- name: GetSessionExecutionBinding :one
SELECT * FROM session_execution_bindings WHERE session_id = ?;

-- name: TouchSessionExecutionBinding :execrows
UPDATE session_execution_bindings SET last_observed_at = ?
WHERE session_id = ? AND archived_at = '';

-- The report cursor only ever moves forward on the terminal it was measured
-- against: a stale pass must not rewind a cursor a later one already advanced,
-- and a cursor read from a replaced terminal addresses nothing. Zero is the one
-- allowed rewind, which is how a caller that saw the cursor move backwards
-- restarts from the beginning.
-- name: AdvanceExecutionEventCursor :execrows
UPDATE session_execution_bindings
SET terminal_lines_consumed = sqlc.arg(consumed)
WHERE session_id = sqlc.arg(session_id)
  AND archived_at = ''
  AND terminal_id = sqlc.arg(terminal_id)
  AND (terminal_lines_consumed < sqlc.arg(consumed) OR sqlc.arg(consumed) = 0);

-- name: FindSessionExecutionBindingsByIntent :many
SELECT * FROM session_execution_bindings WHERE intent_id = ? ORDER BY session_id;

-- An ambiguous create is the only path that advances an attempt in place. The
-- empty-id predicates make it impossible to discard a workspace or agent AO
-- already bound, while the attempt predicate rejects a stale worker.
-- name: EscalateSessionExecutionBinding :execrows
UPDATE session_execution_bindings
SET workspace_title = sqlc.arg(workspace_title),
    intent_id = sqlc.arg(intent_id),
    launch_id = sqlc.arg(launch_id),
    attempt = sqlc.arg(new_attempt),
    labels_written_json = '{}'
WHERE session_id = sqlc.arg(session_id)
  AND attempt = sqlc.arg(prior_attempt)
  AND external_workspace_id = ''
  AND external_agent_id = ''
  AND archived_at = '';

-- name: ListActiveSessionExecutionBindingsByHost :many
SELECT * FROM session_execution_bindings
WHERE host_id = ? AND archived_at = '' ORDER BY session_id;

-- name: ListActiveSessionExecutionBindings :many
SELECT * FROM session_execution_bindings
WHERE archived_at = '' ORDER BY session_id;

-- name: ArchiveSessionExecutionBinding :execrows
UPDATE session_execution_bindings SET archived_at = ?
WHERE session_id = ? AND archived_at = '';

-- name: InsertSessionBrief :exec
INSERT INTO session_briefs (
    id, session_id, version, schema_version, brief_json, brief_sha256,
    report_nonce, created_at, supersedes_brief_id
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?);

-- The highest version is the current contract; earlier versions stay readable
-- because a brief is never rewritten.
-- name: GetLatestSessionBrief :one
SELECT * FROM session_briefs
WHERE session_id = ? ORDER BY version DESC LIMIT 1;

-- name: InsertExecutionCommand :exec
INSERT INTO execution_commands (
    id, session_id, host_id, command_type, payload_json, idempotency_key,
    sequence, state, attempt_count, next_attempt_at, last_error, created_at,
    acknowledged_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetExecutionCommandByIdempotencyKey :one
SELECT * FROM execution_commands WHERE idempotency_key = ?;

-- name: GetExecutionCommand :one
SELECT * FROM execution_commands WHERE id = ?;

-- name: ListExecutionCommandsBySession :many
SELECT * FROM execution_commands WHERE session_id = ? ORDER BY sequence;

-- name: NextExecutionCommandSequence :one
SELECT COALESCE(MAX(sequence), 0) + 1 FROM execution_commands WHERE session_id = ?;

-- name: ListDueExecutionCommands :many
SELECT cmd.* FROM execution_commands AS cmd
WHERE cmd.state IN ('pending','delivering')
  AND (cmd.next_attempt_at = '' OR cmd.next_attempt_at <= ?)
  AND NOT EXISTS (
      SELECT 1 FROM execution_commands AS earlier
      WHERE earlier.session_id = cmd.session_id
        AND earlier.sequence < cmd.sequence
        AND earlier.state <> 'acknowledged'
  )
ORDER BY cmd.created_at, cmd.session_id, cmd.sequence LIMIT ?;

-- name: ClaimExecutionCommand :execrows
UPDATE execution_commands
SET state = 'delivering', attempt_count = attempt_count + 1,
    next_attempt_at = ?, last_error = ''
WHERE execution_commands.id = ?
  AND execution_commands.state IN ('pending','delivering')
  AND (execution_commands.next_attempt_at = ''
       OR execution_commands.next_attempt_at <= ?)
  AND NOT EXISTS (
      SELECT 1 FROM execution_commands AS earlier
      WHERE earlier.session_id = execution_commands.session_id
        AND earlier.sequence < execution_commands.sequence
        AND earlier.state <> 'acknowledged'
  );

-- name: AcknowledgeExecutionCommand :execrows
UPDATE execution_commands
SET state = 'acknowledged', next_attempt_at = '', last_error = '', acknowledged_at = ?
WHERE id = ? AND state = 'delivering';

-- name: UpdateExecutionCommandDelivery :execrows
UPDATE execution_commands
SET state = ?, attempt_count = ?, next_attempt_at = ?, last_error = ?, acknowledged_at = ?
WHERE id = ?;

-- name: RewriteExecutionStartAttempt :execrows
UPDATE execution_commands
SET payload_json = sqlc.arg(payload_json),
    idempotency_key = sqlc.arg(idempotency_key),
    state = 'pending',
    next_attempt_at = '',
    last_error = sqlc.arg(last_error),
    acknowledged_at = ''
WHERE id = sqlc.arg(id)
  AND session_id = sqlc.arg(session_id)
  AND command_type = 'start_agent'
  AND state IN ('pending','delivering');

-- name: InsertExecutionEvent :execrows
INSERT INTO execution_events (
    id, session_id, host_id, launch_id, protocol_event_id, protocol_seq,
    event_type, transport, payload_json, payload_sha256, raw_line, observed_at,
    ingested_at, applied
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT DO NOTHING;

-- name: ListExecutionEventsBySession :many
SELECT * FROM execution_events WHERE session_id = ? ORDER BY ingested_at, id;

-- Keyset pagination over (ingested_at, id): passing empty strings for both
-- cursor halves starts from the beginning, because every stored ingested_at is
-- a non-empty RFC3339 string.
-- name: ListExecutionEventsBySessionAfter :many
SELECT * FROM execution_events
WHERE session_id = sqlc.arg(session_id)
  AND (ingested_at > sqlc.arg(after_ingested_at)
       OR (ingested_at = sqlc.arg(after_ingested_at) AND id > sqlc.arg(after_id)))
ORDER BY ingested_at, id
LIMIT sqlc.arg(row_limit);

-- name: GetExecutionEventCursor :one
SELECT ingested_at, id FROM execution_events WHERE session_id = ? AND id = ?;

-- name: DeleteExecutionHostSkills :exec
DELETE FROM execution_host_skills WHERE host_id = ?;

-- name: InsertExecutionHostSkill :exec
INSERT INTO execution_host_skills (host_id, name, description, captured_at)
VALUES (?, ?, ?, ?);

-- name: ListExecutionHostSkills :many
SELECT * FROM execution_host_skills WHERE host_id = ? ORDER BY name;

-- name: UpsertExecutionHostPrefs :exec
INSERT INTO execution_host_prefs (host_id, content, sha256, file_exists, confirmed_at)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(host_id) DO UPDATE SET
    content = excluded.content, sha256 = excluded.sha256,
    file_exists = excluded.file_exists, confirmed_at = excluded.confirmed_at;

-- name: GetExecutionHostPrefs :one
SELECT * FROM execution_host_prefs WHERE host_id = ?;

-- Agent-authored reports are recorded before they are applied, so the applied
-- flag is what a replay after a crash reads to know it still owes the apply.
-- name: GetExecutionReportApplied :one
SELECT applied FROM execution_events
WHERE session_id = ? AND protocol_event_id = ?;

-- name: MarkExecutionReportApplied :execrows
UPDATE execution_events SET applied = 1
WHERE session_id = ? AND protocol_event_id = ? AND applied = 0;

-- Callers derive the id from the fact's identity (session plus external request
-- id), so re-observing an unanswered request is a no-op rather than a duplicate
-- inbox entry. :execrows lets the caller tell "opened" from "already open".
-- name: InsertHumanQuestion :execrows
INSERT INTO human_questions (
    id, session_id, work_item_id, source, external_question_id, question,
    recommendation, options_json, state, answer, answered_by, answered_at,
    delivery_command_id, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT DO NOTHING;

-- name: AnswerHumanQuestion :execrows
UPDATE human_questions
SET state = 'answered', answer = ?, answered_by = ?, answered_at = ?, delivery_command_id = ?
WHERE id = ? AND state = 'open';

-- name: ListOpenHumanQuestions :many
SELECT * FROM human_questions WHERE state = 'open' ORDER BY created_at, id;

-- Fetched by id rather than filtered out of the open list, so a stale id is a
-- clean "not found" while an already-answered one is a clean "not open". Those
-- are different answers to a human clicking twice, and a scan of the open list
-- cannot tell them apart.
--
-- Keep every comment in this file ASCII: sqlc's query rewriter offsets by byte
-- but counts by rune, so one multi-byte character above a query corrupts the
-- SQL it generates for the queries below it.
-- name: GetHumanQuestion :one
SELECT * FROM human_questions WHERE id = ?;

-- A replayed report lands on the row its sequence already owns, so ingesting the
-- same checkpoint twice is a no-op rather than a duplicate progress entry.
-- name: InsertSessionCheckpoint :execrows
INSERT INTO session_checkpoints (
    id, session_id, sequence, summary, completed_steps_json,
    remaining_steps_json, test_evidence_json, commit_sha, branch_pushed,
    created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT DO NOTHING;

-- name: ListSessionCheckpoints :many
SELECT * FROM session_checkpoints WHERE session_id = ? ORDER BY sequence;

-- Recurring observations (an orphan is re-seen on every sweep) derive a
-- deterministic id so the audit log records the finding once instead of once
-- per poll. One-shot callers pass a unique id and are unaffected.
-- name: InsertAuditEvent :execrows
INSERT INTO audit_events (
    id, event_type, actor_type, actor_id, subject_type, subject_id,
    detail_json, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT DO NOTHING;
