-- +goose Up
-- +goose StatementBegin
CREATE TABLE project_host_bindings (
    project_id     TEXT NOT NULL,
    host_id        TEXT NOT NULL REFERENCES execution_hosts(id) ON DELETE CASCADE,
    host_repo_path TEXT NOT NULL,
    base_branch    TEXT NOT NULL DEFAULT 'main',
    priority       INTEGER NOT NULL DEFAULT 100,
    enabled        INTEGER NOT NULL DEFAULT 1,
    setup_profile  TEXT NOT NULL DEFAULT '',
    created_at     TEXT NOT NULL,
    updated_at     TEXT NOT NULL,
    PRIMARY KEY (project_id, host_id)
);
-- +goose StatementEnd

-- +goose Down
DROP TABLE project_host_bindings;
