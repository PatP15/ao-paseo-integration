-- +goose Up
-- +goose StatementBegin
CREATE TABLE human_questions (
    id                   TEXT PRIMARY KEY,
    session_id           TEXT NOT NULL,
    work_item_id         TEXT REFERENCES work_items(id) ON DELETE SET NULL,
    source               TEXT NOT NULL CHECK (source IN ('agent_event','paseo_permission')),
    external_question_id TEXT NOT NULL DEFAULT '',
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
    id                   TEXT PRIMARY KEY,
    session_id           TEXT NOT NULL,
    sequence             INTEGER NOT NULL,
    summary              TEXT NOT NULL,
    completed_steps_json TEXT NOT NULL DEFAULT '[]',
    remaining_steps_json TEXT NOT NULL DEFAULT '[]',
    test_evidence_json   TEXT NOT NULL DEFAULT '[]',
    commit_sha           TEXT NOT NULL DEFAULT '',
    branch_pushed        INTEGER NOT NULL DEFAULT 0,
    created_at           TEXT NOT NULL,
    UNIQUE (session_id, sequence)
);

CREATE TABLE audit_events (
    id           TEXT PRIMARY KEY,
    event_type   TEXT NOT NULL,
    actor_type   TEXT NOT NULL,
    actor_id     TEXT NOT NULL DEFAULT '',
    subject_type TEXT NOT NULL,
    subject_id   TEXT NOT NULL,
    detail_json  TEXT NOT NULL DEFAULT '{}',
    created_at   TEXT NOT NULL
);
CREATE INDEX idx_audit_subject ON audit_events(subject_type, subject_id);
CREATE INDEX idx_audit_created ON audit_events(created_at);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE audit_events;
DROP TABLE session_checkpoints;
DROP TABLE human_questions;
-- +goose StatementEnd
