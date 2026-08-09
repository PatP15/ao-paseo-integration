-- The worker's home directory, learned through the maintenance channel: every
-- maintenance run's done event reports $HOME, AO persists it here, and later
-- runs create their workspace in it instead of "/" (the only path AO can name
-- before it has learned anything about the host). Deliberately absent from the
-- registration upsert: it is an observed fact like the probe columns, owned by
-- the channel, and a registry edit must not erase it.

-- +goose Up
-- +goose StatementBegin
ALTER TABLE execution_hosts ADD COLUMN maintenance_home TEXT NOT NULL DEFAULT '';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE execution_hosts DROP COLUMN maintenance_home;
-- +goose StatementEnd
