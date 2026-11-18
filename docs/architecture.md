# How newsletter-system works

newsletter-system decides who gets which newsletter. It stores the newsletter lists, the subscribers and every subscription in its own Postgres database. orca tells it about members. Listmonk only delivers the email, and Listmonk is optional.

This document explains the model, the flows and the rules behind them. For setup, env vars and roles, see the [README](../README.md). Request and response shapes are in the Swagger UI.

## Data model

All internal IDs are UUIDs. Provider IDs (`listmonk_id`, `listmonk_subscriber_id`) and orca's `member_number` are integers, because those systems issue them.

### `newsletter_lists`
- The list entity. Lists come from the Listmonk list sync. There is no API to create a list.
- `visibility` is `private` (admins only, the default), `members` (for a future member self-service frontend) or `public` (stored, but nothing uses it yet). A list that is public in Listmonk is set to `public` on every sync. Otherwise only admins set it.
- `listmonk_subscriber_count` is Listmonk's own count. The dashboard shows it next to ours, so a mismatch is easy to see.
- `deleted_at` soft-deletes a list. Lists are never soft-deleted automatically (see the list sync below).

### `subscribers`
- One row for each person who gets a newsletter. A member is a subscriber with a `member_number`. A non-member has no number, for example someone invited to an event through the Listmonk UI.
- `email` is unique, case-insensitive.
- `city` and `lang` exist only for members, and a member must have `lang`. `lang` goes to Listmonk as `attribs.lang`, because newsletters go out separately per language.
- The member's workplace is not stored here. A `workplace` subscription row records it (see the rules below).

### `subscriptions`
One row per subscriber and list.

- `status`:
  - `subscribed`
  - `unsubscribed`: the person opted out themselves.
  - `removed`: an admin removed them, or their rule or workplace no longer applies.
- `source` records how the row was first created:
  - `rule`: a subscription rule matched.
  - `workplace`: it is the member's workplace list.
  - `manual`: an admin added it, here or in the Listmonk UI.
  - `self`: reserved for the future member self-service frontend.
- `listmonk_status` stores the per-list status last seen in Listmonk.

Rows are never deleted, only given a new status. They disappear only when their subscriber is deleted. The provider worker relies on this to know what to remove in Listmonk.

Who may change a row:
- Rules and workplaces only create rows, or change their own `subscribed` rows to `removed`. They never revive an `unsubscribed` or `removed` row, and they never touch `manual` or `self` rows.
- An admin may re-add a `removed` row. Re-adding an `unsubscribed` row returns `409`, because that would override the person's own opt-out. Only the person can undo it.

### `subscription_rules`
- `all`: every member goes to the rule's list.
- `city`: members whose city contains `city_contains` go to the rule's list. The match ignores case and accents, so `praha` matches "Praha 4" and "PRAHA".
- Every matching enabled rule applies, not only the first match.
- Rules apply only to members, never to non-members.
- Changing a rule does not re-evaluate existing members right away. The next orca reconciliation proposes the resulting changes.

### Workplace lists
- orca sends `workplace_newsletter_id`, which is a Listmonk list ID. The member goes to the active list with that `listmonk_id`, with `source = workplace`.
- If no active list matches, a warning is logged and the other lists are still applied. `POST /members` also returns the ID in `unmatched_workplace_newsletter_id`. Fix the ID in orca.
- Switching email providers would mean remapping these IDs.

### Applying member data
`members.DesiredLists` is a pure function. From a member, the rules and the lists, it computes the lists the member should be on: all matching rule lists plus the workplace list.

`applyMember` compares that with the member's rows:
- A desired list with no row is inserted as `subscribed`.
- A `rule` or `workplace` row in `subscribed` that is no longer desired becomes `removed`.
- Everything else is left alone.
- If anything changed, a `push_subscriber` job is queued.

`POST /members`, `PUT /members/{member_number}` and approving a reconciliation change all go through `applyMember`.

### `provider_jobs`
The retry queue for pushing to Listmonk.
- A job holds no personal data and no list IDs. It means "make Listmonk match our DB for this subscriber". The worker reads the current state when it runs.
- Each subscriber has at most one pending `push_subscriber` job. Queueing again makes the existing job due now. It also replaces a `dead` job.
- `delete_subscriber` keeps only the `listmonk_subscriber_id`, because the subscriber row is already gone.
- A job is deleted when it succeeds. After 10 failed attempts it becomes `dead`, shows up in the frontend and can be retried.

### `sync_runs` and `pending_changes`
- `sync_runs` has one row per sync run, read by the dashboard. At startup, runs left in `running` are marked `failed`.
- `pending_changes` holds drift found by the orca reconciliation or the list sync, waiting for an admin to approve or reject it. orca's direct calls never go through here.
  - Detecting the same change again updates its payload instead of adding a new row.
  - A rejected change with an identical payload is not raised again.
  - A pending member or subscription change that a later run no longer detects is deleted, because the drift is gone.

## Flows

### orca calls (immediate)
orca calls this API right after its own transaction commits, forwarding the acting admin's token. Failures are only logged on orca's side. The daily reconciliation catches anything missed.

- **`POST /members`** (a member is accepted):
  - If the `member_number` already exists, it returns `200` and changes nothing, so a retry is safe.
  - If the email belongs to an existing non-member, the member is attached to that row and its `manual` subscriptions stay.
  - Otherwise a new subscriber is created.
  - Then `applyMember` runs and the response is `201`.
- **`PUT /members/{member_number}`** (member data changed: email, name, city, lang or workplace):
  - Updates every field and runs `applyMember`. Moving to another workplace removes the old workplace row and adds the new one.
  - Unknown member: `404`.
  - Email already used by another subscriber: `409`.
- **`DELETE /members/{member_number}`** (a member leaves): hard-deletes the subscriber. Their subscriptions and pending pushes go with it. A `delete_subscriber` job is queued. The response is always `204`, also for an unknown member. `DELETE /subscribers/{id}` does the same for non-members.

### Admin assignment
- `PUT /lists/{id}/members/{member_number}`: creates the row as `manual`, or revives a `removed` row. It returns `409` if the row is `unsubscribed`.
- `DELETE /lists/{id}/members/{member_number}`: sets the row to `removed`.
- Both queue a push.

### Provider worker (every 15s, only with Listmonk configured)
- **Claiming jobs.** It claims due jobs with `FOR UPDATE SKIP LOCKED` and a lease, commits, and only then calls Listmonk. No DB transaction stays open during an HTTP call.
- **Retries.** Backoff is exponential, from 30s up to 1h. After 10 attempts the job becomes `dead`.
- **A change made while a job runs is never lost.** The finished job is deleted only if it was not queued again in the meantime. Otherwise it runs again.

`push_subscriber`:
- **Not linked to Listmonk yet:** creates the Listmonk subscriber with every `subscribed` list, preconfirmed so no opt-in email goes out. Members get `attribs` `{lang, member_number}`.
  - If Listmonk says the email already exists, the worker links that subscriber and imports their Listmonk memberships first. A list they unsubscribed from in Listmonk becomes `unsubscribed` here and is not re-added.
- **Already linked:** first imports the subscriber's Listmonk opt-outs, so a push never resubscribes someone who just unsubscribed. Then it updates name, email and attribs, and sends one membership call for each status: `add` for `subscribed`, `unsubscribe` for `unsubscribed`, `remove` for `removed`.
- It only changes lists we have a row for. It never removes a Listmonk membership we don't know about.
- If the subscriber was deleted in the Listmonk UI, the link is cleared and the next attempt creates them again.

`delete_subscriber` deletes the Listmonk subscriber. A missing subscriber counts as success.

### Listmonk list sync (every 12h and at startup)
- Fetches Listmonk's active lists and upserts them by `listmonk_id`. Name, description and Listmonk's count are copied every time. A list that is public in Listmonk becomes `public` here. A private Listmonk list keeps the visibility it has here.
- New Listmonk lists are created here as `private`, or `public` if they are public in Listmonk.
- A list that reappears in Listmonk gets `deleted_at` cleared.
- A list Listmonk no longer returns, either deleted or archived, is not soft-deleted automatically. The sync raises a `list_removed` pending change, and approving it sets `deleted_at`.

### Listmonk subscription import (every 12h, right after the list sync)
It brings changes made in Listmonk into our state, mainly people's own opt-outs and memberships admins added in the Listmonk UI.

For each linked list it reads every Listmonk subscriber. It skips anyone with a pending or dead job, because our unsent change wins. For each remaining subscriber, in its own transaction:
1. **Find the subscriber.** Match by Listmonk ID, then by email (and link it). Otherwise create a non-member subscriber.
2. **Membership in Listmonk with no row here:** insert it as `manual`.
3. **Unsubscribed in Listmonk, or blocklisted:** set our row to `unsubscribed`. No approval is needed.
4. **Subscribed in Listmonk, our row `removed`:** an admin re-added them in the Listmonk UI, so set it to `subscribed`.
5. **Our row `subscribed`, membership missing in Listmonk:** set it to `removed`.
6. Always store `listmonk_status`.

### orca reconciliation (daily and at startup, only with orca configured)
It pulls every active member from orca's `GET /sync/newsletter/members` and raises pending changes:

| Kind | Meaning | Approving it |
|---|---|---|
| `member_new` | in orca, not here (for example a lost `POST`) | runs the `POST /members` flow |
| `member_updated` | email, name, city or lang differ (for example a lost `PUT`) | runs the `PUT /members` flow |
| `member_removed` | here, not in orca (for example a lost `DELETE`) | runs the `DELETE` flow |
| `subscription_add` / `subscription_remove` | member data matches, but the rule or workplace lists differ (after a rule edit or a lost workplace change) | changes that one row as `applyMember` would |

Rows in `unsubscribed`, `removed`, `manual` or `self` are never proposed. orca members that fail validation are logged and skipped, and they never count as removed.

## Listmonk behavior we rely on

Checked against Listmonk v6.2. `internal/listmonk` depends on these:
- **Auth.** `Authorization: token <api_user>:<token>`.
- **Duplicate email.** Creating a subscriber with an existing email returns `409`. The check is case-insensitive. The client returns `ErrDuplicateEmail`.
- **Lookup by email.** Uses a SQL `query` parameter, so the client doubles single quotes. Listmonk stores emails lowercased.
- **`PUT /api/subscribers/{id}` replaces the whole membership set and sets the status.** `UpdateSubscriber` therefore sends back the current lists and status unchanged. Memberships change only through `PUT /api/subscribers/lists`.
- **`add` with `status: confirmed` also resubscribes an `unsubscribed` membership.** It is therefore never sent for our `unsubscribed` rows.
- **A missing subscriber.** `GET /api/subscribers/{id}` returns `400`, not `404`, so the client looks subscribers up by query instead. `DELETE` on a missing ID returns `200`.
- **`GET /api/subscribers?list_id=X`** returns subscribers in every status, blocklisted ones included.

Status mapping:

| Ours | Listmonk |
|---|---|
| `subscribed` | per-list `confirmed` (`unconfirmed` is imported as `subscribed` too) |
| `unsubscribed` | per-list `unsubscribed`, or the subscriber is blocklisted |
| `removed` | no membership on that list |

## orca integration contract

orca sends this member payload, and only these fields:

```json
{
  "member_number": 834,
  "email": "jan@example.com",
  "name": "Jan Novák",
  "city": "Praha 4",
  "lang": "cs",
  "workplace_newsletter_id": 13
}
```

- `name` is the full name used in the newsletter greeting.
- `city` and `workplace_newsletter_id` may be `null`.
- `workplace_newsletter_id` is `workplaces.newsletter_id`, a Listmonk list ID that orca does not validate.

| orca event | Call |
|---|---|
| A member is accepted | `POST /api/members` with the payload |
| A member's data changes, including workplace assigned, removed or changed | `PUT /api/members/{member_number}` with the payload without `member_number`, every field with its current value |
| A workplace's `newsletter_id` changes | one `PUT` for each member of that workplace |
| A member leaves | `DELETE /api/members/{member_number}` |

How orca makes these calls:
- Synchronously, right after its own transaction commits.
- Forwarding the admin's `Authorization` header.
- With a 5s timeout, only logging failures.

orca also serves `GET /sync/newsletter/members`, protected by `Authorization: Bearer <ORCA_SYNC_TOKEN>`. It returns a plain JSON array of payloads, one per active member.

orca's admin UI calls the list endpoints (`GET /api/lists`, `GET /api/lists/{id}/subscribers`, `GET /api/members/{member_number}`, and `PUT`/`DELETE /api/lists/{id}/members/{member_number}`) directly from the browser with the admin's token.

## Known limits

- **No service account.** Every call carries the acting admin's token. A future automatic member change in orca, such as a scheduled job, would need a service account or a shared token.
- **No list-create API.** Lists come only from the Listmonk sync. Add an API if lists are ever needed without a provider.
- **Member self-service is not built yet.** `visibility = members` and `source = self` are reserved for it. It needs a link from a member's Keycloak token to their `member_number`.
