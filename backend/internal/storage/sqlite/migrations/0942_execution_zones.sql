-- +goose Up
-- +goose StatementBegin

-- Named execution zones, replacing the hobby|work|mixed CHECK on
-- execution_hosts.trust_zone.
--
-- That enum conflated two independent axes:
--
--   AUTONOMY  — may this run unattended, retry, push?  Enforced by AO policy,
--               which is to say by a row in this table.
--   ISOLATION — can an agent here read another zone's transcripts and
--               credentials?  Enforced by the operating system and by nothing
--               else.  See below.
--
-- Fusing them made "work" imply "needs its own uid", which is disproportionate
-- for most projects: a public research repo and unpublished results under a
-- collaboration agreement are both "work" and need completely different
-- handling. Zones are now operator-named so the vocabulary can match the real
-- sensitivity gradient instead of a two-value guess.
--
-- trust_zone is left in place and unused rather than dropped: SQLite cannot
-- remove a CHECK without a table rebuild, and rebuilding a table that
-- session_execution_bindings references is not worth it for a column we can
-- simply stop reading.
CREATE TABLE execution_zones (
    id                  TEXT PRIMARY KEY,
    name                TEXT NOT NULL UNIQUE,
    description         TEXT NOT NULL DEFAULT '',

    -- Autonomy policy. These are AO's own decisions and are fully enforceable
    -- by the control plane, unlike isolation.
    auto_dispatch       INTEGER NOT NULL DEFAULT 0,
    max_repair_attempts INTEGER NOT NULL DEFAULT 0,
    may_push_branch     INTEGER NOT NULL DEFAULT 0,
    may_create_draft_pr INTEGER NOT NULL DEFAULT 0,
    max_runtime_minutes INTEGER NOT NULL DEFAULT 90,

    created_at          TEXT NOT NULL,
    updated_at          TEXT NOT NULL
);

-- Zone membership. Nullable during migration; a host with no zone is not
-- dispatchable, which fails closed.
ALTER TABLE execution_hosts ADD COLUMN zone_id TEXT REFERENCES execution_zones(id);

-- Isolation is a property of the HOST, not of the zone.
--
-- Two zones that share a uid are not isolated from each other no matter what
-- policy either one carries: Paseo's get_agent_activity is cross-agent and
-- unscoped, it rehydrates archived agents from disk, and AO's own loopback
-- daemon has no authentication. So an agent in either zone can read the other's
-- transcripts and credentials.
--
-- AO cannot verify this. Nothing in the CLI reports which uid a remote daemon
-- runs as, so the flag is OPERATOR-ASSERTED and must be surfaced as such in the
-- UI. Recording it explicitly is still worth it: it turns an assumption people
-- carry in their heads into a visible fact that can be wrong out loud.
ALTER TABLE execution_hosts ADD COLUMN isolated INTEGER NOT NULL DEFAULT 0;
ALTER TABLE execution_hosts ADD COLUMN isolation_note TEXT NOT NULL DEFAULT '';

CREATE INDEX idx_execution_hosts_zone ON execution_hosts(zone_id) WHERE zone_id IS NOT NULL;

-- Projects bind to a zone too, so routing can require that a project only ever
-- lands on a host in its own zone.
ALTER TABLE project_host_bindings ADD COLUMN required_zone_id TEXT REFERENCES execution_zones(id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_execution_hosts_zone;
DROP TABLE IF EXISTS execution_zones;
-- Columns added by ALTER TABLE are left in place: SQLite's DROP COLUMN cannot
-- remove a column referenced by an index or a foreign key, and an unused column
-- is harmless.
-- +goose StatementEnd
