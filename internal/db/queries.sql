-- name: CreateAuditLog :exec
-- Written by the handler.AuditLog middleware once per authenticated
-- POST/PUT/PATCH/DELETE request, after the handler has responded.
INSERT INTO audit_logs (
    actor_sub, actor_email,
    category, action, method, route, path, status_code
) VALUES (
    sqlc.arg(actor_sub), sqlc.narg(actor_email),
    sqlc.arg(category), sqlc.arg(action), sqlc.arg(method), sqlc.arg(route), sqlc.arg(path), sqlc.arg(status_code)
);

-- name: ListAuditLogs :many
-- Admin audit log (GET /audit-logs), newest first. Every filter arg is
-- nullable — NULL means "don't filter on this". date_from/date_to are
-- inclusive calendar days, compared against created_at in the session time
-- zone. limit/offset paging with count(*) OVER () as the total.
SELECT
    id,
    created_at,
    actor_sub,
    actor_email,
    category,
    action,
    method,
    route,
    path,
    status_code,
    count(*) OVER () AS total_count
FROM audit_logs
WHERE (sqlc.narg(category)::text IS NULL OR category = sqlc.narg(category)::text)
  AND (sqlc.narg(actor_sub)::text IS NULL OR actor_sub = sqlc.narg(actor_sub)::text)
  AND (sqlc.narg(date_from)::date IS NULL OR created_at >= sqlc.narg(date_from)::date)
  AND (sqlc.narg(date_to)::date IS NULL OR created_at < sqlc.narg(date_to)::date + 1)
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg(lim)::int OFFSET sqlc.arg(off)::int;

-- name: StartSyncRun :one
-- Fails with a unique violation (sync_runs_running_idx) while another run
-- of the same kind is still running.
INSERT INTO sync_runs (kind) VALUES (sqlc.arg(kind)) RETURNING id;

-- name: FinishSyncRun :exec
UPDATE sync_runs
SET status = sqlc.arg(status),
    finished_at = now(),
    items_fetched = sqlc.arg(items_fetched),
    changes_detected = sqlc.arg(changes_detected),
    error = sqlc.narg(error)
WHERE id = sqlc.arg(id);

-- name: FailStaleSyncRuns :execrows
-- Run once at startup: a run still 'running' was cut off by a crash or restart.
UPDATE sync_runs
SET status = 'failed', finished_at = now(), error = 'interrupted: server restarted'
WHERE status = 'running';

-- name: UpsertListmonkList :exec
-- Listmonk list sync. New lists get the default 'private' visibility; an
-- existing list keeps its visibility and gets deleted_at cleared if it had
-- been soft-deleted and reappeared.
INSERT INTO newsletter_lists (name, description, listmonk_id, listmonk_subscriber_count, synced_at)
VALUES (sqlc.arg(name), sqlc.arg(description), sqlc.arg(listmonk_id)::int, sqlc.arg(listmonk_subscriber_count)::int, now())
ON CONFLICT (listmonk_id) DO UPDATE
SET name = EXCLUDED.name,
    description = EXCLUDED.description,
    listmonk_subscriber_count = EXCLUDED.listmonk_subscriber_count,
    synced_at = now(),
    deleted_at = NULL,
    updated_at = now();

-- name: ListMissingListmonkLists :many
-- Active Listmonk-linked lists that Listmonk no longer returned.
SELECT id, name, listmonk_id::int AS listmonk_id
FROM newsletter_lists
WHERE listmonk_id IS NOT NULL
  AND deleted_at IS NULL
  AND NOT (listmonk_id = ANY (sqlc.arg(seen_listmonk_ids)::int[]))
ORDER BY listmonk_id;

-- name: DeleteResolvedListRemovedChanges :execrows
-- A pending list_removed whose list Listmonk returns again is obsolete.
DELETE FROM pending_changes AS change
USING newsletter_lists AS list
WHERE change.kind = 'list_removed'
  AND change.status = 'pending'
  AND change.list_id = list.id
  AND list.listmonk_id = ANY (sqlc.arg(seen_listmonk_ids)::int[]);

-- name: RaisePendingChange :execrows
-- Raises a change, or refreshes the payload of the same pending change
-- (kind, member_number, list_id). Not raised when an admin already rejected
-- the same change with an identical payload. Returns 1 when the change is
-- now pending, 0 when it was suppressed.
INSERT INTO pending_changes (kind, member_number, list_id, payload)
SELECT sqlc.arg(kind)::text, sqlc.narg(member_number)::int, sqlc.narg(list_id)::uuid, sqlc.arg(payload)::jsonb
WHERE NOT EXISTS (
    SELECT 1 FROM pending_changes AS rejected
    WHERE rejected.status = 'rejected'
      AND rejected.kind = sqlc.arg(kind)::text
      AND rejected.member_number IS NOT DISTINCT FROM sqlc.narg(member_number)::int
      AND rejected.list_id IS NOT DISTINCT FROM sqlc.narg(list_id)::uuid
      AND rejected.payload = sqlc.arg(payload)::jsonb
)
ON CONFLICT (kind, member_number, list_id) WHERE status = 'pending'
DO UPDATE SET payload = EXCLUDED.payload;
