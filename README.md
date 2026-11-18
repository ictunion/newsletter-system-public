# Newsletter System

Go API (Postgres, Keycloak auth) with a React + Vite admin frontend. It owns the union's newsletter lists, subscribers and subscription state:
* orca tells it about members (accepted, changed, left), and rules put members on lists.
* Admins manage lists and memberships in its frontend or in orca's admin UI.
* Listmonk only delivers the email. Changes are pushed to it in the background, and opt-outs made there are imported back.

How it works (data model, flows, Listmonk details, the orca contract): [docs/architecture.md](docs/architecture.md).

## Layout
* [cmd/server](cmd/server) — API entrypoint: config, DB pool, Keycloak provider, routes, middleware chain, graceful shutdown.
* [internal/config](internal/config) — env-var config (`.env` loaded for local dev).
* [internal/keycloak](internal/keycloak) — JWT verification against the realm's JWKS, client roles.
* [internal/handler](internal/handler) — HTTP handlers and middleware (auth wrappers, audit log, panic recovery, body size cap).
* [internal/db](internal/db) — sqlc-generated queries; write SQL in `queries.sql`, run `make sqlc`.
* [internal/members](internal/members) — rules engine (`DesiredLists`) and the member create/update/delete logic.
* [internal/changes](internal/changes) — approving and rejecting pending changes.
* [internal/listmonk](internal/listmonk), [internal/orca](internal/orca) — HTTP clients.
* [internal/worker](internal/worker) — pushes subscription state to Listmonk from the `provider_jobs` queue.
* [internal/syncjob](internal/syncjob) — scheduled Listmonk list sync, Listmonk subscription import and orca reconciliation.
* [migrations](migrations) — goose migrations.
* [internal/dbtest](internal/dbtest), [internal/keycloaktest](internal/keycloaktest), [internal/listmonktest](internal/listmonktest) — test helpers: real migrated Postgres, fake Keycloak, fake Listmonk.
* [frontend](frontend) — React + Vite SPA, Keycloak login via OIDC (Authorization Code + PKCE).

## How to build
Run everything from inside `nix develop`. Copy `.env.example` to `.env` and set all mandatory fields first.

API:
* `make db-init` (once)
* `make db-start`
* `make migrate`
* `make sqlc && make start`

Frontend (API must be running):
* `make frontend-install`
* `make frontend-dev`

Tests: `make test` (creates and migrates a separate test database).

Listmonk (optional, local testing only): `make listmonk` runs a throwaway Listmonk on http://localhost:9000 in the foreground, using the dev Postgres (database `listmonk`, config in [listmonk.toml](listmonk.toml)). It has no SMTP and sends no email. The first run creates the UI login `admin` / `listmonk` and the API user `newsletter-system` with token `newsletter-system-dev-token`, matching `.env.example`.

Newsletter System needs a running Keycloak. It can be started by `make up` in the Main-system repo, or point your `.env` at your own Keycloak instance.

## Configuration
All settings are env vars, documented in [.env.example](.env.example). Besides Postgres and Keycloak:

| Variable | Notes |
|---|---|
| `LISTMONK_HOST`, `LISTMONK_API_USER`, `LISTMONK_API_TOKEN` | Optional. If `LISTMONK_HOST` is set, the other two are required. |
| `ORCA_API_URL`, `ORCA_SYNC_TOKEN` | Optional. Setting one requires the other. `ORCA_API_URL` is orca's base URL, without `/sync/...`. |
| `CORS_ALLOWED_ORIGINS` | Set to orca's admin UI origin, because that UI calls this API from the browser. |

`LISTMONK_HOST` and `ORCA_API_URL` must be `https://`, except for loopback addresses.

## Background jobs
They run inside the API process and start with it:

| Job | When | Needs |
|---|---|---|
| Listmonk list sync, then subscription import | at startup and every 12h | Listmonk |
| Provider worker (pushes queued changes to Listmonk) | checks queue every 15s; calls Listmonk only when jobs are due | Listmonk |
| orca reconciliation | at startup and every 24h | orca |

Admins can trigger any sync from the dashboard (`POST /api/sync-runs/{kind}`).

**Without Listmonk:** no worker, no Listmonk syncs, and no jobs are queued. Members, rules, subscriptions, the orca calls, the reconciliation and the frontend still work. Lists can only come from the Listmonk sync, so with no Listmonk there are no new lists.

**Without orca:** no reconciliation. orca's direct calls to `/api/members` still work.

## Roles
Every route needs a client role on the Keycloak client `newsletter-system`. The constants are in [internal/keycloak/keycloak.go](internal/keycloak/keycloak.go):

| Role | For | Grants |
|---|---|---|
| `view-newsletters` | admins | every newsletter `GET` route: lists, subscribers, members, rules, changes, jobs, sync runs |
| `manage-newsletters` | admins | list visibility, adding and removing members on lists, approving changes, job retry, sync triggers, deleting members and subscribers |
| `manage-rules` | admins who maintain the rules | creating, editing and deleting rules (`GET /rules` also accepts `view-newsletters`) |
| `update-newsletter-members` | orca admins | `POST /members`, `PUT /members/{member_number}` and `DELETE /members/{member_number}`, called by orca's backend |
| `view-audit-logs` | admins | `GET /audit-logs` |

The frontend hides pages the user has no role for. The backend enforces the roles regardless.

### Keycloak setup for orca
orca has no service account. orca's backend and orca's admin UI both call this API with the logged-in admin's own token. For that to work:
1. **Grant `update-newsletter-members` together with the orca admin role** (same group, or a composite role). If an admin lacks it, the orca action still succeeds, but this API answers `403`, and the member is out of sync until the daily reconciliation.
2. **Add an audience mapper on orca's admin frontend client**, so its tokens carry `newsletter-system` in `aud`. Tokens without it are rejected.
3. **Add orca's admin UI origin to `CORS_ALLOWED_ORIGINS`.**

## Adding a route
1. Add the SQL to `internal/db/queries.sql` (and a migration if needed, created with `goose -dir migrations -s create <name> sql` for a sequential number), run `make sqlc`.
2. Write the handler in `internal/handler`.
3. Add a role in `internal/keycloak/keycloak.go` if needed.
4. Register it in `cmd/server/routes.go` behind `RequireRole`/`RequireAnyRole`/`RequireAuth`, and add it to the table in `cmd/server/routes_test.go`.
5. Mutating routes: add an entry to `auditRoutes` in `internal/handler/audit.go`.
6. Run `make swagger` to refresh the API docs.

## Swagger
To enable swagger docs, set `ENABLE_SWAGGER_DOCS` to true and open `/api/docs/index.html`. Swagger docs are intended only for development and should be turned off in production.
