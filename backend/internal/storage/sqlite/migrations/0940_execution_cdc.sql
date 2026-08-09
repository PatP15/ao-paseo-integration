-- The upstream change_log vocabulary is closed and project-scoped. These
-- fork-owned triggers therefore emit its existing invalidation event and keep
-- host changes scoped to projects already bound to that host. A later API/UI
-- PR can add a typed transport vocabulary without rebuilding an upstream table.
-- +goose Up

-- +goose StatementBegin
CREATE TRIGGER work_items_cdc_insert
AFTER INSERT ON work_items
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    VALUES (NEW.project_id, NULL, 'session_updated',
        json_object('scope', 'work_item', 'id', NEW.id), NEW.updated_at);
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER work_items_cdc_update
AFTER UPDATE ON work_items
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    VALUES (NEW.project_id, NULL, 'session_updated',
        json_object('scope', 'work_item', 'id', NEW.id), NEW.updated_at);
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER work_item_sessions_cdc_insert
AFTER INSERT ON work_item_sessions
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    SELECT project_id, NULL, 'session_updated',
        json_object('scope', 'work_item_session', 'id', NEW.work_item_id), NEW.created_at
    FROM work_items WHERE id = NEW.work_item_id;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER work_item_sessions_cdc_update
AFTER UPDATE ON work_item_sessions
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    SELECT project_id, NULL, 'session_updated',
        json_object('scope', 'work_item_session', 'id', NEW.work_item_id), datetime('now')
    FROM work_items WHERE id = NEW.work_item_id;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER session_execution_bindings_cdc_insert
AFTER INSERT ON session_execution_bindings
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    SELECT project_id, NEW.session_id, 'session_updated',
        json_object('scope', 'execution_binding', 'id', NEW.session_id), NEW.created_at
    FROM sessions WHERE id = NEW.session_id;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER session_execution_bindings_cdc_update
AFTER UPDATE ON session_execution_bindings
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    SELECT project_id, NEW.session_id, 'session_updated',
        json_object('scope', 'execution_binding', 'id', NEW.session_id), datetime('now')
    FROM sessions WHERE id = NEW.session_id;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER human_questions_cdc_insert
AFTER INSERT ON human_questions
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    SELECT project_id, NEW.session_id, 'session_updated',
        json_object('scope', 'human_question', 'id', NEW.id), NEW.created_at
    FROM sessions WHERE id = NEW.session_id;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER human_questions_cdc_update
AFTER UPDATE ON human_questions
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    SELECT project_id, NEW.session_id, 'session_updated',
        json_object('scope', 'human_question', 'id', NEW.id), datetime('now')
    FROM sessions WHERE id = NEW.session_id;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER session_checkpoints_cdc_insert
AFTER INSERT ON session_checkpoints
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    SELECT project_id, NEW.session_id, 'session_updated',
        json_object('scope', 'session_checkpoint', 'id', NEW.id), NEW.created_at
    FROM sessions WHERE id = NEW.session_id;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER execution_hosts_cdc_update
AFTER UPDATE ON execution_hosts
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    SELECT DISTINCT project_id, NULL, 'session_updated',
        json_object('scope', 'execution_host', 'id', NEW.id), NEW.updated_at
    FROM project_host_bindings WHERE host_id = NEW.id;
END;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TRIGGER execution_hosts_cdc_update;
DROP TRIGGER session_checkpoints_cdc_insert;
DROP TRIGGER human_questions_cdc_update;
DROP TRIGGER human_questions_cdc_insert;
DROP TRIGGER session_execution_bindings_cdc_update;
DROP TRIGGER session_execution_bindings_cdc_insert;
DROP TRIGGER work_item_sessions_cdc_update;
DROP TRIGGER work_item_sessions_cdc_insert;
DROP TRIGGER work_items_cdc_update;
DROP TRIGGER work_items_cdc_insert;
-- +goose StatementEnd
