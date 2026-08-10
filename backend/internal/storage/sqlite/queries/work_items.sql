-- name: UpsertWorkItem :exec
INSERT INTO work_items (
    id, project_id, parent_work_item_id, title, body,
    acceptance_criteria_json, allowed_scope_json, excluded_scope_json,
    risk_level, policy_profile_id, approval_state, lifecycle_fact, priority,
    created_by_type, created_by_id, approved_by, approved_at, decision_note,
    created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (id) DO UPDATE SET
    project_id = excluded.project_id,
    parent_work_item_id = excluded.parent_work_item_id,
    title = excluded.title,
    body = excluded.body,
    acceptance_criteria_json = excluded.acceptance_criteria_json,
    allowed_scope_json = excluded.allowed_scope_json,
    excluded_scope_json = excluded.excluded_scope_json,
    risk_level = excluded.risk_level,
    policy_profile_id = excluded.policy_profile_id,
    approval_state = excluded.approval_state,
    lifecycle_fact = excluded.lifecycle_fact,
    priority = excluded.priority,
    created_by_type = excluded.created_by_type,
    created_by_id = excluded.created_by_id,
    approved_by = excluded.approved_by,
    approved_at = excluded.approved_at,
    decision_note = excluded.decision_note,
    updated_at = excluded.updated_at;

-- name: GetWorkItem :one
SELECT * FROM work_items WHERE id = ?;

-- name: SetWorkItemApproval :one
UPDATE work_items
SET approval_state = ?, approved_by = ?, approved_at = ?, decision_note = ?,
    updated_at = ?
WHERE id = ? AND approval_state IN ('draft', 'proposed')
RETURNING *;

-- name: MarkWorkItemInProgress :execrows
UPDATE work_items
SET lifecycle_fact = 'in_progress', updated_at = ?
WHERE id = ? AND approval_state = 'approved'
  AND lifecycle_fact IN ('open', 'in_progress');

-- name: ListWorkItemsByProject :many
SELECT * FROM work_items WHERE project_id = ? ORDER BY priority, created_at, id;

-- name: InsertWorkItemDependency :exec
INSERT INTO work_item_deps (work_item_id, related_work_item_id, relationship)
VALUES (?, ?, ?);

-- name: DeleteWorkItemDependencies :exec
DELETE FROM work_item_deps WHERE work_item_id = ?;

-- name: ListWorkItemDependencies :many
SELECT * FROM work_item_deps WHERE work_item_id = ? ORDER BY relationship, related_work_item_id;

-- name: ClaimWorkItemSession :exec
INSERT INTO work_item_sessions (
    work_item_id, session_id, role, attempt_number, is_active_owner,
    created_at, released_at
) VALUES (?, ?, ?, ?, 1, ?, '');

-- name: ReleaseWorkItemSession :execrows
UPDATE work_item_sessions
SET is_active_owner = 0, released_at = ?
WHERE work_item_id = ? AND session_id = ? AND is_active_owner = 1;

-- name: ListWorkItemSessions :many
SELECT * FROM work_item_sessions WHERE work_item_id = ? ORDER BY attempt_number, created_at;

-- Every session claim in one project, so a work-item list can carry the path to
-- the session running each item without one query per row.
-- name: ListWorkItemSessionsByProject :many
SELECT wis.* FROM work_item_sessions AS wis
JOIN work_items AS wi ON wi.id = wis.work_item_id
WHERE wi.project_id = ?
ORDER BY wis.work_item_id, wis.attempt_number, wis.created_at;
