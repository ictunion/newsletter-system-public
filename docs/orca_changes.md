# orca changes for newsletter-system

Changes needed in the main-system repo (orca) so that orca hands newsletter subscriptions over to newsletter-system.

newsletter-system is a separate service with its own API and database. It owns newsletter lists, subscribers and subscription state, and pushes them to Listmonk. orca stops talking to Listmonk. Instead, orca tells newsletter-system about member changes and offers one sync endpoint.

The work ships in two releases:
1. **Release 1: sync endpoint and Keycloak setup.** No behavior change for users. newsletter-system needs the endpoint to load the existing members (initial population).
2. **Release 2: hard switch.** orca starts calling newsletter-system, the Listmonk code in orca is removed, and the admin UI gets the newsletter screens. Ship it only after newsletter-system's initial population is done.

## The member payload

All member data orca sends has the same shape and contains only these fields. Send nothing else.

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

| Field | Type | Value |
|---|---|---|
| `member_number` | integer, required | the member's number |
| `email` | string, required | the member's email, a plain address (no display name) |
| `name` | string, required | full name for the newsletter greeting: first name and last name joined with one space |
| `city` | string or `null` | the member's city as stored in orca; used for city-based lists |
| `lang` | string, required | the member's language code as stored in orca; newsletters go out separately per language |
| `workplace_newsletter_id` | integer or `null` | `workplaces.newsletter_id` of the member's workplace; `null` without a workplace, or when the workplace has no `newsletter_id` |

`workplaces.newsletter_id` stays a free integer that orca does not validate. It is a Listmonk list ID. newsletter-system logs IDs it doesn't know.

## Release 1

### 1. Sync endpoint `GET /sync/newsletter/members`

Add it next to the existing `GET /sync/bank/members`, built the same way:
- **Auth:** the same shared-token check as the bank endpoint, with its own token. Add a config value next to the bank sync token (for example `newsletter_sync_token`). newsletter-system sends it as `Authorization: Bearer <token>`. A missing or wrong token answers `401`.
- **Response:** `200` with a plain JSON array of member payloads (see above), one per active member. Use the same notion of "active member" as the bank endpoint. Do not wrap the array in an object, and add no other fields.
- **Order:** not significant.

newsletter-system calls it once a day to catch lost calls, and once during initial population.

Tell the newsletter-system operator the URL and token. They go into newsletter-system's `ORCA_API_URL` (orca's base URL, without `/sync/...`) and `ORCA_SYNC_TOKEN`.

### 2. Keycloak

newsletter-system has its own Keycloak client, `newsletter-system`, in the same realm. Its client roles:

| Role | Who gets it | What it allows |
|---|---|---|
| `update-newsletter-members` | every orca admin who can accept, edit or remove members | the member calls below |
| `view-newsletters` | admins who use the newsletter screens | reading lists, subscribers and members |
| `manage-newsletters` | admins who manage newsletter subscriptions | adding and removing members on lists, deleting members |

Configure in Keycloak:
1. **Grant `update-newsletter-members` together with the orca admin role.** Use the same group, or make it part of a composite role. If an admin lacks it, orca's action still succeeds, but newsletter-system answers `403`, and the member is out of sync until the daily reconciliation.
2. **Grant `view-newsletters` and `manage-newsletters`** to the admins who should use the newsletter screens (release 2).
3. **Add an audience mapper** on orca's admin frontend client, so access tokens carry `newsletter-system` in `aud`. newsletter-system rejects tokens without it. Every call in release 2 uses this token.

## Release 2 (hard switch)

### 3. Configuration

Add a config value for newsletter-system's base URL, for example `newsletter_host` (like `https://newsletter.example.org`). All API paths below are under `{newsletter_host}/api`.

### 4. Calls on member changes

Every member change in orca is an admin action, and the call forwards that admin's token. There is no service account.

How to call:
- **When:** synchronously, right after orca's own transaction has committed, in the same request. Never inside the transaction.
- **Auth:** forward the incoming `Authorization: Bearer <token>` header unchanged.
- **Timeout:** 5 seconds.
- **Failures:** only log them, with the member number and the response status. Never roll back or fail the orca action because of newsletter-system. The daily reconciliation catches anything missed.
- **Body:** the member payload, as JSON (`Content-Type: application/json`).

| orca event | Call | Body |
|---|---|---|
| A member is accepted | `POST /api/members` | member payload |
| A member's data changes: email, name, city, lang, or workplace assigned, removed or changed | `PUT /api/members/{member_number}` | member payload **without** `member_number` |
| A workplace's `newsletter_id` changes (`PATCH /workplaces/{id}`) | `PUT /api/members/{member_number}` once for every member of that workplace | member payload without `member_number` |
| A member leaves (alongside the Keycloak account deletion) | `DELETE /api/members/{member_number}` | none |

Responses:
- **`POST`:** `201` when created, `200` when the member already existed (nothing changes, so a retry is safe).
- **`PUT`:** `200`.
- **`DELETE`:** `204`, also when the member doesn't exist.
- **Errors to log:**
  - `400`: invalid payload; the body's `error` field says why.
  - `403`: the admin lacks `update-newsletter-members`.
  - `404`: `PUT` for a member newsletter-system doesn't know.
  - `409`: another subscriber already has that email.
- **Field values:** `PUT` replaces all fields, so always send every field with its current value, not only the changed one.

### 5. Remove orca's Listmonk integration

- Delete `orca/src/listmonk/` and every use of it.
- Remove the Listmonk subscribe call from the member-creation transaction.
- Remove orca's Listmonk configuration.
- Stop writing `email_subscriptions`. The table can be dropped on orca's own schedule. newsletter-system does not read it.

### 6. Admin UI: newsletter screens

orca's frontend calls newsletter-system directly from the browser, with the logged-in admin's token (the one with the audience mapper from step 2). The newsletter-system operator must add orca's UI origin to newsletter-system's `CORS_ALLOWED_ORIGINS`.

Screens and the calls they use:

| Screen | Call | Role |
|---|---|---|
| List overview | `GET /api/lists`: every list with visibility, our subscriber counts per status, and Listmonk's count | `view-newsletters` |
| Subscribers of a list | `GET /api/lists/{id}/subscribers?status=subscribed&limit=100&offset=0` | `view-newsletters` |
| A member's newsletters | `GET /api/members/{member_number}`: the member's lists with status and source | `view-newsletters` |
| Add a member to a list | `PUT /api/lists/{id}/members/{member_number}`, returns the member. `409` means the member unsubscribed themselves; show that, it can't be overridden. | `manage-newsletters` |
| Remove a member from a list | `DELETE /api/lists/{id}/members/{member_number}`, returns `204` | `manage-newsletters` |

Hide each action from admins without its role. Response shapes are documented in newsletter-system's Swagger UI (`/api/docs/index.html`, available in its local development setup).

## Done when

- **Release 1:**
  - `GET /sync/newsletter/members` returns every active member, with exactly the payload fields and the bank endpoint's token check.
  - Keycloak grants the roles, and admin tokens carry `newsletter-system` in `aud`.
- **Release 2:**
  - Accepting, editing and removing a member, and changing a workplace's `newsletter_id`, each produce the call above.
  - An unreachable newsletter-system never breaks an orca action; it only logs.
  - No Listmonk code or config is left in orca.
  - The newsletter screens work with an admin token.
