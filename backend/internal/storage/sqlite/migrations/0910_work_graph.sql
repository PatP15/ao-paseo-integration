-- +goose Up
-- +goose StatementBegin
CREATE TABLE work_items (
    id                       TEXT PRIMARY KEY,
    project_id               TEXT NOT NULL,
    parent_work_item_id      TEXT REFERENCES work_items(id) ON DELETE SET NULL,
    title                    TEXT NOT NULL,
    body                     TEXT NOT NULL DEFAULT '',
    acceptance_criteria_json TEXT NOT NULL DEFAULT '[]',
    allowed_scope_json       TEXT NOT NULL DEFAULT '[]',
    excluded_scope_json      TEXT NOT NULL DEFAULT '[]',
    risk_level               TEXT NOT NULL DEFAULT 'normal',
    policy_profile_id        TEXT NOT NULL DEFAULT '',
    approval_state           TEXT NOT NULL CHECK (approval_state IN ('draft','proposed','approved','rejected')),
    lifecycle_fact           TEXT NOT NULL CHECK (lifecycle_fact IN ('open','in_progress','done','cancelled')),
    priority                 INTEGER NOT NULL DEFAULT 100,
    created_by_type          TEXT NOT NULL,
    created_by_id            TEXT NOT NULL DEFAULT '',
    approved_by              TEXT NOT NULL DEFAULT '',
    approved_at              TEXT NOT NULL DEFAULT '',
    created_at               TEXT NOT NULL,
    updated_at               TEXT NOT NULL
);

CREATE TABLE work_item_deps (
    work_item_id         TEXT NOT NULL REFERENCES work_items(id) ON DELETE CASCADE,
    related_work_item_id TEXT NOT NULL REFERENCES work_items(id) ON DELETE CASCADE,
    relationship         TEXT NOT NULL CHECK (relationship IN ('blocks','parent','related')),
    PRIMARY KEY (work_item_id, related_work_item_id, relationship),
    CHECK (work_item_id <> related_work_item_id)
);

CREATE TABLE work_item_sessions (
    work_item_id    TEXT NOT NULL REFERENCES work_items(id) ON DELETE CASCADE,
    session_id      TEXT NOT NULL,
    role            TEXT NOT NULL CHECK (role IN ('planner','implementer','reviewer','verifier')),
    attempt_number  INTEGER NOT NULL DEFAULT 1,
    is_active_owner INTEGER NOT NULL DEFAULT 0,
    created_at      TEXT NOT NULL,
    released_at     TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (work_item_id, session_id)
);

CREATE UNIQUE INDEX idx_wis_one_active_implementer
    ON work_item_sessions(work_item_id)
    WHERE is_active_owner = 1 AND role = 'implementer';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE work_item_sessions;
DROP TABLE work_item_deps;
DROP TABLE work_items;
-- +goose StatementEnd
