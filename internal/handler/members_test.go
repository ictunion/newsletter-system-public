package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kubik/newsletter-system/internal/db"
	"github.com/kubik/newsletter-system/internal/dbtest"
	"github.com/kubik/newsletter-system/internal/members"
)

const janBody = `{"member_number":834,"email":"jan@example.com","name":"Jan Novák","city":"Praha 4","lang":"cs","workplace_newsletter_id":null}`

// serve calls h with the path values set, returning status and the decoded memberResponse.
func serve(t *testing.T, h http.HandlerFunc, method, body string, pathValues map[string]string) (int, memberResponse) {
	t.Helper()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(method, "/", strings.NewReader(body))
	for name, value := range pathValues {
		request.SetPathValue(name, value)
	}
	h(recorder, request)
	var response memberResponse
	if recorder.Code == http.StatusOK || recorder.Code == http.StatusCreated {
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
			t.Fatalf("decoding %s: %v", recorder.Body, err)
		}
	}
	return recorder.Code, response
}

func count(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return n
}

func mustUUID(t *testing.T, s string) pgtype.UUID {
	t.Helper()
	var id pgtype.UUID
	if err := id.Scan(s); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestCreateMember(t *testing.T) {
	pool := dbtest.Pool(t)
	listID := insertList(t, pool, "Všichni", 1, false)
	if _, err := db.New(pool).CreateRule(context.Background(), db.CreateRuleParams{Kind: "all", ListID: mustUUID(t, listID), Enabled: true}); err != nil {
		t.Fatal(err)
	}
	create := CreateMember(pool, members.Service{PushEnabled: true})

	body := strings.Replace(janBody, `"workplace_newsletter_id":null`, `"workplace_newsletter_id":99`, 1)
	code, created := serve(t, create, http.MethodPost, body, nil)
	if code != http.StatusCreated || created.Name != "Jan Novák" || *created.MemberNumber != 834 || *created.City != "Praha 4" {
		t.Fatalf("create: code = %d, response = %+v", code, created)
	}
	if len(created.Subscriptions) != 1 || created.Subscriptions[0].ListName != "Všichni" || created.Subscriptions[0].Source != "rule" || created.Subscriptions[0].Status != "subscribed" {
		t.Errorf("subscriptions = %+v", created.Subscriptions)
	}
	if created.UnmatchedWorkplaceNewsletterID == nil || *created.UnmatchedWorkplaceNewsletterID != 99 {
		t.Errorf("unmatched_workplace_newsletter_id = %v, want 99", created.UnmatchedWorkplaceNewsletterID)
	}
	if n := count(t, pool, `SELECT count(*) FROM provider_jobs WHERE kind = 'push_subscriber'`); n != 1 {
		t.Errorf("push jobs = %d, want 1", n)
	}

	// Existing member_number: 200, nothing changes.
	code, again := serve(t, create, http.MethodPost, strings.Replace(janBody, "jan@example.com", "new@example.com", 1), nil)
	if code != http.StatusOK || again.ID != created.ID || again.Email != "jan@example.com" {
		t.Errorf("repeat: code = %d, response = %+v", code, again)
	}

	// Another member with Jan's email conflicts.
	other := strings.Replace(janBody, `"member_number":834`, `"member_number":835`, 1)
	if code, _ := serve(t, create, http.MethodPost, other, nil); code != http.StatusConflict {
		t.Errorf("email conflict: code = %d, want 409", code)
	}

	for name, invalid := range map[string]string{
		"zero member_number": strings.Replace(janBody, "834", "0", 1),
		"bad email":          strings.Replace(janBody, "jan@example.com", "Jan <jan@example.com>", 1),
		"missing lang":       strings.Replace(janBody, `"lang":"cs"`, `"lang":" "`, 1),
		"unknown field":      strings.Replace(janBody, `"lang":"cs"`, `"lang":"cs","phone":"1"`, 1),
	} {
		if code, _ := serve(t, create, http.MethodPost, invalid, nil); code != http.StatusBadRequest {
			t.Errorf("%s: code = %d, want 400", name, code)
		}
	}
}

func TestCreateMember_AttachesToNonMember(t *testing.T) {
	pool := dbtest.Pool(t)
	var guestID string
	if err := pool.QueryRow(context.Background(), `INSERT INTO subscribers (email, name) VALUES ('Jan@Example.com', 'Guest') RETURNING id::text`).Scan(&guestID); err != nil {
		t.Fatal(err)
	}
	code, response := serve(t, CreateMember(pool, members.Service{}), http.MethodPost, janBody, nil)
	if code != http.StatusCreated || response.ID.String() != guestID || *response.MemberNumber != 834 {
		t.Errorf("code = %d, response = %+v, want member attached to %s", code, response, guestID)
	}
	if n := count(t, pool, `SELECT count(*) FROM provider_jobs`); n != 0 {
		t.Errorf("jobs with Listmonk disabled = %d, want 0", n)
	}
}

func TestUpdateMember(t *testing.T) {
	pool := dbtest.Pool(t)
	service := members.Service{PushEnabled: true}
	insertList(t, pool, "Dílna", 13, false)
	serve(t, CreateMember(pool, service), http.MethodPost, janBody, nil)
	serve(t, CreateMember(pool, service), http.MethodPost, strings.NewReplacer(`834`, `835`, `jan@`, `eva@`).Replace(janBody), nil)
	update := UpdateMember(pool, service)
	path := map[string]string{"member_number": "834"}

	code, updated := serve(t, update, http.MethodPut, `{"email":"jan@example.com","name":"Jan Dvořák","lang":"en","workplace_newsletter_id":13}`, path)
	if code != http.StatusOK || updated.Name != "Jan Dvořák" || updated.City != nil || *updated.Lang != "en" ||
		len(updated.Subscriptions) != 1 || updated.Subscriptions[0].Source != "workplace" || updated.UnmatchedWorkplaceNewsletterID != nil {
		t.Errorf("update: code = %d, response = %+v", code, updated)
	}

	tests := []struct {
		name, number, body string
		want               int
	}{
		{"unknown member", "999", `{"email":"x@example.com","name":"X Y","lang":"cs"}`, http.StatusNotFound},
		{"email of another member", "834", `{"email":"EVA@example.com","name":"Jan Novák","lang":"cs"}`, http.StatusConflict},
		{"member_number in body", "834", janBody, http.StatusBadRequest},
		{"bad member_number", "abc", `{}`, http.StatusBadRequest},
	}
	for _, tt := range tests {
		if code, _ := serve(t, update, http.MethodPut, tt.body, map[string]string{"member_number": tt.number}); code != tt.want {
			t.Errorf("%s: code = %d, want %d", tt.name, code, tt.want)
		}
	}
}

func TestDeleteMemberAndSubscriber(t *testing.T) {
	pool := dbtest.Pool(t)
	service := members.Service{PushEnabled: true}
	serve(t, CreateMember(pool, service), http.MethodPost, janBody, nil)
	mustExec(t, pool, `UPDATE subscribers SET listmonk_subscriber_id = 42`)
	var guestID string
	if err := pool.QueryRow(context.Background(), `INSERT INTO subscribers (email, name, listmonk_subscriber_id) VALUES ('g@example.com', 'G', 43) RETURNING id::text`).Scan(&guestID); err != nil {
		t.Fatal(err)
	}

	for i := range 2 { // the second delete finds nothing and still answers 204
		if code, _ := serve(t, DeleteMember(pool, service), http.MethodDelete, "", map[string]string{"member_number": "834"}); code != http.StatusNoContent {
			t.Errorf("delete member #%d: code = %d, want 204", i+1, code)
		}
		if code, _ := serve(t, DeleteSubscriber(pool, service), http.MethodDelete, "", map[string]string{"id": guestID}); code != http.StatusNoContent {
			t.Errorf("delete subscriber #%d: code = %d, want 204", i+1, code)
		}
	}
	if n := count(t, pool, `SELECT count(*) FROM subscribers`); n != 0 {
		t.Errorf("subscribers left = %d", n)
	}
	// The pending push cascaded away; one delete job per provider ID remains.
	if n := count(t, pool, `SELECT count(*) FROM provider_jobs WHERE kind = 'push_subscriber'`); n != 0 {
		t.Errorf("push jobs = %d, want 0", n)
	}
	if n := count(t, pool, `SELECT count(*) FROM provider_jobs WHERE kind = 'delete_subscriber' AND listmonk_subscriber_id IN (42, 43)`); n != 2 {
		t.Errorf("delete jobs = %d, want 2", n)
	}

	// Without Listmonk no delete job is enqueued.
	mustExec(t, pool, `DELETE FROM provider_jobs`)
	serve(t, CreateMember(pool, members.Service{}), http.MethodPost, janBody, nil)
	mustExec(t, pool, `UPDATE subscribers SET listmonk_subscriber_id = 44`)
	serve(t, DeleteMember(pool, members.Service{}), http.MethodDelete, "", map[string]string{"member_number": "834"})
	if n := count(t, pool, `SELECT count(*) FROM provider_jobs`); n != 0 {
		t.Errorf("jobs with Listmonk disabled = %d, want 0", n)
	}
}

func TestGetMemberAndSubscriber(t *testing.T) {
	pool := dbtest.Pool(t)
	queries := db.New(pool)
	insertList(t, pool, "Dílna", 13, false)
	_, created := serve(t, CreateMember(pool, members.Service{}), http.MethodPost, strings.Replace(janBody, "null", "13", 1), nil)

	code, byNumber := serve(t, GetMember(queries), http.MethodGet, "", map[string]string{"member_number": "834"})
	if code != http.StatusOK || byNumber.ID != created.ID || len(byNumber.Subscriptions) != 1 || byNumber.Subscriptions[0].ListmonkID == nil {
		t.Errorf("by member_number: code = %d, response = %+v", code, byNumber)
	}
	code, byID := serve(t, GetSubscriber(queries), http.MethodGet, "", map[string]string{"id": created.ID.String()})
	if code != http.StatusOK || byID.Email != "jan@example.com" || len(byID.Subscriptions) != 1 {
		t.Errorf("by id: code = %d, response = %+v", code, byID)
	}

	for name, tt := range map[string]struct {
		h      http.HandlerFunc
		values map[string]string
		want   int
	}{
		"unknown member":     {GetMember(queries), map[string]string{"member_number": "1"}, http.StatusNotFound},
		"bad member_number":  {GetMember(queries), map[string]string{"member_number": "-1"}, http.StatusBadRequest},
		"unknown subscriber": {GetSubscriber(queries), map[string]string{"id": "00000000-0000-0000-0000-000000000000"}, http.StatusNotFound},
		"bad id":             {GetSubscriber(queries), map[string]string{"id": "x"}, http.StatusBadRequest},
	} {
		if code, _ := serve(t, tt.h, http.MethodGet, "", tt.values); code != tt.want {
			t.Errorf("%s: code = %d, want %d", name, code, tt.want)
		}
	}
}
