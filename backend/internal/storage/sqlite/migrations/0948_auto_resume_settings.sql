-- U12: the app-wide policy for restarting a session whose agent was killed by a
-- provider usage limit.
--
-- A singleton row rather than a generic key/value settings table: AO has no
-- app-settings store today, and inventing one for a single typed policy would
-- trade a CHECK-able schema for stringly-typed JSON with no second caller to
-- justify it. The id CHECK is what keeps it a singleton — a second row cannot
-- be inserted, so no reader has to decide which row wins.
--
-- resume_prompt is stored empty when the operator has not customised it. The
-- default text lives in the domain, so improving it ships with the code instead
-- of stranding every install on the wording that was current when they first
-- opened the toggle.

-- +goose Up
-- +goose StatementBegin
CREATE TABLE auto_resume_settings (
    id            INTEGER PRIMARY KEY CHECK (id = 1),
    enabled       INTEGER NOT NULL DEFAULT 0,
    resume_prompt TEXT NOT NULL DEFAULT '',
    updated_at    TEXT NOT NULL DEFAULT ''
);

INSERT INTO auto_resume_settings (id, enabled, resume_prompt, updated_at)
VALUES (1, 0, '', '');
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE auto_resume_settings;
-- +goose StatementEnd
