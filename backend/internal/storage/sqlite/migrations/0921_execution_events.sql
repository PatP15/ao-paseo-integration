-- +goose Up
-- +goose StatementBegin
CREATE TABLE execution_events (
    id                TEXT PRIMARY KEY,
    session_id        TEXT NOT NULL,
    host_id           TEXT NOT NULL,
    launch_id         TEXT NOT NULL DEFAULT '',
    protocol_event_id TEXT NOT NULL DEFAULT '',
    protocol_seq      INTEGER,
    event_type        TEXT NOT NULL,
    transport         TEXT NOT NULL CHECK (transport IN ('terminal','sentinel','inspect','output_schema')),
    payload_json      TEXT NOT NULL,
    payload_sha256    TEXT NOT NULL,
    raw_line          TEXT NOT NULL DEFAULT '',
    observed_at       TEXT NOT NULL,
    ingested_at       TEXT NOT NULL,
    applied           INTEGER NOT NULL DEFAULT 0
);

CREATE UNIQUE INDEX idx_ee_protocol
    ON execution_events(session_id, protocol_event_id)
    WHERE protocol_event_id <> '';

CREATE UNIQUE INDEX idx_ee_observation
    ON execution_events(session_id, event_type, payload_sha256)
    WHERE protocol_event_id = '';
-- +goose StatementEnd

-- +goose Down
DROP TABLE execution_events;
