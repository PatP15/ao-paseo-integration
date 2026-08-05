-- +goose Up
-- +goose StatementBegin
CREATE TABLE session_briefs (
    id                  TEXT PRIMARY KEY,
    session_id          TEXT NOT NULL,
    version             INTEGER NOT NULL,
    schema_version      TEXT NOT NULL,
    brief_json          TEXT NOT NULL,
    brief_sha256        TEXT NOT NULL,
    report_nonce        TEXT NOT NULL,
    created_at          TEXT NOT NULL,
    supersedes_brief_id TEXT REFERENCES session_briefs(id),
    UNIQUE (session_id, version)
);

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
    sequence        INTEGER NOT NULL,
    state           TEXT NOT NULL CHECK (state IN ('pending','delivering','acknowledged','failed')),
    attempt_count   INTEGER NOT NULL DEFAULT 0,
    next_attempt_at TEXT NOT NULL DEFAULT '',
    last_error      TEXT NOT NULL DEFAULT '',
    created_at      TEXT NOT NULL,
    acknowledged_at TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_ec_due ON execution_commands(next_attempt_at)
    WHERE state IN ('pending','delivering');
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE execution_commands;
DROP TABLE session_briefs;
-- +goose StatementEnd
