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
