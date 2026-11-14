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
-- Raises a change, or refreshes the payload and detected_at of the same
-- pending change (kind, member_number, list_id). Not raised when an admin
-- already rejected the same change with an identical payload. Returns 1
-- when the change is now pending, 0 when it was suppressed.
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
DO UPDATE SET payload = EXCLUDED.payload, detected_at = now();

-- name: ListNewsletterLists :many
-- GET /lists: every list with our subscription counts per status. Deleted
-- lists only with include_deleted. id narrows it to one list (PUT /lists/{id}
-- returns the updated list in the same shape).
SELECT
    list.id,
    list.name,
    list.description,
    list.visibility,
    list.listmonk_id,
    list.listmonk_subscriber_count,
    list.synced_at,
    list.deleted_at,
    count(*) FILTER (WHERE subscription.status = 'subscribed') AS subscribed_count,
    count(*) FILTER (WHERE subscription.status = 'unsubscribed') AS unsubscribed_count,
    count(*) FILTER (WHERE subscription.status = 'removed') AS removed_count
FROM newsletter_lists AS list
LEFT JOIN subscriptions AS subscription ON subscription.list_id = list.id
WHERE (sqlc.arg(include_deleted)::bool OR list.deleted_at IS NULL)
  AND (sqlc.narg(id)::uuid IS NULL OR list.id = sqlc.narg(id)::uuid)
GROUP BY list.id
ORDER BY list.name, list.id;

-- name: UpdateListVisibility :execrows
UPDATE newsletter_lists
SET visibility = sqlc.arg(visibility), updated_at = now()
WHERE id = sqlc.arg(id);

-- name: ListRecentSyncRuns :many
-- GET /sync-runs: the latest runs of each kind, newest first.
SELECT id, kind, status, started_at, finished_at, items_fetched, changes_detected, error
FROM (
    SELECT *, row_number() OVER (PARTITION BY kind ORDER BY started_at DESC) AS position
    FROM sync_runs
) AS ranked
WHERE position <= sqlc.arg(per_kind)::int
ORDER BY kind, started_at DESC;

-- name: SyncRunIsRunning :one
SELECT EXISTS (SELECT 1 FROM sync_runs WHERE kind = sqlc.arg(kind) AND status = 'running');

-- name: ListRules :many
SELECT * FROM subscription_rules ORDER BY created_at, id;

-- name: CreateRule :one
INSERT INTO subscription_rules (kind, list_id, city_contains, enabled, note)
VALUES (sqlc.arg(kind), sqlc.arg(list_id), sqlc.narg(city_contains), sqlc.arg(enabled), sqlc.narg(note))
RETURNING *;

-- name: UpdateRule :one
UPDATE subscription_rules
SET kind = sqlc.arg(kind),
    list_id = sqlc.arg(list_id),
    city_contains = sqlc.narg(city_contains),
    enabled = sqlc.arg(enabled),
    note = sqlc.narg(note)
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: DeleteRule :execrows
DELETE FROM subscription_rules WHERE id = sqlc.arg(id);

-- name: ListIsActive :one
SELECT EXISTS (SELECT 1 FROM newsletter_lists WHERE id = sqlc.arg(id) AND deleted_at IS NULL);

-- name: GetSubscriber :one
SELECT * FROM subscribers WHERE id = sqlc.arg(id);

-- name: GetSubscriberByMemberNumber :one
SELECT * FROM subscribers WHERE member_number = sqlc.arg(member_number);

-- name: GetSubscriberByEmail :one
SELECT * FROM subscribers WHERE lower(email) = lower(sqlc.arg(email));

-- name: CreateMember :one
INSERT INTO subscribers (email, name, member_number, city, lang)
VALUES (sqlc.arg(email), sqlc.arg(name), sqlc.arg(member_number), sqlc.narg(city), sqlc.arg(lang))
RETURNING *;

-- name: SetMemberFields :one
-- Updates a member's data, or turns a non-member subscriber into a member.
UPDATE subscribers
SET email = sqlc.arg(email),
    name = sqlc.arg(name),
    member_number = sqlc.arg(member_number),
    city = sqlc.narg(city),
    lang = sqlc.arg(lang),
    updated_at = now()
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: ListAllLists :many
SELECT * FROM newsletter_lists ORDER BY name, id;

-- name: ListSubscriptions :many
-- A subscriber's subscriptions with their list, for responses and applyMember.
SELECT
    subscription.list_id,
    list.name AS list_name,
    list.listmonk_id,
    list.deleted_at AS list_deleted_at,
    subscription.status,
    subscription.source,
    subscription.rule_id,
    subscription.listmonk_status,
    subscription.created_at,
    subscription.updated_at
FROM subscriptions AS subscription
JOIN newsletter_lists AS list ON list.id = subscription.list_id
WHERE subscription.subscriber_id = sqlc.arg(subscriber_id)
ORDER BY list.name, list.id;

-- name: InsertSubscription :execrows
-- Creates a row only; an existing row for the list is left alone.
INSERT INTO subscriptions (subscriber_id, list_id, status, source, rule_id)
VALUES (sqlc.arg(subscriber_id), sqlc.arg(list_id), sqlc.arg(status), sqlc.arg(source), sqlc.narg(rule_id))
ON CONFLICT (subscriber_id, list_id) DO NOTHING;

-- name: SetSubscriptionStatus :exec
UPDATE subscriptions
SET status = sqlc.arg(status), updated_at = now()
WHERE subscriber_id = sqlc.arg(subscriber_id) AND list_id = sqlc.arg(list_id);

-- name: EnqueuePushSubscriber :exec
-- One push job per subscriber: the worker reads current state when it runs.
-- A pending job is touched instead (due now, new updated_at), so a worker
-- already processing it sees the change and keeps it for another run. A
-- dead push is replaced by the new one.
WITH replaced_dead AS (
    DELETE FROM provider_jobs
    WHERE kind = 'push_subscriber' AND status = 'dead' AND subscriber_id = sqlc.arg(subscriber_id)
)
INSERT INTO provider_jobs (kind, subscriber_id)
VALUES ('push_subscriber', sqlc.arg(subscriber_id))
ON CONFLICT (subscriber_id) WHERE kind = 'push_subscriber' AND status = 'pending'
DO UPDATE SET next_attempt_at = now(), updated_at = clock_timestamp();

-- name: EnqueueDeleteSubscriber :exec
INSERT INTO provider_jobs (kind, listmonk_subscriber_id)
VALUES ('delete_subscriber', sqlc.arg(listmonk_subscriber_id));

-- name: DeleteSubscriber :one
-- Subscriptions and pending push jobs cascade. Returns the provider ID for
-- the delete_subscriber job.
DELETE FROM subscribers WHERE id = sqlc.arg(id) RETURNING listmonk_subscriber_id;

-- name: DeleteSubscriberByMemberNumber :one
DELETE FROM subscribers WHERE member_number = sqlc.arg(member_number) RETURNING listmonk_subscriber_id;

-- name: GetList :one
SELECT * FROM newsletter_lists WHERE id = sqlc.arg(id);

-- name: GetSubscription :one
SELECT * FROM subscriptions WHERE subscriber_id = sqlc.arg(subscriber_id) AND list_id = sqlc.arg(list_id);

-- name: ListListSubscribers :many
-- GET /lists/{id}/subscribers: the list's subscriptions with their
-- subscriber, by name. status NULL means every status. limit/offset paging
-- with count(*) OVER () as the total.
SELECT
    subscriber.id AS subscriber_id,
    subscriber.email,
    subscriber.name,
    subscriber.member_number,
    subscription.status,
    subscription.source,
    subscription.rule_id,
    subscription.listmonk_status,
    subscription.created_at,
    subscription.updated_at,
    count(*) OVER () AS total_count
FROM subscriptions AS subscription
JOIN subscribers AS subscriber ON subscriber.id = subscription.subscriber_id
WHERE subscription.list_id = sqlc.arg(list_id)
  AND (sqlc.narg(status)::text IS NULL OR subscription.status = sqlc.narg(status)::text)
ORDER BY lower(subscriber.name), subscriber.id
LIMIT sqlc.arg(lim)::int OFFSET sqlc.arg(off)::int;

-- name: ClaimDueJob :one
-- Takes the next due job and leases it: next_attempt_at moves past the
-- lease so no other run picks it up meanwhile. The returned updated_at
-- identifies this claim (see DeleteFinishedJob).
UPDATE provider_jobs
SET next_attempt_at = now() + make_interval(secs => sqlc.arg(lease_seconds)::int), updated_at = clock_timestamp()
WHERE id = (
    SELECT id FROM provider_jobs
    WHERE status = 'pending' AND next_attempt_at <= now()
    ORDER BY next_attempt_at
    LIMIT 1
    FOR UPDATE SKIP LOCKED
)
RETURNING *;

-- name: DeleteFinishedJob :execrows
-- Deletes a successful job unless it was enqueued again while it ran
-- (updated_at moved), in which case it stays pending and runs again.
DELETE FROM provider_jobs WHERE id = sqlc.arg(id) AND updated_at = sqlc.arg(claimed_at);

-- name: FailJob :exec
UPDATE provider_jobs
SET attempt_count = attempt_count + 1,
    last_error = sqlc.arg(last_error),
    status = CASE WHEN attempt_count + 1 >= sqlc.arg(max_attempts)::int THEN 'dead' ELSE 'pending' END,
    next_attempt_at = now() + make_interval(secs => sqlc.arg(retry_in_seconds)::int),
    updated_at = clock_timestamp()
WHERE id = sqlc.arg(id);

-- name: LinkListmonkSubscriber :exec
UPDATE subscribers
SET listmonk_subscriber_id = sqlc.narg(listmonk_subscriber_id), updated_at = now()
WHERE id = sqlc.arg(id);

-- name: SetListmonkStatus :exec
UPDATE subscriptions
SET listmonk_status = sqlc.narg(listmonk_status)
WHERE subscriber_id = sqlc.arg(subscriber_id) AND list_id = sqlc.arg(list_id);

-- name: ListJobs :many
-- GET /jobs: newest first, with the subscriber for display. status NULL
-- means every status.
SELECT
    job.*,
    subscriber.email AS subscriber_email,
    subscriber.member_number AS subscriber_member_number,
    count(*) OVER () AS total_count
FROM provider_jobs AS job
LEFT JOIN subscribers AS subscriber ON subscriber.id = job.subscriber_id
WHERE sqlc.narg(status)::text IS NULL OR job.status = sqlc.narg(status)::text
ORDER BY job.created_at DESC, job.id
LIMIT sqlc.arg(lim)::int OFFSET sqlc.arg(off)::int;

-- name: RetryJob :one
-- Dead job back to pending, due now, with a fresh attempt count.
UPDATE provider_jobs
SET status = 'pending', attempt_count = 0, next_attempt_at = now(), updated_at = clock_timestamp()
WHERE id = sqlc.arg(id) AND status = 'dead'
RETURNING *;

-- name: GetJob :one
SELECT * FROM provider_jobs WHERE id = sqlc.arg(id);

-- name: GetSubscriberByListmonkID :one
SELECT * FROM subscribers WHERE listmonk_subscriber_id = sqlc.arg(listmonk_subscriber_id);

-- name: CreateNonMember :one
-- Listmonk subscription import: someone added in the Listmonk UI.
INSERT INTO subscribers (email, name, listmonk_subscriber_id)
VALUES (sqlc.arg(email), sqlc.arg(name), sqlc.arg(listmonk_subscriber_id))
RETURNING *;

-- name: ListSubscriberIDsWithPushJobs :many
-- Subscribers whose unsent (pending or dead) push wins over Listmonk's state.
SELECT subscriber_id FROM provider_jobs WHERE kind = 'push_subscriber';

-- name: ListListmonkIDsWithDeleteJobs :many
-- Listmonk subscribers we deleted but Listmonk still has until the job runs.
SELECT listmonk_subscriber_id::int FROM provider_jobs WHERE kind = 'delete_subscriber';

-- name: ListLinkedSubscribedOnList :many
-- Subscribed rows on a list whose subscriber is linked to Listmonk.
SELECT subscriber.id, subscriber.listmonk_subscriber_id::int AS listmonk_subscriber_id
FROM subscriptions AS subscription
JOIN subscribers AS subscriber ON subscriber.id = subscription.subscriber_id
WHERE subscription.list_id = sqlc.arg(list_id)
  AND subscription.status = 'subscribed'
  AND subscriber.listmonk_subscriber_id IS NOT NULL;

-- name: DeleteUndetectedMemberChanges :execrows
-- orca reconciliation, in its transaction: pending member and subscription
-- changes this run did not detect again (detected_at older than the
-- transaction) are resolved; drop them so nobody approves a stale change.
DELETE FROM pending_changes
WHERE status = 'pending'
  AND kind IN ('member_new', 'member_updated', 'member_removed', 'subscription_add', 'subscription_remove')
  AND detected_at < now();

-- name: ListMembers :many
SELECT * FROM subscribers WHERE member_number IS NOT NULL ORDER BY member_number;

-- name: ListMemberSubscriptions :many
-- Every member's subscriptions, for the orca reconciliation.
SELECT subscription.*
FROM subscriptions AS subscription
JOIN subscribers AS subscriber ON subscriber.id = subscription.subscriber_id
WHERE subscriber.member_number IS NOT NULL;

-- name: ListPendingChanges :many
-- GET /changes: newest first, with the list name for list changes. Every
-- filter is nullable.
SELECT
    change.*,
    list.name AS list_name,
    count(*) OVER () AS total_count
FROM pending_changes AS change
LEFT JOIN newsletter_lists AS list ON list.id = change.list_id
WHERE (sqlc.narg(status)::text IS NULL OR change.status = sqlc.narg(status)::text)
  AND (sqlc.narg(kind)::text IS NULL OR change.kind = sqlc.narg(kind)::text)
ORDER BY change.detected_at DESC, change.id
LIMIT sqlc.arg(lim)::int OFFSET sqlc.arg(off)::int;

-- name: LockPendingChange :one
SELECT * FROM pending_changes WHERE id = sqlc.arg(id) FOR UPDATE;

-- name: DecidePendingChange :one
UPDATE pending_changes
SET status = sqlc.arg(status), decided_at = now(), decided_by = sqlc.arg(decided_by)
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: SoftDeleteList :exec
UPDATE newsletter_lists SET deleted_at = now(), updated_at = now() WHERE id = sqlc.arg(id) AND deleted_at IS NULL;

-- name: SetListmonkSubscriberCount :exec
-- After the worker pushes changes: refresh Listmonk's count only. Creating
-- lists and synced_at stay with the list sync.
UPDATE newsletter_lists
SET listmonk_subscriber_count = sqlc.arg(listmonk_subscriber_count)::int
WHERE listmonk_id = sqlc.arg(listmonk_id)::int;
