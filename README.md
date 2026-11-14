# Newsletter System

Go API (Postgres, Keycloak auth) with a React + Vite admin frontend.

## Layout
* [cmd/server](cmd/server) — API entrypoint: config, DB pool, Keycloak provider, routes, middleware chain, graceful shutdown.
* [internal/config](internal/config) — env-var config (`.env` loaded for local dev).
* [internal/keycloak](internal/keycloak) — JWT verification against the realm's JWKS, client roles.
* [internal/handler](internal/handler) — HTTP handlers and middleware (auth wrappers, audit log, panic recovery, body size cap).
* [internal/db](internal/db) — sqlc-generated queries; write SQL in `queries.sql`, run `make sqlc`.
* [migrations](migrations) — goose migrations.
* [internal/dbtest](internal/dbtest), [internal/keycloaktest](internal/keycloaktest) — test helpers: real migrated Postgres, fake Keycloak.
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

## Roles
Every route needs a role from Keycloak (client `newsletter-system`). See [internal/keycloak/keycloak.go](internal/keycloak/keycloak.go) for the list of roles.

## Adding a route
1. Add the SQL to `internal/db/queries.sql` (and a migration if needed), run `make sqlc`.
2. Write the handler in `internal/handler`.
3. Add a role in `internal/keycloak/keycloak.go` if needed.
4. Register it in `cmd/server/main.go` behind `RequireRole`/`RequireAnyRole`/`RequireAuth`.
5. Mutating routes: add an entry to `auditRoutes` in `internal/handler/audit.go`.
6. Run `make swagger` to refresh the API docs.

## Swagger
To enable swagger docs, set `ENABLE_SWAGGER_DOCS` to true and open `/api/docs/index.html`. Swagger docs are intended only for development and should be turned off in production.
