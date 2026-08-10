-- U12: one row per usage-limit death AO decided to act on.
--
-- A row per ATTEMPT, not per session. The cap ("at most five auto-resumes per
-- session") then reads straight off a COUNT, and the same rows are the audit
-- trail: what notice was read, when the limit was expected to lift, whether the
-- reset time was parsed or guessed, and what became of the resume. A single
-- mutable row per session would answer the cap and lose the history.
--
-- state is the row's whole lifecycle:
--   pending   — scheduled, not yet sent.
--   resumed   — the prompt was delivered (locally, or queued on the outbox).
--   failed    — delivery was attempted and refused; detail says why.
--   cancelled — the resume no longer applies (session revived on its own, was
--               terminated, or the schedule went stale while AO was down).
-- Only pending rows are ever acted on, so a settled row is immutable history.
--
-- The partial unique index is the concurrency guard: two ticks racing on the
-- same session cannot both schedule a resume, so an agent cannot be nudged
-- twice for one death.
--
-- session_id carries no foreign key, matching every other table in the 09xx
-- block (execution_events, execution_commands): AO's session delete path
-- enumerates its own cleanup rather than relying on cascades.

-- +goose Up
-- +goose StatementBegin
CREATE TABLE auto_resume_schedule (
    id          TEXT PRIMARY KEY,
    session_id  TEXT NOT NULL,
    launch_id   TEXT NOT NULL DEFAULT '',
    attempt     INTEGER NOT NULL,
    state       TEXT NOT NULL CHECK (state IN ('pending','resumed','failed','cancelled')),
    resume_at   TEXT NOT NULL,
    exact_reset INTEGER NOT NULL DEFAULT 0,
    notice      TEXT NOT NULL DEFAULT '',
    detail      TEXT NOT NULL DEFAULT '',
    detected_at TEXT NOT NULL,
    updated_at  TEXT NOT NULL
);

CREATE UNIQUE INDEX idx_auto_resume_one_pending
    ON auto_resume_schedule(session_id)
    WHERE state = 'pending';

CREATE INDEX idx_auto_resume_due
    ON auto_resume_schedule(resume_at)
    WHERE state = 'pending';

CREATE INDEX idx_auto_resume_session
    ON auto_resume_schedule(session_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE auto_resume_schedule;
-- +goose StatementEnd
