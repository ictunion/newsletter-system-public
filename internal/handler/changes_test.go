package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kubik/newsletter-system/internal/db"
	"github.com/kubik/newsletter-system/internal/dbtest"
	"github.com/kubik/newsletter-system/internal/keycloak"
	"github.com/kubik/newsletter-system/internal/members"
)

const adminSub = "admin-sub"

// raise creates a pending change and returns its ID.
func raise(t *testing.T, pool *pgxpool.Pool, kind string, memberNumber *int32, listID, payload string) string {
	t.Helper()
	var id string
	err := pool.QueryRow(context.Background(),
		`INSERT INTO pending_changes (kind, member_number, list_id, payload) VALUES ($1, $2, NULLIF($3, '')::uuid, $4) RETURNING id::text`,
		kind, memberNumber, listID, payload).Scan(&id)
	if err != nil {
		t.Fatalf("raise %s: %v", kind, err)
	}
	return id
}

// asAdmin serves h with an authenticated caller, as RequireRole would.
func asAdmin(t *testing.T, h http.HandlerFunc, method, body string, pathValues map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(method, "/", strings.NewReader(body))
	for name, value := range pathValues {
		request.SetPathValue(name, value)
	}
	claims := &keycloak.Claims{RegisteredClaims: jwt.RegisteredClaims{Subject: adminSub}}
	h(recorder, request.WithContext(withClaims(request.Context(), claims, "token")))
	return recorder
}

func TestApproveAndRejectChanges(t *testing.T) {
	pool := dbtest.Pool(t)
	ctx := context.Background()
	service := members.Service{PushEnabled: true}
	listA := insertList(t, pool, "A", 1, false)
	listB := insertList(t, pool, "B", 2, false)
	listGone := insertList(t, pool, "Gone", 3, false)
	if _, err := db.New(pool).CreateRule(ctx, db.CreateRuleParams{Kind: "all", ListID: mustUUID(t, listA), Enabled: true}); err != nil {
		t.Fatal(err)
	}
	serve(t, CreateMember(pool, service), http.MethodPost, janBody, nil) // 834, on A by rule
	serve(t, CreateMember(pool, service), http.MethodPost, strings.NewReplacer("834", "835", "jan@", "eva@").Replace(janBody), nil)
	mustExec(t, pool, `DELETE FROM provider_jobs`)
	n := func(v int32) *int32 { return &v }

	memberNew := raise(t, pool, "member_new", n(900), "", `{"member_number":900,"email":"new@example.com","name":"New One","city":null,"lang":"cs","workplace_newsletter_id":null}`)
	memberUpdated := raise(t, pool, "member_updated", n(834), "", `{"member_number":834,"email":"jan@example.com","name":"Jan Novák","city":"Brno","lang":"cs","workplace_newsletter_id":null}`)
	memberRemoved := raise(t, pool, "member_removed", n(835), "", `{"email":"eva@example.com","name":"Eva"}`)
	subscriptionAdd := raise(t, pool, "subscription_add", n(834), listB, `{"source":"workplace","rule_id":null}`)
	subscriptionRemove := raise(t, pool, "subscription_remove", n(834), listA, `{"source":"rule"}`)
	listRemoved := raise(t, pool, "list_removed", nil, listGone, `{"listmonk_id":3,"name":"Gone"}`)
	rejected := raise(t, pool, "member_new", n(901), "", `{"member_number":901,"email":"no@example.com","name":"No","city":null,"lang":"cs","workplace_newsletter_id":null}`)
	conflicting := raise(t, pool, "member_new", n(902), "", `{"member_number":902,"email":"JAN@example.com","name":"Dup","city":null,"lang":"cs","workplace_newsletter_id":null}`)

	approve := func(id string) *httptest.ResponseRecorder {
		return asAdmin(t, ApproveChange(pool, service), http.MethodPost, "", map[string]string{"id": id})
	}
	for _, id := range []string{memberNew, memberUpdated, memberRemoved} {
		recorder := approve(id)
		var item changeItem
		_ = json.Unmarshal(recorder.Body.Bytes(), &item)
		if recorder.Code != http.StatusOK || item.Status != "approved" || *item.DecidedBy != adminSub || !item.DecidedAt.Valid {
			t.Errorf("approve %s: %d %s", id, recorder.Code, recorder.Body)
		}
	}
	if count(t, pool, `SELECT count(*) FROM subscribers WHERE member_number = 900`) != 1 {
		t.Error("member_new not applied")
	}
	if count(t, pool, `SELECT count(*) FROM subscribers WHERE member_number = 834 AND city = 'Brno'`) != 1 {
		t.Error("member_updated not applied")
	}
	if count(t, pool, `SELECT count(*) FROM subscribers WHERE member_number = 835`) != 0 {
		t.Error("member_removed not applied")
	}

	// Bulk: three good ones, one already decided, one unknown.
	unknown := "00000000-0000-0000-0000-000000000000"
	body := `{"ids":["` + strings.Join([]string{subscriptionAdd, subscriptionRemove, listRemoved, memberNew, unknown}, `","`) + `"]}`
	recorder := asAdmin(t, ApproveChanges(pool, service), http.MethodPost, body, nil)
	var bulk bulkApproveResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &bulk); err != nil || recorder.Code != http.StatusOK {
		t.Fatalf("bulk: %d %s", recorder.Code, recorder.Body)
	}
	if len(bulk.Approved) != 3 || len(bulk.Failed) != 2 || bulk.Failed[0].Error != "change is already decided" || bulk.Failed[1].Error != "change not found" {
		t.Errorf("bulk = %+v", bulk)
	}
	var stateA, stateB string
	state := `SELECT source || '/' || status FROM subscriptions JOIN subscribers ON id = subscriber_id WHERE member_number = 834 AND list_id = $1`
	_ = pool.QueryRow(ctx, state, listA).Scan(&stateA)
	_ = pool.QueryRow(ctx, state, listB).Scan(&stateB)
	if stateA != "rule/removed" || stateB != "workplace/subscribed" {
		t.Errorf("subscriptions A = %s, B = %s", stateA, stateB)
	}
	if count(t, pool, `SELECT count(*) FROM newsletter_lists WHERE deleted_at IS NOT NULL AND id = $1`, listGone) != 1 {
		t.Error("list_removed not applied")
	}
	if count(t, pool, `SELECT count(*) FROM provider_jobs WHERE kind = 'push_subscriber'`) == 0 {
		t.Error("approved changes queued no push")
	}

	// Reject changes nothing else.
	recorder = asAdmin(t, RejectChange(pool), http.MethodPost, "", map[string]string{"id": rejected})
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"status":"rejected"`) ||
		count(t, pool, `SELECT count(*) FROM subscribers WHERE member_number = 901`) != 0 {
		t.Errorf("reject: %d %s", recorder.Code, recorder.Body)
	}
	if code := approve(rejected).Code; code != http.StatusConflict {
		t.Errorf("approve rejected: code = %d, want 409", code)
	}

	// A change that can't be applied stays pending.
	recorder = approve(conflicting)
	if recorder.Code != http.StatusConflict || !strings.Contains(recorder.Body.String(), "email already used") ||
		count(t, pool, `SELECT count(*) FROM pending_changes WHERE id = $1 AND status = 'pending'`, conflicting) != 1 {
		t.Errorf("conflicting: %d %s", recorder.Code, recorder.Body)
	}
	if code := approve(unknown).Code; code != http.StatusNotFound {
		t.Errorf("unknown: code = %d, want 404", code)
	}

	// orca updated the member after detection: the older payload must not win.
	stale := raise(t, pool, "member_updated", n(834), "", `{"member_number":834,"email":"jan@example.com","name":"Jan Novák","city":"Praha","lang":"cs","workplace_newsletter_id":null}`)
	mustExec(t, pool, `UPDATE pending_changes SET detected_at = now() - interval '1 hour' WHERE id = $1`, stale)
	recorder = approve(stale)
	if recorder.Code != http.StatusConflict || !strings.Contains(recorder.Body.String(), "member changed") ||
		count(t, pool, `SELECT count(*) FROM subscribers WHERE member_number = 834 AND city = 'Praha'`) != 0 {
		t.Errorf("stale: %d %s", recorder.Code, recorder.Body)
	}

	// The rule a subscription_add came from was deleted meanwhile.
	ruleGone := raise(t, pool, "subscription_add", n(834), listB, `{"source":"rule","rule_id":"00000000-0000-0000-0000-000000000001"}`)
	recorder = approve(ruleGone)
	if recorder.Code != http.StatusConflict || !strings.Contains(recorder.Body.String(), "rule not found") {
		t.Errorf("rule gone: %d %s", recorder.Code, recorder.Body)
	}
}

func TestListChanges(t *testing.T) {
	pool := dbtest.Pool(t)
	listID := insertList(t, pool, "Praha", 1, false)
	n := int32(834)
	raise(t, pool, "member_new", &n, "", `{"member_number":834}`)
	raise(t, pool, "list_removed", nil, listID, `{"listmonk_id":1,"name":"Praha"}`)
	mustExec(t, pool, `INSERT INTO pending_changes (kind, member_number, payload, status, decided_at, decided_by) VALUES ('member_removed', 1, '{}', 'rejected', now(), 'x')`)

	get := func(query string) (int, changesResponse) {
		recorder := httptest.NewRecorder()
		ListChanges(db.New(pool))(recorder, httptest.NewRequest(http.MethodGet, "/changes"+query, nil))
		var response changesResponse
		_ = json.Unmarshal(recorder.Body.Bytes(), &response)
		return recorder.Code, response
	}
	if _, all := get(""); all.Total != 3 {
		t.Errorf("all = %d, want 3", all.Total)
	}
	if _, pending := get("?status=pending"); pending.Total != 2 {
		t.Errorf("pending = %d, want 2", pending.Total)
	}
	_, lists := get("?status=pending&kind=list_removed")
	if lists.Total != 1 || *lists.Changes[0].ListName != "Praha" || string(lists.Changes[0].Payload) != `{"name":"Praha","listmonk_id":1}` {
		t.Errorf("list_removed = %+v", lists.Changes)
	}
	for _, query := range []string{"?status=open", "?kind=other"} {
		if code, _ := get(query); code != http.StatusBadRequest {
			t.Errorf("%s: code = %d, want 400", query, code)
		}
	}
}
