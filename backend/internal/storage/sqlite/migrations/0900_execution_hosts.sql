-- +goose Up
-- +goose StatementBegin
CREATE TABLE execution_hosts (
    id                       TEXT PRIMARY KEY,
    name                     TEXT NOT NULL UNIQUE,
    backend_type             TEXT NOT NULL CHECK (backend_type IN ('local','paseo')),
    transport                TEXT NOT NULL CHECK (transport IN ('local','tailscale','lan','paseo_relay')),
    endpoint                 TEXT NOT NULL DEFAULT '',
    endpoint_secret_ref      TEXT NOT NULL DEFAULT '',
    trust_zone               TEXT NOT NULL CHECK (trust_zone IN ('hobby','work','mixed')),
    enabled                  INTEGER NOT NULL DEFAULT 1,
    max_concurrent_sessions  INTEGER NOT NULL DEFAULT 1,
    server_id                TEXT NOT NULL DEFAULT '',
    paseo_version            TEXT NOT NULL DEFAULT '',
    requires_no_mcp          INTEGER NOT NULL DEFAULT 1,
    requires_no_relay        INTEGER NOT NULL DEFAULT 1,
    last_successful_probe_at TEXT NOT NULL DEFAULT '',
    last_failed_probe_at     TEXT NOT NULL DEFAULT '',
    last_probe_error         TEXT NOT NULL DEFAULT '',
    created_at               TEXT NOT NULL,
    updated_at               TEXT NOT NULL
);

CREATE TABLE execution_host_capabilities (
    host_id    TEXT NOT NULL REFERENCES execution_hosts(id) ON DELETE CASCADE,
    capability TEXT NOT NULL,
    PRIMARY KEY (host_id, capability)
);
CREATE INDEX idx_ehc_capability ON execution_host_capabilities(capability);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE execution_host_capabilities;
DROP TABLE execution_hosts;
-- +goose StatementEnd
