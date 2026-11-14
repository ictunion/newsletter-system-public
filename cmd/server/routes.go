package main

import (
	"context"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kubik/newsletter-system/internal/db"
	"github.com/kubik/newsletter-system/internal/handler"
	"github.com/kubik/newsletter-system/internal/keycloak"
	"github.com/kubik/newsletter-system/internal/members"
)

// registerRoutes registers every API route behind its role (see
// internal/keycloak.Role). Mutating routes also need an auditRoutes entry
// in internal/handler/audit.go.
func registerRoutes(api *http.ServeMux, appContext context.Context, provider *keycloak.Provider, pool *pgxpool.Pool, syncs handler.Syncs, memberService members.Service) {
	queries := db.New(pool)
	view := func(next http.HandlerFunc) http.HandlerFunc {
		return handler.RequireRole(provider, keycloak.RoleViewNewsletters, next)
	}
	manage := func(next http.HandlerFunc) http.HandlerFunc {
		return handler.RequireRole(provider, keycloak.RoleManageNewsletters, next)
	}

	updateMembers := func(next http.HandlerFunc) http.HandlerFunc {
		return handler.RequireRole(provider, keycloak.RoleUpdateNewsletterMembers, next)
	}

	api.HandleFunc("GET /audit-logs", handler.RequireRole(provider, keycloak.RoleViewAuditLogs, handler.ListAuditLogs(queries)))

	api.HandleFunc("GET /lists", view(handler.ListLists(queries)))
	api.HandleFunc("PUT /lists/{id}", manage(handler.UpdateList(queries)))
	api.HandleFunc("GET /lists/{id}/subscribers", view(handler.ListListSubscribers(queries)))
	api.HandleFunc("PUT /lists/{id}/members/{member_number}", manage(handler.AddListMember(pool, memberService)))
	api.HandleFunc("DELETE /lists/{id}/members/{member_number}", manage(handler.RemoveListMember(pool, memberService)))

	api.HandleFunc("POST /members", updateMembers(handler.CreateMember(pool, memberService)))
	api.HandleFunc("PUT /members/{member_number}", updateMembers(handler.UpdateMember(pool, memberService)))
	api.HandleFunc("DELETE /members/{member_number}", handler.RequireAnyRole(provider,
		[]keycloak.Role{keycloak.RoleUpdateNewsletterMembers, keycloak.RoleManageNewsletters}, handler.DeleteMember(pool, memberService)))
	api.HandleFunc("GET /members/{member_number}", view(handler.GetMember(queries)))
	api.HandleFunc("GET /subscribers/{id}", view(handler.GetSubscriber(queries)))
	api.HandleFunc("DELETE /subscribers/{id}", manage(handler.DeleteSubscriber(pool, memberService)))

	api.HandleFunc("GET /rules", view(handler.ListRules(queries)))
	api.HandleFunc("POST /rules", manage(handler.CreateRule(queries)))
	api.HandleFunc("PUT /rules/{id}", manage(handler.UpdateRule(queries)))
	api.HandleFunc("DELETE /rules/{id}", manage(handler.DeleteRule(queries)))

	api.HandleFunc("GET /sync-runs", view(handler.ListSyncRuns(queries)))
	api.HandleFunc("POST /sync-runs/{kind}", manage(handler.TriggerSync(appContext, queries, syncs)))
}
