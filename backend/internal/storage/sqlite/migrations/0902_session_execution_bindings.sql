-- +goose Up
-- +goose StatementBegin
CREATE TABLE session_execution_bindings (
    session_id                 TEXT PRIMARY KEY,
    work_item_id               TEXT REFERENCES work_items(id) ON DELETE SET NULL,
    backend_type               TEXT NOT NULL CHECK (backend_type IN ('local','paseo')),
    host_id                    TEXT NOT NULL REFERENCES execution_hosts(id),
    external_workspace_id      TEXT NOT NULL DEFAULT '',
    external_agent_id          TEXT NOT NULL DEFAULT '',
    external_parent_agent_id   TEXT NOT NULL DEFAULT '',
    bound_server_id            TEXT NOT NULL DEFAULT '',
    workspace_title            TEXT NOT NULL DEFAULT '',
    intent_id                  TEXT NOT NULL DEFAULT '',
    attempt                    INTEGER NOT NULL DEFAULT 1,
    labels_written_json        TEXT NOT NULL DEFAULT '{}',
    branch_name                TEXT NOT NULL DEFAULT '',
    host_workspace_path        TEXT NOT NULL DEFAULT '',
    provider                   TEXT NOT NULL DEFAULT '',
    model                      TEXT NOT NULL DEFAULT '',
    mode                       TEXT NOT NULL DEFAULT '',
    dispatch_generation        INTEGER NOT NULL DEFAULT 1,
    launch_id                  TEXT NOT NULL DEFAULT '',
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
-- +goose StatementEnd

-- +goose Down
DROP TABLE session_execution_bindings;
