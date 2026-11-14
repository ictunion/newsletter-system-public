package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/golang-jwt/jwt/v5"

	"github.com/kubik/newsletter-system/internal/dbtest"
	"github.com/kubik/newsletter-system/internal/handler"
	"github.com/kubik/newsletter-system/internal/keycloak"
	"github.com/kubik/newsletter-system/internal/keycloaktest"
	"github.com/kubik/newsletter-system/internal/members"
)

// TestRouteRoles checks every route against its allowed roles: a token with
// every other role gets 403, a token with any one allowed role gets past
// auth (any status but 401/403).
func TestRouteRoles(t *testing.T) {
	provider := keycloaktest.New(t)
	api := http.NewServeMux()
	registerRoutes(api, context.Background(), provider.Provider, dbtest.Pool(t), handler.Syncs{}, members.Service{})

	allRoles := []keycloak.Role{keycloak.RoleViewAuditLogs, keycloak.RoleViewNewsletters, keycloak.RoleManageNewsletters, keycloak.RoleUpdateNewsletterMembers}
	token := func(roles ...keycloak.Role) string {
		names := make([]string, len(roles))
		for i, role := range roles {
			names[i] = string(role)
		}
		return provider.SignToken(t, keycloak.Claims{
			RegisteredClaims: jwt.RegisteredClaims{Subject: "test-admin"},
			ResourceAccess:   map[string]keycloak.RolesClaim{keycloaktest.ClientID: {Roles: names}},
		})
	}

	const someID = "00000000-0000-0000-0000-000000000000"
	view, manage, updateMembers := keycloak.RoleViewNewsletters, keycloak.RoleManageNewsletters, keycloak.RoleUpdateNewsletterMembers
	routes := []struct {
		method, path string
		roles        []keycloak.Role
	}{
		{http.MethodGet, "/audit-logs", []keycloak.Role{keycloak.RoleViewAuditLogs}},
		{http.MethodGet, "/lists", []keycloak.Role{view}},
		{http.MethodPut, "/lists/" + someID, []keycloak.Role{manage}},
		{http.MethodGet, "/lists/" + someID + "/subscribers", []keycloak.Role{view}},
		{http.MethodPut, "/lists/" + someID + "/members/1", []keycloak.Role{manage}},
		{http.MethodDelete, "/lists/" + someID + "/members/1", []keycloak.Role{manage}},
		{http.MethodPost, "/members", []keycloak.Role{updateMembers}},
		{http.MethodPut, "/members/1", []keycloak.Role{updateMembers}},
		{http.MethodDelete, "/members/1", []keycloak.Role{updateMembers, manage}},
		{http.MethodGet, "/members/1", []keycloak.Role{view}},
		{http.MethodGet, "/subscribers/" + someID, []keycloak.Role{view}},
		{http.MethodDelete, "/subscribers/" + someID, []keycloak.Role{manage}},
		{http.MethodGet, "/rules", []keycloak.Role{view}},
		{http.MethodPost, "/rules", []keycloak.Role{manage}},
		{http.MethodPut, "/rules/" + someID, []keycloak.Role{manage}},
		{http.MethodDelete, "/rules/" + someID, []keycloak.Role{manage}},
		{http.MethodGet, "/changes", []keycloak.Role{view}},
		{http.MethodPost, "/changes/" + someID + "/approve", []keycloak.Role{manage}},
		{http.MethodPost, "/changes/" + someID + "/reject", []keycloak.Role{manage}},
		{http.MethodPost, "/changes/approve", []keycloak.Role{manage}},
		{http.MethodGet, "/jobs", []keycloak.Role{view}},
		{http.MethodPost, "/jobs/" + someID + "/retry", []keycloak.Role{manage}},
		{http.MethodGet, "/sync-runs", []keycloak.Role{view}},
		{http.MethodPost, "/sync-runs/listmonk_lists", []keycloak.Role{manage}},
	}
	for _, route := range routes {
		t.Run(route.method+" "+route.path, func(t *testing.T) {
			var others []keycloak.Role
			for _, role := range allRoles {
				if !slices.Contains(route.roles, role) {
					others = append(others, role)
				}
			}
			type roleCase struct {
				name, token string
				allowed     bool
			}
			cases := []roleCase{{"other roles", token(others...), false}}
			for _, role := range route.roles {
				cases = append(cases, roleCase{string(role), token(role), true})
			}
			for _, tt := range cases {
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
