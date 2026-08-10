-- U11: the session timeline has to show what AO sent, not only what the host
-- reported back. Every transport in 0921 names a remote surface an event was
-- *read from*; a message AO delivers through the outbox has no such surface, so
-- it needs its own value rather than a borrowed one that would misattribute
-- AO's own writes to the host.
--
-- SQLite cannot widen a CHECK in place, so the table is rebuilt. It carries no
-- triggers and no foreign keys point at it, so the rebuild is contained to the
-- table and its two unique indexes.

-- +goose Up
-- +goose StatementBegin
CREATE TABLE execution_events_new (
    id                TEXT PRIMARY KEY,
    session_id        TEXT NOT NULL,
    host_id           TEXT NOT NULL,
    launch_id         TEXT NOT NULL DEFAULT '',
    protocol_event_id TEXT NOT NULL DEFAULT '',
    protocol_seq      INTEGER,
    event_type        TEXT NOT NULL,
    transport         TEXT NOT NULL CHECK (transport IN ('terminal','sentinel','inspect','output_schema','outbox')),
    payload_json      TEXT NOT NULL,
    payload_sha256    TEXT NOT NULL,
    raw_line          TEXT NOT NULL DEFAULT '',
    observed_at       TEXT NOT NULL,
    ingested_at       TEXT NOT NULL,
    applied           INTEGER NOT NULL DEFAULT 0
);

INSERT INTO execution_events_new SELECT * FROM execution_events;

DROP TABLE execution_events;

ALTER TABLE execution_events_new RENAME TO execution_events;

CREATE UNIQUE INDEX idx_ee_protocol
    ON execution_events(session_id, protocol_event_id)
    WHERE protocol_event_id <> '';

CREATE UNIQUE INDEX idx_ee_observation
    ON execution_events(session_id, event_type, payload_sha256)
    WHERE protocol_event_id = '';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DELETE FROM execution_events WHERE transport = 'outbox';

CREATE TABLE execution_events_old (
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

INSERT INTO execution_events_old SELECT * FROM execution_events;

DROP TABLE execution_events;

ALTER TABLE execution_events_old RENAME TO execution_events;

CREATE UNIQUE INDEX idx_ee_protocol
    ON execution_events(session_id, protocol_event_id)
    WHERE protocol_event_id <> '';

CREATE UNIQUE INDEX idx_ee_observation
    ON execution_events(session_id, event_type, payload_sha256)
    WHERE protocol_event_id = '';
-- +goose StatementEnd
