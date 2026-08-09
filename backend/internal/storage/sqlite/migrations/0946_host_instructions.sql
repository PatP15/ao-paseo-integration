-- U9a: the confirmed copy of each host's machine-scope CLAUDE.md, exactly the
-- shape and ownership rules of execution_host_prefs — a cache of a remote
-- fact whose confirmed_at carries the staleness.

-- +goose Up
-- +goose StatementBegin
CREATE TABLE execution_host_instructions (
    host_id      TEXT PRIMARY KEY REFERENCES execution_hosts(id) ON DELETE CASCADE,
    content      TEXT NOT NULL,
    sha256       TEXT NOT NULL,
    file_exists  INTEGER NOT NULL DEFAULT 1,
    confirmed_at TEXT NOT NULL
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE execution_host_instructions;
-- +goose StatementEnd
