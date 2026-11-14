package main

import (
	"context"
	"net/http"

	"github.com/kubik/newsletter-system/internal/db"
	"github.com/kubik/newsletter-system/internal/handler"
	"github.com/kubik/newsletter-system/internal/keycloak"
)

// registerRoutes registers every API route behind its role (see
// internal/keycloak.Role). Mutating routes also need an auditRoutes entry
// in internal/handler/audit.go.
func registerRoutes(api *http.ServeMux, appContext context.Context, provider *keycloak.Provider, queries *db.Queries, syncs handler.Syncs) {
	view := func(next http.HandlerFunc) http.HandlerFunc {
		return handler.RequireRole(provider, keycloak.RoleViewNewsletters, next)
	}
	manage := func(next http.HandlerFunc) http.HandlerFunc {
		return handler.RequireRole(provider, keycloak.RoleManageNewsletters, next)
	}

	api.HandleFunc("GET /audit-logs", handler.RequireRole(provider, keycloak.RoleViewAuditLogs, handler.ListAuditLogs(queries)))

	api.HandleFunc("GET /lists", view(handler.ListLists(queries)))
	api.HandleFunc("PUT /lists/{id}", manage(handler.UpdateList(queries)))

	api.HandleFunc("GET /rules", view(handler.ListRules(queries)))
	api.HandleFunc("POST /rules", manage(handler.CreateRule(queries)))
	api.HandleFunc("PUT /rules/{id}", manage(handler.UpdateRule(queries)))
	api.HandleFunc("DELETE /rules/{id}", manage(handler.DeleteRule(queries)))

	api.HandleFunc("GET /sync-runs", view(handler.ListSyncRuns(queries)))
	api.HandleFunc("POST /sync-runs/{kind}", manage(handler.TriggerSync(appContext, queries, syncs)))
}
