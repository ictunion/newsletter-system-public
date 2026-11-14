package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kubik/newsletter-system/internal/db"
	"github.com/kubik/newsletter-system/internal/dbtest"
)

func serveRule(t *testing.T, h http.HandlerFunc, method, id, body string) (int, ruleItem) {
	t.Helper()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(method, "/rules/"+id, strings.NewReader(body))
	request.SetPathValue("id", id)
	h(recorder, request)
	var rule ruleItem
	if recorder.Code == http.StatusOK || recorder.Code == http.StatusCreated {
		if err := json.Unmarshal(recorder.Body.Bytes(), &rule); err != nil {
			t.Fatalf("decoding %s: %v", recorder.Body, err)
		}
	}
	return recorder.Code, rule
}

func TestCreateRule(t *testing.T) {
	pool := dbtest.Pool(t)
	queries := db.New(pool)
	listID := insertList(t, pool, "Praha", 1, false)
	deletedID := insertList(t, pool, "Old", 2, true)

	tests := []struct {
		name, body string
		want       int
	}{
		{"all", `{"kind":"all","list_id":"` + listID + `"}`, http.StatusCreated},
		{"city", `{"kind":"city","list_id":"` + listID + `","city_contains":" praha ","enabled":false,"note":"Prague"}`, http.StatusCreated},
		{"unknown kind", `{"kind":"workplace","list_id":"` + listID + `"}`, http.StatusBadRequest},
		{"city without value", `{"kind":"city","list_id":"` + listID + `","city_contains":"  "}`, http.StatusBadRequest},
		{"all with city", `{"kind":"all","list_id":"` + listID + `","city_contains":"praha"}`, http.StatusBadRequest},
		{"missing list_id", `{"kind":"all"}`, http.StatusBadRequest},
		{"bad list_id", `{"kind":"all","list_id":"7"}`, http.StatusBadRequest},
		{"unknown list", `{"kind":"all","list_id":"00000000-0000-0000-0000-000000000000"}`, http.StatusBadRequest},
		{"deleted list", `{"kind":"all","list_id":"` + deletedID + `"}`, http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if code, _ := serveRule(t, CreateRule(queries), http.MethodPost, "", tt.body); code != tt.want {
				t.Errorf("code = %d, want %d", code, tt.want)
			}
		})
	}

	rules, err := queries.ListRules(context.Background())
	if err != nil || len(rules) != 2 {
		t.Fatalf("rules = %+v, err = %v", rules, err)
	}
	if city := rules[1]; *city.CityContains != "praha" || city.Enabled || *city.Note != "Prague" {
		t.Errorf("city rule = %+v, want trimmed city, disabled, note", city)
	}
	if !rules[0].Enabled {
		t.Error("enabled should default to true")
	}
}

func TestUpdateAndDeleteRule(t *testing.T) {
	pool := dbtest.Pool(t)
	queries := db.New(pool)
	listID := insertList(t, pool, "Praha", 1, false)
	otherID := insertList(t, pool, "Brno", 2, false)
	_, created := serveRule(t, CreateRule(queries), http.MethodPost, "", `{"kind":"all","list_id":"`+listID+`"}`)
	id := created.ID.String()

	code, updated := serveRule(t, UpdateRule(queries), http.MethodPut, id, `{"kind":"city","list_id":"`+otherID+`","city_contains":"Brno","enabled":false}`)
	if code != http.StatusOK || updated.Kind != "city" || updated.ListID.String() != otherID || *updated.CityContains != "Brno" || updated.Enabled || updated.ID != created.ID {
		t.Errorf("update: code = %d, rule = %+v", code, updated)
	}
	if code, _ := serveRule(t, UpdateRule(queries), http.MethodPut, id, `{"kind":"city","list_id":"`+otherID+`"}`); code != http.StatusBadRequest {
		t.Errorf("invalid update: code = %d, want 400", code)
	}
	missing := "00000000-0000-0000-0000-000000000000"
	if code, _ := serveRule(t, UpdateRule(queries), http.MethodPut, missing, `{"kind":"all","list_id":"`+listID+`"}`); code != http.StatusNotFound {
		t.Errorf("missing update: code = %d, want 404", code)
	}

	// Deleting a rule keeps its subscriptions, with rule_id cleared.
	var subscriberID string
	if err := pool.QueryRow(context.Background(), `INSERT INTO subscribers (email, name) VALUES ('a@b.cz', 'a') RETURNING id::text`).Scan(&subscriberID); err != nil {
		t.Fatal(err)
	}
	mustExec(t, pool, `INSERT INTO subscriptions (subscriber_id, list_id, status, source, rule_id) VALUES ($1, $2, 'subscribed', 'rule', $3)`, subscriberID, otherID, id)

	if code, _ := serveRule(t, DeleteRule(queries), http.MethodDelete, id, ""); code != http.StatusNoContent {
		t.Errorf("delete: code = %d, want 204", code)
	}
	if code, _ := serveRule(t, DeleteRule(queries), http.MethodDelete, id, ""); code != http.StatusNotFound {
		t.Errorf("delete again: code = %d, want 404", code)
	}
	var ruleIDNull bool
	if err := pool.QueryRow(context.Background(), `SELECT rule_id IS NULL FROM subscriptions`).Scan(&ruleIDNull); err != nil || !ruleIDNull {
		t.Errorf("subscription after rule delete: rule_id null = %v, err = %v", ruleIDNull, err)
	}

	recorder := httptest.NewRecorder()
	ListRules(queries)(recorder, httptest.NewRequest(http.MethodGet, "/rules", nil))
	if recorder.Code != http.StatusOK || strings.TrimSpace(recorder.Body.String()) != `{"rules":[]}` {
		t.Errorf("list after delete: %d %s", recorder.Code, recorder.Body)
	}
}
