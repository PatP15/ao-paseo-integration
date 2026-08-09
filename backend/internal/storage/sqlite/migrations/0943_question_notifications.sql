-- Execution questions surface as notifications (U8). A notification can now
-- name the inbox question it announces (question_id) and the work item that
-- question belongs to (work_item_id), so the dashboard can deep-link straight
-- to the answerable thing instead of just the session.
--
-- The open dedupe index (0041) gains question_id: two open questions on one
-- session are two answerable items and must be two notifications, while rows
-- without a question keep the old (session, type, pr_url) dedupe unchanged
-- via ''.

-- +goose Up
-- +goose StatementBegin
ALTER TABLE notifications ADD COLUMN work_item_id TEXT NOT NULL DEFAULT '';
ALTER TABLE notifications ADD COLUMN question_id TEXT NOT NULL DEFAULT '';
DROP INDEX idx_notifications_open_dedupe;
CREATE UNIQUE INDEX idx_notifications_open_dedupe
    ON notifications(session_id, type, pr_url, question_id)
    WHERE status = 'unread' OR resolved_at IS NULL;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX idx_notifications_open_dedupe;
CREATE UNIQUE INDEX idx_notifications_open_dedupe
    ON notifications(session_id, type, pr_url)
    WHERE status = 'unread' OR resolved_at IS NULL;
ALTER TABLE notifications DROP COLUMN question_id;
ALTER TABLE notifications DROP COLUMN work_item_id;
-- +goose StatementEnd
