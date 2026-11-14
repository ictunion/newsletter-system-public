package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kubik/newsletter-system/internal/dbtest"
	"github.com/kubik/newsletter-system/internal/handler"
	"github.com/kubik/newsletter-system/internal/keycloak"
	"github.com/kubik/newsletter-system/internal/keycloaktest"
)

// TestRouteRoles checks every route against its required role: a token
// with every other role gets 403, a token with the required role gets past
// auth (any status but 401/403).
func TestRouteRoles(t *testing.T) {
	provider := keycloaktest.New(t)
	api := http.NewServeMux()
	registerRoutes(api, context.Background(), provider.Provider, dbtest.Tx(t), handler.Syncs{})

	allRoles := []keycloak.Role{keycloak.RoleViewAuditLogs, keycloak.RoleViewNewsletters, keycloak.RoleManageNewsletters, keycloak.RoleUpdateNewsletterMembers}
	token := func(roles ...keycloak.Role) string {
		names := make([]string, len(roles))
		for i, role := range roles {
			names[i] = string(role)
		}
		return provider.SignToken(t, keycloak.Claims{ResourceAccess: map[string]keycloak.RolesClaim{keycloaktest.ClientID: {Roles: names}}})
	}

	const someID = "00000000-0000-0000-0000-000000000000"
	routes := []struct {
		method, path string
		role         keycloak.Role
	}{
		{http.MethodGet, "/audit-logs", keycloak.RoleViewAuditLogs},
		{http.MethodGet, "/lists", keycloak.RoleViewNewsletters},
		{http.MethodPut, "/lists/" + someID, keycloak.RoleManageNewsletters},
		{http.MethodGet, "/rules", keycloak.RoleViewNewsletters},
		{http.MethodPost, "/rules", keycloak.RoleManageNewsletters},
		{http.MethodPut, "/rules/" + someID, keycloak.RoleManageNewsletters},
		{http.MethodDelete, "/rules/" + someID, keycloak.RoleManageNewsletters},
		{http.MethodGet, "/sync-runs", keycloak.RoleViewNewsletters},
		{http.MethodPost, "/sync-runs/listmonk_lists", keycloak.RoleManageNewsletters},
	}
	for _, route := range routes {
		t.Run(route.method+" "+route.path, func(t *testing.T) {
			var others []keycloak.Role
			for _, role := range allRoles {
				if role != route.role {
					others = append(others, role)
				}
			}
			for _, tt := range []struct {
				name    string
				token   string
				allowed bool
			}{
				{"other roles", token(others...), false},
				{"required role", token(route.role), true},
			} {
				recorder := httptest.NewRecorder()
				request := httptest.NewRequest(route.method, route.path, strings.NewReader(`{}`))
				request.Header.Set("Authorization", "Bearer "+tt.token)
				api.ServeHTTP(recorder, request)
				denied := recorder.Code == http.StatusUnauthorized || recorder.Code == http.StatusForbidden
				if denied == tt.allowed {
					t.Errorf("%s: status = %d, body = %s", tt.name, recorder.Code, recorder.Body)
				}
			}
		})
	}
}
