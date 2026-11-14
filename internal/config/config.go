// Package config loads server configuration from environment variables.
//
// Env vars are read directly via os.Getenv — no dependency on systemd or any other
// OS-specific mechanism to set them, so this works the same whether the process is
// started by systemd on NixOS, a Windows service, Docker, or a plain shell.
//
// If a .env file exists in the working directory, it's loaded first (without
// overriding any variable already set in the real environment) purely as a local-dev
// convenience. Production deployments are expected to set real env vars and can omit
// the file entirely.
package config

import (
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/joho/godotenv"
)

type Config struct {
	// DatabaseURL is a standard Postgres connection string, e.g.
	// postgres://user:password@host:5433/dbname?sslmode=disable
	// The Postgres server does not need to run on the same machine — pass any
	// reachable host here.
	DatabaseURL string

	// Addr is the address the HTTP server listens on, e.g. ":8080".
	Addr string

	// Debug enables verbose logging for local development. Set via the DEBUG
	// env var (any of "1", "true", "yes", case-insensitive).
	Debug bool

	// EnableSwaggerDocs mounts the generated Swagger UI (see
	// internal/swaggerdocs, produced by `make swagger` from the @-annotations
	// on cmd/server/main.go and internal/handler/*.go) at GET /api/docs/*.
	// Off by default — dev-only, never set in production: every real route
	// here is role-gated, and the docs UI itself carries no auth of its own,
	// so it should never be reachable outside local development. Set via the
	// ENABLE_SWAGGER_DOCS env var (any of "1", "true", "yes", case-insensitive).
	EnableSwaggerDocs bool

	// KeycloakHost/Realm/ClientID configure verification of user-facing bearer
	// tokens (see internal/keycloak), same Keycloak instance/realm as the rest
	// of ictunion's stack. ClientID is newsletter-system's own client ID in
	// Keycloak — the audience every verified token must carry, and the key
	// under which required roles are looked up (resource_access[ClientID]).
	// KeycloakHost must be https:// unless it's localhost/127.0.0.1/::1 (see
	// requireHTTPSExceptLoopback) — plain HTTP against a real Keycloak host
	// exposes the JWKS fetch and forwarded bearer tokens to interception.
	KeycloakHost     string
	KeycloakRealm    string
	KeycloakClientID string

	// CorsAllowedOrigins is an exact-match allow-list of browser origins
	// permitted to call this API cross-origin.
	// Optional, comma-separated, empty by default: no origin
	// needs to be here unless another site calls in from a browser.
	CorsAllowedOrigins []string
}

func envBool(name string) bool {
	return map[string]bool{"1": true, "true": true, "yes": true}[strings.ToLower(os.Getenv(name))]
}

// requireHTTPSExceptLoopback rejects rawURL unless it's https://, or plain
// http:// against loopback (localhost/127.0.0.1/::1) — the one case with no
// TLS cert available, since Keycloak normally runs on the same dev machine
// (see .env.example). A real Keycloak host reachable over plain HTTP would
// let a network-position attacker substitute the JWKS response (forging
// tokens this service then accepts as valid — see internal/keycloak) or
// read forwarded bearer tokens off the wire (internal/keycloak.Provider's
// Account API calls forward the caller's own token).
func requireHTTPSExceptLoopback(rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	if parsed.Scheme == "https" {
		return nil
	}
	switch parsed.Hostname() {
	case "localhost", "127.0.0.1", "::1":
		return nil
	}
	return fmt.Errorf("must be https:// (got %q) — plain http is only allowed for localhost/127.0.0.1", rawURL)
}

func Load() (Config, error) {
	_ = godotenv.Load()

	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		return Config{}, fmt.Errorf("DATABASE_URL environment variable is required")
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	keycloakHost := os.Getenv("KEYCLOAK_HOST")
	if keycloakHost == "" {
		return Config{}, fmt.Errorf("KEYCLOAK_HOST environment variable is required")
	}
	if err := requireHTTPSExceptLoopback(keycloakHost); err != nil {
		return Config{}, fmt.Errorf("KEYCLOAK_HOST: %w", err)
	}

	keycloakRealm := os.Getenv("KEYCLOAK_REALM")
	if keycloakRealm == "" {
		return Config{}, fmt.Errorf("KEYCLOAK_REALM environment variable is required")
	}

	keycloakClientID := os.Getenv("KEYCLOAK_CLIENT_ID")
	if keycloakClientID == "" {
		return Config{}, fmt.Errorf("KEYCLOAK_CLIENT_ID environment variable is required")
	}

	var corsAllowedOrigins []string
	for _, origin := range strings.Split(os.Getenv("CORS_ALLOWED_ORIGINS"), ",") {
		if origin = strings.TrimSpace(origin); origin != "" {
			corsAllowedOrigins = append(corsAllowedOrigins, origin)
		}
	}

	return Config{
		DatabaseURL:        databaseURL,
		Addr:               ":" + port,
		Debug:              envBool("DEBUG"),
		EnableSwaggerDocs:  envBool("ENABLE_SWAGGER_DOCS"),
		KeycloakHost:       keycloakHost,
		KeycloakRealm:      keycloakRealm,
		KeycloakClientID:   keycloakClientID,
		CorsAllowedOrigins: corsAllowedOrigins,
	}, nil
}
