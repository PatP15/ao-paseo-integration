-- The reason a human gave with an approval decision.
--
-- A rejected work item was a dead end for everyone except the person who
-- rejected it: the row said "Rejected", named the decider, and stopped. The
-- decision itself was already durable and attributable — only its reason was
-- thrown away, so nobody else could tell whether the work was wrong, premature,
-- or already done elsewhere.
--
-- One column on the decision, not a separate comment table: there is exactly one
-- decision per work item (the store's guard only permits deciding from
-- draft/proposed, so a decision can never be overwritten), so a note has the
-- same cardinality as approved_by. Empty is the honest default — an approval
-- rarely needs prose, and every row that existed before this column had no note
-- to migrate.

-- +goose Up
-- +goose StatementBegin
ALTER TABLE work_items ADD COLUMN decision_note TEXT NOT NULL DEFAULT '';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE work_items DROP COLUMN decision_note;
-- +goose StatementEnd
