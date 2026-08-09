-- U9 host maintenance channel: the persisted half of what the channel reads.
-- Both tables are caches of remote facts — the host's daemon is the authority
-- and every row carries when AO captured it, so staleness is visible, never
-- hidden.

-- +goose Up
-- +goose StatementBegin
CREATE TABLE execution_host_skills (
    host_id     TEXT NOT NULL REFERENCES execution_hosts(id) ON DELETE CASCADE,
    name        TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    captured_at TEXT NOT NULL,
    PRIMARY KEY (host_id, name)
);

CREATE TABLE execution_host_prefs (
    host_id      TEXT PRIMARY KEY REFERENCES execution_hosts(id) ON DELETE CASCADE,
    content      TEXT NOT NULL,
    sha256       TEXT NOT NULL,
    file_exists  INTEGER NOT NULL DEFAULT 1,
    confirmed_at TEXT NOT NULL
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE execution_host_prefs;
DROP TABLE execution_host_skills;
-- +goose StatementEnd
