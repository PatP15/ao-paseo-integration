-- name: GetAutoResumeSettings :one
SELECT enabled, resume_prompt, updated_at FROM auto_resume_settings WHERE id = 1;

-- The singleton row is seeded by the migration, so this only ever updates. An
-- INSERT ... ON CONFLICT here would be dead code that hid a missing row.
-- name: PutAutoResumeSettings :execrows
UPDATE auto_resume_settings
SET enabled = ?, resume_prompt = ?, updated_at = ?
WHERE id = 1;
