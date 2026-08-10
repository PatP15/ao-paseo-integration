-- name: GetAutoResumeSettings :one
SELECT enabled, resume_prompt, updated_at FROM auto_resume_settings WHERE id = 1;

-- The singleton row is seeded by the migration, so this only ever updates. An
-- INSERT ... ON CONFLICT here would be dead code that hid a missing row.
-- name: PutAutoResumeSettings :execrows
UPDATE auto_resume_settings
SET enabled = ?, resume_prompt = ?, updated_at = ?
WHERE id = 1;

-- name: InsertAutoResume :exec
INSERT INTO auto_resume_schedule (
    id, session_id, launch_id, attempt, state, resume_at, exact_reset,
    notice, detail, detected_at, updated_at
) VALUES (?, ?, ?, ?, 'pending', ?, ?, ?, '', ?, ?);

-- name: GetPendingAutoResume :one
SELECT id, session_id, launch_id, attempt, state, resume_at, exact_reset,
       notice, detail, detected_at, updated_at
FROM auto_resume_schedule
WHERE session_id = ? AND state = 'pending';

-- Every row counts against the cap, settled or not: the cap bounds how many
-- times AO acted on one session, and a delivery that failed still spent an act.
-- name: CountAutoResumes :one
SELECT CAST(COUNT(*) AS INTEGER) AS attempts
FROM auto_resume_schedule WHERE session_id = ?;

-- name: LastAutoResumeDetectedAt :one
SELECT CAST(COALESCE(MAX(detected_at), '') AS TEXT) AS detected_at
FROM auto_resume_schedule WHERE session_id = ?;

-- name: ListDueAutoResumes :many
SELECT id, session_id, launch_id, attempt, state, resume_at, exact_reset,
       notice, detail, detected_at, updated_at
FROM auto_resume_schedule
WHERE state = 'pending' AND resume_at <= ?
ORDER BY resume_at, id
LIMIT ?;

-- name: ListPendingAutoResumes :many
SELECT id, session_id, launch_id, attempt, state, resume_at, exact_reset,
       notice, detail, detected_at, updated_at
FROM auto_resume_schedule
WHERE state = 'pending'
ORDER BY resume_at, id;

-- The state guard is what makes settling idempotent: a row another tick already
-- resolved is left alone rather than overwritten with a second verdict.
-- name: SettleAutoResume :execrows
UPDATE auto_resume_schedule
SET state = ?, detail = ?, updated_at = ?
WHERE id = ? AND state = 'pending';

-- name: CancelPendingAutoResumes :execrows
UPDATE auto_resume_schedule
SET state = 'cancelled', detail = ?, updated_at = ?
WHERE session_id = ? AND state = 'pending';
