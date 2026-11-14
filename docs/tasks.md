# Newsletter System: tasks

Tasks are implemented one at a time, in order. Each one ends green: `make test` passes and `gofmt` is clean. Section names refer to [newsletter_service_plan.md](newsletter_service_plan.md).

For every task that adds routes:
- Register each route in `cmd/server/main.go` behind the right role.
- Add every mutating route to `auditRoutes`.
- Run `make swagger`.

---

## 1. Goose sequential migrations
- [ ] Run `goose fix` to convert `20261114000001_audit_logs.sql` to `00001_audit_logs.sql`.
- [ ] Note in the README that new migrations use `goose -s create <name> sql`.

**Done when:** `make migrate` and `make test` run on a fresh DB.

## 2. Roles and config
- [ ] Add `view-newsletters`, `manage-newsletters` and `update-newsletter-members` to `internal/keycloak/keycloak.go`.
- [ ] Add the config fields:
  - `LISTMONK_HOST`, `LISTMONK_API_USER`, `LISTMONK_API_TOKEN`. All are optional, but when `LISTMONK_HOST` is set, the user and token are required.
  - `ORCA_API_URL`, `ORCA_SYNC_TOKEN`. Both are optional, and setting one requires the other.
  - Both URLs go through `requireHTTPSExceptLoopback`.
- [ ] Update `.env.example`.

**Done when:** `config_test.go` covers the new required and optional combinations and the HTTPS check.

## 3. Schema
- [ ] Add migrations `00002_lists` through `00006_sync`, exactly as described in *Data model*: tables, CHECKs, partial unique indexes and FKs.
- [ ] Add a `+goose Down` for each.
- [ ] No queries yet. They come with the tasks that use them.

**Done when:** `make migrate` applies, the Down migrations roll back cleanly, and `make psql` shows the schema.

## 4. Listmonk client (`internal/listmonk`)
- [ ] Add these calls:
  - `ListLists`
  - `ListSubscribersByList`
  - `CreateSubscriber`
  - `GetSubscriber`
  - `FindSubscriberByEmail`, which must escape single quotes
  - `UpdateSubscriber`
  - `ManageLists` (`add` / `unsubscribe` / `remove`)
  - `DeleteSubscriber`
- [ ] A duplicate email returns the sentinel error `ErrDuplicateEmail`.
- [ ] `DeleteSubscriber` treats a `404` as success.
- [ ] Tests against an `httptest` fake.
- [ ] Manually check each item in *Listmonk details to verify against our instance* against our instance. Record the answers in the plan, and adjust the client if needed.

**Done when:** the client tests pass and every open Listmonk item has an answer.

## 5. Sync runs and Listmonk list sync (flow 6)
- [ ] `internal/syncjob`:
  - sync-run bookkeeping
  - `FailStaleRuns` at startup
  - `RunListmonkListSync`: upsert by `listmonk_id`, clear `deleted_at` when a list reappears, create new lists as `private`, raise a `list_removed` pending change for missing lists
- [ ] Scheduler goroutine in `main.go`: every 15m plus once at startup, non-blocking. It runs only when Listmonk is configured.

**Done when:** tests against a fake Listmonk cover upsert, reappear and `list_removed`, and a real sync fills `newsletter_lists`.

## 6. Lists and sync-runs API
- [ ] `GET /lists`: visibility, our count per status, `listmonk_subscriber_count`, `?include_deleted=true`.
- [ ] `PUT /lists/{id}`: change visibility.
- [ ] `GET /sync-runs`.
- [ ] `POST /sync-runs/{kind}`. Only `listmonk_lists` is supported for now. Later tasks add the other kinds.

**Done when:** handler tests cover the role checks and the response shapes.

## 7. Rules engine and rules API
- [ ] Pure function `desiredLists(member, rules, lists)` with table tests:
  - union of matching rules
  - accent and case insensitivity
  - workplace match by `listmonk_id`
  - unknown workplace ID skipped and reported
  - deleted lists ignored
- [ ] `GET /rules`, `POST /rules`, `PUT /rules/{id}`, `DELETE /rules/{id}`.

**Done when:** the table tests and handler tests pass.

## 8. Members API (flows 1–3)
- [ ] `applyMember(tx, member)` with tests:
  - a workplace move removes the old row and adds the new one
  - `unsubscribed`, `removed` and `manual` rows are left alone
  - a push is enqueued only when something changed
- [ ] `POST /members`:
  - an existing `member_number` returns `200` and changes nothing
  - an email held by a non-member attaches the member to that row
  - the response carries `unmatched_workplace_newsletter_id`
- [ ] `PUT /members/{member_number}`: `404` for an unknown member, `409` for an email conflict.
- [ ] `DELETE /members/{member_number}` and `DELETE /subscribers/{id}`: hard delete plus a `delete_subscriber` job, and `204` even when the record is missing.
- [ ] `GET /members/{member_number}` and `GET /subscribers/{id}`.
- [ ] Jobs are enqueued only when Listmonk is configured.

**Done when:** handler tests pass on `dbtest` + `keycloaktest`, including role checks for `update-newsletter-members` vs `manage-newsletters`.

## 9. List assignment API (flow 4)
- [ ] `PUT /lists/{id}/members/{member_number}`: revives a `removed` row, returns `409` on an `unsubscribed` row, and creates new rows as `manual`.
- [ ] `DELETE /lists/{id}/members/{member_number}`: sets the row to `removed`.
- [ ] `GET /lists/{id}/subscribers` with `?status=` and `limit`/`offset`.

**Done when:** handler tests pass.

## 10. Provider worker and jobs API (flow 5)
- [ ] Worker goroutine:
  - claims jobs with `FOR UPDATE SKIP LOCKED` and a lease, and calls Listmonk outside the transaction
  - `push_subscriber`: create, or duplicate email → link → import existing memberships → push; already linked → update, then one `ManageLists` call per status
  - `delete_subscriber`
  - exponential backoff, `dead` after 10 attempts, row deleted on success
- [ ] `GET /jobs?status=dead`, `POST /jobs/{id}/retry`.

**Done when:** tests against a fake Listmonk cover create, link that keeps opt-outs, unknown Listmonk lists never removed, backoff to `dead`, a `404` on delete counting as success, and retry.

## 11. Listmonk subscription import (flow 7)
- [ ] `RunListmonkSubscriptionImport`, scheduled after the list sync. It follows flow 7's steps 1–6 and skips subscribers that have a pending or dead job.
- [ ] Add `listmonk_subscriptions` to `POST /sync-runs/{kind}`.

**Done when:** tests cover a Listmonk UI add becoming `manual`, a non-member being created, an opt-out becoming `unsubscribed`, re-add and removal, the skip on pending jobs, and blocklisted subscribers.

## 12. orca client and reconciliation (flow 8)
- [ ] `internal/orca` client for `GET /sync/newsletter/members`, using the `ORCA_SYNC_TOKEN` bearer token.
- [ ] `RunOrcaReconciliation`: raise `member_new`, `member_updated`, `member_removed`, `subscription_add` and `subscription_remove`. Re-detection updates the payload. A rejected change with an identical payload is not raised again.
- [ ] Daily schedule, only when orca is configured. Add `orca_members` to `POST /sync-runs/{kind}`.

**Done when:** tests against a fake orca cover every change kind and the rejected-payload suppression.

## 13. Pending changes API
- [ ] `GET /changes?status=`.
- [ ] `POST /changes/{id}/approve`, `POST /changes/{id}/reject`, and the bulk `POST /changes/approve`.
- [ ] Approving applies the change through the same code as flows 1–3 and `applyMember`. Approving `list_removed` sets `deleted_at`. `decided_by` is set from the token.

**Done when:** tests cover approving each kind, reject, and bulk approve.

## 14. Frontend: dashboard and lists
- [ ] Dashboard: lists with our count next to Listmonk's, the last run per sync kind, the pending-changes count and the dead-jobs count.
- [ ] Lists page: visibility toggle, and a subscribers view per list.

**Done when:** it works against a local API with real data. Nav items are hidden by `useHasRole`.

## 15. Frontend: member lookup
- [ ] Search by `member_number`. Show member data and every list with its status, source and rule.
- [ ] Add and remove a member on a list, and delete a member (with confirmation).

**Done when:** it works against a local API.

## 16. Frontend: changes, jobs and rules
- [ ] Pending changes: kind filter, approve or reject one, bulk approve.
- [ ] Dead jobs: last error, attempt count, retry.
- [ ] Rules: list, create, edit, delete.

**Done when:** it works against a local API.

## 17. README and docs
- [ ] README: roles, the new env vars, the background syncs and the worker, provider-optional behavior, and the Keycloak setup (`update-newsletter-members` granted with the orca admin group, the audience mapper on orca's admin frontend client, CORS for orca's UI).

**Done when:** a new developer can run everything from the README alone.

## 18. Initial population and end-to-end check (manual, with a real Listmonk)
- [ ] Run the end-to-end scenario from *Verification*.
- [ ] Run the population in order:
  1. Listmonk import
  2. orca sync
  3. review and bulk approve `member_new`
- [ ] Check that list counts match Listmonk on the dashboard.

**Done when:** counts match, opt-outs are preserved, and nobody is resubscribed.

---

## External dependencies (main-system, not tasks here)
- orca: `GET /sync/newsletter/members` endpoint. Needed for real (not faked) testing of task 12, and for task 18.
- orca: synchronous `POST`/`PUT`/`DELETE /members` calls forwarding the admin's token (including one `PUT` per member when a workplace's `newsletter_id` changes), removal of `orca/src/listmonk/`, and the admin UI. This is the hard switch, after task 18.
- Keycloak: roles on the `newsletter-system` client, `update-newsletter-members` granted via the orca admin group or a composite role, and an audience mapper on orca's admin frontend client.
