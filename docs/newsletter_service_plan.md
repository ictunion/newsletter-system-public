# Newsletter System: implementation plan

## Context

Today orca subscribes new members to Listmonk directly (`orca/src/listmonk/`). That has three problems:

1. **No lists model.** List IDs 3/4/5 are hardcoded and picked by string-matching the member's city. Nothing syncs from Listmonk's real lists.
2. **Fatal coupling.** The Listmonk call runs inside the member-creation transaction. If Listmonk fails, the member is not created.
3. **No visibility.** orca's `email_subscriptions` table is write-only. Nobody can see who is on which list.

This repo (`newsletter-system`) is a standalone service with its own API, Postgres database and Keycloak client. It owns lists, subscribers and subscription state. It does not extend the main-system monorepo.

### Intended use

- **New member.** orca accepts a member and sends them here with their city. Rules put them on All plus the matching city lists.
- **Workplace changes.** At any later time, orca sends updates: assigned to a workplace, removed from it, moved to another. Each update is applied immediately. The workplace list subscription is added or removed.
- **Admins in orca's UI.**
  - They see every list and the subscribers of a given list.
  - They add or remove members on lists directly.
  - orca's frontend calls this API with the admin's own token.
- **Admins in the Listmonk UI.** They add people to lists by hand, typically new event-specific lists such as invitations. These people may be non-members. Those memberships are imported here, so the state survives a provider change.
- **Members.** They unsubscribe themselves through Listmonk's unsubscribe link. Their opt-out is imported and respected. Later, a member self-management frontend will show the lists visible to members and let them join or leave.
- **Safety net.** A daily reconciliation against orca catches anything missed, such as a lost call or a rule edit. Only what it finds waits for admin approval.
- **Listmonk is optional.** It delivers email, but it is not the source of truth. Lists, subscribers and subscription state live here. The provider can be swapped without losing state.

## Existing scaffold (reuse, don't replace)

- [cmd/server/main.go](../cmd/server/main.go): config, pgx pool, Keycloak provider, stdlib `ServeMux` under `/api`, middleware chain, graceful shutdown.
- [internal/handler](../internal/handler): `RequireRole` / `RequireAnyRole` / `RequireAuth`, the `AuditLog` middleware with its `auditRoutes` map, `Recover` and `MaxBodySize`.
- [internal/keycloak](../internal/keycloak): JWT verification and client roles (`Role` constants).
- [internal/db](../internal/db): sqlc-generated queries. Write SQL in `queries.sql`, then run `make sqlc`.
- [internal/dbtest](../internal/dbtest), [internal/keycloaktest](../internal/keycloaktest): test helpers.
- [frontend](../frontend): React + Vite SPA with OIDC login and `useHasRole`.
- Makefile: `db-init`, `db-start`, `migrate`, `sqlc`, `test`, `start`, `swagger`. Nix flake devShell. CI is `.github/workflows/test.yml`.

New code follows the same shape as bank-system: `internal/syncjob` for scheduled pulls, plain `net/http` clients, sqlc queries and `log`. No router framework and no `slog`.

## Migrations: Goose sequential

- Convert the existing `migrations/20261114000001_audit_logs.sql` to `00001_audit_logs.sql` with `goose fix`. No database has applied it yet.
- New migrations are created with `goose -s create <name> sql`.

| File | Content |
|---|---|
| `00001_audit_logs.sql` | existing |
| `00002_lists.sql` | `newsletter_lists` |
| `00003_subscribers.sql` | `subscribers`, `subscriptions` |
| `00004_rules.sql` | `subscription_rules`, plus `subscriptions.rule_id` FK |
| `00005_provider_jobs.sql` | `provider_jobs` |
| `00006_sync.sql` | `sync_runs`, `pending_changes` |

## Data model

### `newsletter_lists`
Our own list entity. Provider columns are nullable, so a list can exist without Listmonk.

- `id BIGSERIAL PRIMARY KEY`
- `name TEXT NOT NULL`
- `visibility TEXT NOT NULL DEFAULT 'private' CHECK (visibility IN ('private', 'members', 'public'))`
  - `private`: admins only. This is the default, including for lists the Listmonk sync creates.
  - `members`: visible to members in the future self-management frontend.
  - `public`: open to anyone, for example a future website signup. It is stored now, but no public signup route is built yet.
  - It is set by admins. Listmonk's own `public`/`private` type is not copied.
- `listmonk_id INTEGER UNIQUE NULL`
- `listmonk_subscriber_count INTEGER NULL`: Listmonk's own count. The dashboard shows it next to ours so discrepancies are visible.
- `synced_at`, `deleted_at` (soft delete, see list sync), `created_at`, `updated_at`

### `subscribers`
Anyone who receives a newsletter. A member is a subscriber with a `member_number`. Non-members, for example event invitees added in the Listmonk UI, have none.

- `id BIGSERIAL PRIMARY KEY`
- `email TEXT NOT NULL`, with a unique index on `lower(email)`
- `name TEXT NOT NULL`: what goes to the provider. For members it is `first_name || ' ' || last_name`, computed on write.
- Member-only fields, NULL for non-members:
  - `member_number INTEGER UNIQUE NULL`
  - `first_name TEXT NULL`, `last_name TEXT NULL`, `city TEXT NULL`, `lang TEXT NULL`
  - `workplace_newsletter_id INTEGER NULL`: orca's `workplaces.newsletter_id`, stored as received
- `listmonk_subscriber_id INTEGER UNIQUE NULL`
- `created_at`, `updated_at`
- CHECK: `member_number IS NOT NULL` or every other member-only field is NULL. A member also needs `first_name`, `last_name` and `lang`.

### `subscriptions`
One row per subscriber and list. Rows are **never deleted, only re-statused**, except when the subscriber is deleted. That is how the provider sync knows what to remove.

- `subscriber_id BIGINT REFERENCES subscribers ON DELETE CASCADE`
- `list_id BIGINT REFERENCES newsletter_lists ON DELETE RESTRICT`
- `status TEXT NOT NULL CHECK (status IN ('subscribed', 'unsubscribed', 'removed'))`
  - `unsubscribed`: the subscriber opted out themselves, either in the provider or later in the self-management frontend.
  - `removed`: an admin removed them, or their rule or workplace no longer applies.
- `source TEXT NOT NULL CHECK (source IN ('rule', 'workplace', 'manual', 'self'))`: how the row was first created.
  - `manual` comes from an admin, through orca's UI or the Listmonk UI.
  - `self` is reserved for the self-management frontend.
- `rule_id BIGINT NULL REFERENCES subscription_rules ON DELETE SET NULL`: set only for `source = 'rule'`
- `listmonk_status TEXT NULL`: last seen Listmonk per-list status (`unconfirmed` / `confirmed` / `unsubscribed`)
- `created_at`, `updated_at`
- `PRIMARY KEY (subscriber_id, list_id)`

What may change a row automatically:
- Automatic sources (rules, workplace) only create rows, or change their own `subscribed` rows to `removed`.
- They never revive an `unsubscribed` or `removed` row, and they never touch `manual` or `self` rows.
- An admin can re-add a `removed` row. Re-adding an `unsubscribed` row returns `409`, because it would override the person's own opt-out. Only the person can undo it.

### `subscription_rules`
For cities and other member attributes. Workplaces are not rules. This follows bank-system's `categorization_rules`, with one difference: **every** matching enabled rule applies (union), not only the first match.

- `id BIGSERIAL PRIMARY KEY`
- `kind TEXT NOT NULL CHECK (kind IN ('all', 'city'))`. New attribute kinds are added here as they come up.
  - `all`: every member goes to `list_id`.
  - `city`: members whose city contains `city_contains` go to `list_id`. The match is case-insensitive and accent-insensitive: "Praha", "praha 4" and "PRAHA" all match `praha`. Normalize in Go with `golang.org/x/text` (NFD, then strip marks). It is already in the module graph.
- `list_id BIGINT NOT NULL REFERENCES newsletter_lists ON DELETE RESTRICT`
- `city_contains TEXT NULL`
- `enabled BOOLEAN NOT NULL DEFAULT true`, `note TEXT NULL`, `created_at`
- CHECK: `all` takes no `city_contains`. `city` requires it.
- Rules apply to members only, never to non-member subscribers.

No seed rows. List IDs come from the first Listmonk sync, and admins create the rules afterwards.

### Workplace lists (not rules)
- The member's `workplace_newsletter_id` goes to the list whose `listmonk_id` matches, with `source = 'workplace'`.
- No match among active lists: log a warning and skip. We fix the stored ID in orca based on the logs.
- The ID is a Listmonk list ID, because that is what orca stores. Switching providers would need these IDs remapped.
- Workplace is the only group type for now. Other group types later follow the same pattern: a new payload field, a new `source` value, and direct ID matching.

### Applying member data
One pure function, `desiredLists(member, rules, lists)`, with table tests. It returns the matching rule lists plus the workplace list, each tagged with its `source` and `rule_id`.

One function, `applyMember(tx, member)`, runs it and changes the subscription rows:
- A desired list with no row: insert it as `subscribed`.
- An existing `rule` or `workplace` row in `subscribed` that is no longer desired: set it to `removed`.
- Everything else is left alone.
- If anything changed, enqueue `push_subscriber`.

`POST /members`, `PUT /members/{member_number}` and approving a reconciliation change all use `applyMember`.

### `provider_jobs`
The retry queue for pushing state to the provider. A job stores **no PII** and no list IDs. The worker reads current state from the DB when it runs, so a job means "make the provider match our DB for this subscriber".

- `id BIGSERIAL PRIMARY KEY`
- `kind TEXT NOT NULL CHECK (kind IN ('push_subscriber', 'delete_subscriber'))`
- `subscriber_id BIGINT NULL REFERENCES subscribers ON DELETE CASCADE`: set for `push_subscriber`
- `listmonk_subscriber_id INTEGER NULL`: set for `delete_subscriber`, because the subscriber row is already gone
- `status TEXT NOT NULL CHECK (status IN ('pending', 'dead'))`, `attempt_count`, `next_attempt_at`, `last_error`, `created_at`, `updated_at`
- `UNIQUE (subscriber_id) WHERE kind = 'push_subscriber' AND status = 'pending'`: enqueueing twice is a no-op (`ON CONFLICT DO NOTHING`)
- Index on `(next_attempt_at) WHERE status = 'pending'`

A job row is **deleted on success**. No `done` state is needed.

### `sync_runs`
One row per scheduled run, as in bank-system's `sync_fio_runs` / `sync_orca_runs`. The dashboard reads it.

- `id`, `kind CHECK (kind IN ('listmonk_lists', 'listmonk_subscriptions', 'orca_members'))`
- `status CHECK (status IN ('running', 'succeeded', 'failed'))`, `started_at`, `finished_at`, `items_fetched`, `changes_detected`, `error`
- At startup, `FailStaleRuns` marks leftover `running` rows `failed`, as in bank-system's `syncjob/reconcile.go`.

### `pending_changes`
Drift found by the orca reconciliation or the list sync, waiting for an admin decision. orca's direct calls never go through here.

- `id BIGSERIAL PRIMARY KEY`
- `kind TEXT NOT NULL CHECK (kind IN ('member_new', 'member_updated', 'member_removed', 'subscription_add', 'subscription_remove', 'list_removed'))`
- `member_number INTEGER NULL`, `list_id BIGINT NULL`
- `payload JSONB NOT NULL`: the orca snapshot or field diff that gets applied on approval
- `status CHECK (status IN ('pending', 'approved', 'rejected'))`, `detected_at`, `decided_at`, `decided_by` (Keycloak `sub`)
- `UNIQUE (kind, member_number, list_id) WHERE status = 'pending'`: re-detecting the same change updates `payload` instead of adding a row.
- A rejected change with an identical `payload` is not raised again. Otherwise the admin would reject the same thing every day.

## Flows

### 1. orca: new member (`POST /members`, immediate)
orca commits the member first, then makes this call synchronously in the same request, forwarding the admin's token (see Auth). Failures are only logged and never undo the orca action.

```json
{
  "member_number": 834,
  "email": "jan@example.com",
  "first_name": "Jan",
  "last_name": "Novák",
  "city": "Praha 4",
  "lang": "cs",
  "workplace_newsletter_id": null
}
```

- **Idempotent by code check.** If `member_number` already exists, return `200` with the current state and change nothing.
- **Email belongs to an existing non-member subscriber** (for example someone invited to an event before joining): attach `member_number` and the member fields to that row instead of creating a new one. Their existing `manual` subscriptions stay.
- Otherwise insert the subscriber.
- Run `applyMember`, then return `201`.
- An unknown `workplace_newsletter_id` is logged and returned in `unmatched_workplace_newsletter_id`. The other lists are still applied.

### 2. orca: member changed (`PUT /members/{member_number}`, immediate)
The body is the same as `POST /members`, without `member_number`. orca sends it when workplace, city, email, name or lang change.
- Update the subscriber's fields and run `applyMember`.
  - Workplace assigned: the new workplace list is added.
  - Workplace removed: its row becomes `removed`.
  - Workplace moved: both happen.
- If the email is already used by another subscriber, return `409` and log it.
- Unknown member: `404`. orca should `POST` first, and the reconciliation catches it anyway.

### 3. orca: member left (`DELETE /members/{member_number}`, hard delete)
orca calls it when a member leaves, or an admin calls it from the frontend. It runs in one transaction:
1. Read `listmonk_subscriber_id`.
2. Delete the subscriber. Subscriptions and pending push jobs cascade.
3. Enqueue `delete_subscriber` with only the provider ID.
4. Return `204`. A missing member also returns `204`.

`DELETE /subscribers/{id}` does the same for non-members.

What stays afterwards: the `audit_logs` row (it records the path, which contains the member number or ID), and a `delete_subscriber` job until it succeeds.

### 4. Admin assignment (orca UI or our frontend)
- `PUT /lists/{id}/members/{member_number}`:
  - Missing or `removed` row: set it to `subscribed`, `source = 'manual'` when new.
  - `unsubscribed` row: return `409`.
  - Enqueue a push.
- `DELETE /lists/{id}/members/{member_number}`: set the row to `removed` and enqueue a push.

### 5. Provider worker (Listmonk)
A goroutine polls every 15s. It claims due jobs with `FOR UPDATE SKIP LOCKED` and handles each job in its own transaction.

The transaction is short. The Listmonk HTTP call happens **outside** it: claim the job, bump `next_attempt_at` as a lease, commit, call Listmonk, then record the result. Backoff is exponential, from 30s up to 1h. After 10 attempts (about 4h) the job becomes `dead`. These values are constants in code.

**The worker only acts on lists we have a row for.** It never removes a Listmonk membership we don't know about. This protects lists an admin just added in the Listmonk UI before the next import.

`push_subscriber`:
- **No `listmonk_subscriber_id` yet:** `POST /api/subscribers` with:
  - `name`, `email`, `status: "enabled"`
  - `lists`: the `listmonk_id` of every `subscribed` row
  - `preconfirm_subscriptions: true`. Membership is staff-driven, so no double-opt-in email goes out.
  - `attribs`: `{"lang": ..., "member_number": ...}` for members, `{}` for non-members
- **Duplicate email:** look the subscriber up and store its ID. Then, **before adding lists**, import their existing Listmonk memberships as in flow 7. A list they already unsubscribed from becomes `unsubscribed` here and is not re-added.
- **Already linked:**
  - Update name, email and attribs: `PUT /api/subscribers/{id}`.
  - Then send `PUT /api/subscribers/lists` once per status group:
    - `action: "add"` with `status: "confirmed"` for `subscribed` rows
    - `action: "unsubscribe"` for `unsubscribed` rows
    - `action: "remove"` for `removed` rows

`delete_subscriber`: `DELETE /api/subscribers/{id}`. A `404` counts as success.

Dead jobs show up in the frontend. `POST /jobs/{id}/retry` resets them to `pending`.

### 6. Listmonk list sync (every 15m, plus once at startup, non-blocking)
- `GET /api/lists?per_page=all`, upserted by `listmonk_id`. A list that reappears gets `deleted_at` cleared.
- New Listmonk lists are created here automatically, as `private`.
- A list we have but Listmonk no longer returns is **not** soft-deleted automatically. The sync raises a `list_removed` pending change. Approving it sets `deleted_at`.

### 7. Listmonk subscription import (every 15m, after the list sync)
This makes Listmonk UI changes and member opt-outs part of our state.

For each linked list, `GET /api/subscribers?list_id=X&per_page=all`. Skip any subscriber with a pending or dead push job, because our unsent change wins. For the rest:
1. **Find the subscriber.** Match by `listmonk_subscriber_id`, then by email (and link it). Otherwise create a non-member subscriber from Listmonk's name and email.
2. **Membership in Listmonk with no row here** (an admin added them in the Listmonk UI): insert it with `source = 'manual'` and the status mapped from Listmonk.
3. **Listmonk `unsubscribed`, or a `blocklisted` subscriber:** set our row to `unsubscribed`. This is the person's own opt-out and needs no approval.
4. **Listmonk subscribed, our row `removed`:** an admin re-added them in the Listmonk UI, so set it to `subscribed`.
5. **Our row `subscribed`, membership missing in Listmonk:** an admin removed them in the Listmonk UI, or deleted the subscriber there, so set it to `removed`.
6. Always store `listmonk_status`.

### 8. orca reconciliation (daily)
Pull every active member from orca's sync endpoint: `ORCA_API_URL` + `ORCA_SYNC_TOKEN`, the bank-system pattern. It returns the `POST /members` fields. Diff against our members and raise pending changes:
- `member_new`: member is in orca but not here, for example a lost `POST`. Approving it runs flow 1.
- `member_updated`: fields differ, for example a lost `PUT`. Approving it runs flow 2.
- `subscription_add` / `subscription_remove`: the data matches but `desiredLists` differs from our `rule`/`workplace` rows, typically after an admin edits a rule. Approving it changes that one row as `applyMember` would. Rows in `unsubscribed`, `removed`, `manual` or `self` are never proposed.
- `member_removed`: member is here but not in orca, for example a lost `DELETE`. Approving it runs flow 3.

The frontend supports bulk approve, which initial population needs.

### Provider optional
- `LISTMONK_HOST` unset: no worker, no Listmonk syncs, and no jobs are enqueued.
- Everything else still runs: subscribers, rules, subscriptions, orca calls, the reconciliation and the frontend.
- Listmonk access stays inside `internal/listmonk` (client), the worker and the two Listmonk syncs. Swapping providers later means a new client plus `<provider>_id` columns, with no change to lists or subscription state.
- shortcut: with no provider, lists can only be created by the Listmonk sync. Add a list-create API when a second provider or provider-less use actually happens.

## Auth: Keycloak client `newsletter-system`

New roles in [internal/keycloak/keycloak.go](../internal/keycloak/keycloak.go), next to `view-audit-logs`:

| Role | Holder | Grants |
|---|---|---|
| `view-newsletters` | admins | all `GET` routes below |
| `manage-newsletters` | admins | visibility, assignment, rules, change approval, job retry, sync trigger, deletes |
| `update-newsletter-members` | orca admins who accept, edit or remove members | `POST /members`, `PUT /members/{member_number}`, `DELETE /members/{member_number}` |

Every call carries the token of the admin who started the action. There is no service account. Two kinds of caller:
- **orca's backend** forwards the admin's bearer token for member changes. Every member change in orca is an admin action: accept, edit, remove, workplace assign or remove.
  - The call is synchronous, right after orca commits. Access tokens are short-lived (5 minutes by default), so a queued retry could run with an expired token.
  - Use a short timeout (5s) and only log failures. The daily reconciliation catches anything missed.
  - The admin needs `update-newsletter-members`. Grant it through the same Keycloak group or composite role as their orca admin role. Otherwise they get a `403` here while the orca action succeeds, and the member drifts until the next reconciliation.
- **orca's admin UI** calls this API directly from the browser with the logged-in admin's token:
  - The admin needs `view-newsletters` and/or `manage-newsletters` on this client.
  - Add orca's UI origin to `CORS_ALLOWED_ORIGINS`.

Both use the same token, from orca's admin frontend client. That client needs an audience mapper so the token's `aud` includes `newsletter-system`. `Verify` checks the audience. The audit log records the real admin either way.

shortcut: there is no service account. A future automatic member change in orca (for example a scheduled job ending memberships) has no user token. It would need a service account or shared token at that point.

## HTTP API (all under `/api`, stdlib `ServeMux`)

| Route | Role | Notes |
|---|---|---|
| `POST /members` | `update-newsletter-members` | flow 1 |
| `PUT /members/{member_number}` | `update-newsletter-members` | flow 2 |
| `DELETE /members/{member_number}` | `update-newsletter-members` or `manage-newsletters` (`RequireAnyRole`) | flow 3 |
| `GET /members/{member_number}` | `view-newsletters` | member data plus every subscription with status, source, rule and `listmonk_status` |
| `GET /subscribers/{id}` | `view-newsletters` | same, for any subscriber |
| `DELETE /subscribers/{id}` | `manage-newsletters` | non-member erasure, same as flow 3 |
| `GET /lists` | `view-newsletters` | visibility, our counts per status, `listmonk_subscriber_count`; `?include_deleted=true` |
| `PUT /lists/{id}` | `manage-newsletters` | body `{"visibility": ...}` |
| `GET /lists/{id}/subscribers` | `view-newsletters` | `?status=`, `limit`/`offset` as in `/audit-logs` |
| `PUT /lists/{id}/members/{member_number}` | `manage-newsletters` | flow 4; `409` if the member unsubscribed themselves |
| `DELETE /lists/{id}/members/{member_number}` | `manage-newsletters` | flow 4 |
| `GET /rules` | `view-newsletters` | |
| `POST /rules`, `PUT /rules/{id}`, `DELETE /rules/{id}` | `manage-newsletters` | |
| `GET /changes?status=pending` | `view-newsletters` | |
| `POST /changes/{id}/approve`, `POST /changes/{id}/reject` | `manage-newsletters` | |
| `POST /changes/approve` | `manage-newsletters` | bulk, body `{"ids": [...]}` |
| `GET /jobs?status=dead` | `view-newsletters` | |
| `POST /jobs/{id}/retry` | `manage-newsletters` | |
| `GET /sync-runs` | `view-newsletters` | latest runs per kind |
| `POST /sync-runs/{kind}` | `manage-newsletters` | manual trigger, as in bank-system's `TriggerFioSync` |

Every mutating route gets an `auditRoutes` entry and a new `AuditCategory`. Run `make swagger` after adding routes.

## Frontend (monitoring, this repo)

- **Dashboard:** lists with our count next to Listmonk's count, last run per sync kind with its status and error, the number of pending changes and the number of dead jobs.
- **Lists:** visibility toggle, and the subscribers of a list.
- **Member lookup:** search by `member_number`. Shows member data, and every list with its status and source.
- **Pending changes:** filter by kind, approve or reject one, bulk approve.
- **Dead jobs:** last error, attempt count, retry button.
- **Rules:** list and edit.
- **Audit log:** existing.

Show or hide nav items with `useHasRole`. The backend enforces roles regardless.

## Later: member self-management (not in this plan)

- Members log in with their own Keycloak account. This needs a link from the token to `member_number`, either a token claim or orca sending the Keycloak `sub` in `POST`/`PUT /members`. Decide when this phase starts.
- `GET /me/lists` shows `members` and `public` lists, with the member's own status.
- `PUT /me/lists/{id}` subscribes (`source = 'self'`). It is allowed to revive the member's own `unsubscribed` row.
- `DELETE /me/lists/{id}` sets the row to `unsubscribed`.

## Config additions (`.env.example`)

```
# Listmonk provider. Optional: unset LISTMONK_HOST disables the provider entirely.
# HOST must be https:// except for loopback (reuse requireHTTPSExceptLoopback).
LISTMONK_HOST=
LISTMONK_API_USER=
LISTMONK_API_TOKEN=

# orca member pull for reconciliation (same names as bank-system).
# Must be https:// except for loopback.
ORCA_API_URL=
ORCA_SYNC_TOKEN=
```

Set `CORS_ALLOWED_ORIGINS` to orca's admin UI origin. Sync intervals and backoff are constants in code.

## Listmonk details to verify against our instance

The docs at https://listmonk.app/docs/apis/ leave these unclear:
- **Auth header.** v4+ API users use `Authorization: token <api_user>:<token>`. Older versions use basic auth. Check our version.
- **Duplicate email on `POST /api/subscribers`.** It is not documented. orca's current code handles a `409`. Confirm.
- **Lookup by email.** There is no dedicated endpoint, only `GET /api/subscribers?query=subscribers.email='...'`, which is a raw SQL expression. **Escape single quotes** in the email, or it is an injection into Listmonk's query.
- **`PUT /api/subscribers/{id}` and lists.** It may replace list membership when `lists` is sent, or clear it when `lists` is omitted. The worker changes lists only through `PUT /api/subscribers/lists`. Confirm the subscriber PUT leaves memberships alone.
- **`description` on lists.** It is not returned by `GET /api/lists`, so it is not stored.

Status mapping:

| Ours | Listmonk |
|---|---|
| `subscribed` | per-list `confirmed` (we add with `preconfirm_subscriptions` / `status: confirmed`). Imported `unconfirmed` also maps to `subscribed`. |
| `unsubscribed` | per-list `unsubscribed`, or subscriber `blocklisted` |
| `removed` | no membership on that list |

## orca side (separate work in main-system, listed here only as the contract)

- **All calls below** run synchronously, right after orca's transaction commits, and forward the admin's `Authorization` header. Use a 5s timeout and only log failures.
- **Acceptance:** call `POST /api/members`. Delete `orca/src/listmonk/` and the in-transaction subscribe.
  - This is a **hard switch**: it ships together with `newsletter_host` configured. There is no parallel run.
  - No data migration: state here is rebuilt by the initial reconciliation plus the Listmonk import.
- **Member data change** (workplace assigned, removed or moved; city, email, name or lang): `PUT /api/members/{member_number}`.
- **Workplace `newsletter_id` changed** (`PATCH /workplaces/{id}`): send `PUT /api/members/{member_number}` for every member of that workplace.
- **Member leaves:** `DELETE /api/members/{member_number}`, alongside the Keycloak account deletion.
- **New sync endpoint:** `GET /sync/newsletter/members`, protected by a shared token as in `/sync/bank/members`. It returns active members with the `POST /members` fields.
- **Admin UI:** list overview, the subscribers of a list, and add/remove a member on a list. It calls this API directly with the admin's token (see Auth).
- `workplaces.newsletter_id` stays a free int. orca does not validate it. An unknown ID shows up as a warning in this service's logs.
- orca's `email_subscriptions` table can be dropped on orca's own schedule.

## Phasing

1. **Schema and roles.** Run `goose fix`, add migrations 00002 to 00006, regenerate sqlc, add the roles.
2. **Listmonk list sync, `sync_runs`, lists view with visibility.** Verify against a local or real Listmonk.
3. **Members and assignment.** Rules, `desiredLists`/`applyMember`, the `/members` routes, list assignment, `/lists/{id}/subscribers`, the worker, and the jobs page. Verify with a fake Listmonk in tests and by hand against a real one.
4. **Imports and reconciliation.** The Listmonk subscription import, the orca reconciliation and the pending-changes page. Test against a fake orca until orca has its sync endpoint.
5. **Initial population.**
   1. Run the Listmonk import. It creates subscribers, including non-members, keeps their opt-outs and keeps manual list memberships.
   2. Run the orca sync.
   3. Review the `member_new` changes and bulk approve them. Approval attaches each member to the subscriber row the import already created for their email.
6. **orca hard switch.** Deploy orca with `newsletter_host` set and the admin UI pointed at this API. Members accepted between phases 5 and 6 are caught by the next reconciliation.

## Verification

- `make test`. It covers:
  - `desiredLists` table tests: union of matching rules, accent and case handling, workplace match, unknown workplace ID skipped.
  - `applyMember` tests: a workplace move removes the old row and adds the new one; `unsubscribed`, `removed` and `manual` rows are left alone.
  - Handler tests on `dbtest` and `keycloaktest`:
    - role checks
    - `POST /members` idempotency, and attaching a member to an existing non-member row
    - `PUT` email conflict returning `409`
    - admin re-add of an `unsubscribed` row returning `409`
    - delete cascade
  - Worker tests against an `httptest` fake Listmonk:
    - create
    - the duplicate-then-link path that keeps opt-outs
    - unknown Listmonk lists never removed
    - backoff to `dead`
    - a `404` on delete counting as success
  - Import and sync tests:
    - a Listmonk UI add becomes `manual`
    - an opt-out becomes `unsubscribed`
    - subscribers with pending jobs are skipped
    - `list_removed` is raised instead of a silent soft delete
    - a rejected change is not raised again
- `make migrate` on a fresh local DB, `make psql` to inspect the schema.
- Manual end-to-end against a local Listmonk:
  1. Sync the lists and create the rules.
  2. `POST /members`: the member is on All and the city list in Listmonk, with the right attribs.
  3. `PUT` with a workplace, then a different one: the lists change in Listmonk.
  4. Add someone to a list in the Listmonk UI: they appear as `manual` here.
  5. Unsubscribe in Listmonk: the row becomes `unsubscribed`.
  6. `DELETE`: the subscriber is gone in Listmonk.
