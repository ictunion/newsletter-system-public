package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kubik/newsletter-system/internal/db"
	"github.com/kubik/newsletter-system/internal/dbtest"
	"github.com/kubik/newsletter-system/internal/members"
)

func TestListMemberAssignment(t *testing.T) {
	pool := dbtest.Pool(t)
	service := members.Service{PushEnabled: true}
	listA := insertList(t, pool, "A", 1, false)
	listB := insertList(t, pool, "B", 2, false)
	listC := insertList(t, pool, "C", 3, false)
	deleted := insertList(t, pool, "Old", 4, true)
	serve(t, CreateMember(pool, service), http.MethodPost, janBody, nil)
	// B: removed rule row (an admin removed it earlier). C: the member opted out.
	mustExec(t, pool, `INSERT INTO subscriptions (subscriber_id, list_id, status, source)
		SELECT id, $1, 'removed', 'rule' FROM subscribers`, listB)
	mustExec(t, pool, `INSERT INTO subscriptions (subscriber_id, list_id, status, source)
		SELECT id, $1, 'unsubscribed', 'manual' FROM subscribers`, listC)
	mustExec(t, pool, `DELETE FROM provider_jobs`)

	add, remove := AddListMember(pool, service), RemoveListMember(pool, service)
	path := func(list, number string) map[string]string {
		return map[string]string{"id": list, "member_number": number}
	}
	state := func(list string) string {
		var status, source string
		if err := pool.QueryRow(t.Context(), `SELECT status, source FROM subscriptions WHERE list_id = $1`, list).Scan(&status, &source); err != nil {
			return err.Error()
		}
		return source + "/" + status
	}

	steps := []struct {
		name      string
		h         http.HandlerFunc
		list      string
		number    string
		wantCode  int
		wantState string
		wantPush  int
	}{
		{"add new", add, listA, "834", http.StatusOK, "manual/subscribed", 1},
		{"add again", add, listA, "834", http.StatusOK, "manual/subscribed", 0},
		{"remove", remove, listA, "834", http.StatusNoContent, "manual/removed", 1},
		{"remove again", remove, listA, "834", http.StatusNoContent, "manual/removed", 0},
		{"re-add removed", add, listA, "834", http.StatusOK, "manual/subscribed", 1},
		{"revive keeps source", add, listB, "834", http.StatusOK, "rule/subscribed", 1},
		{"add after opt-out", add, listC, "834", http.StatusConflict, "manual/unsubscribed", 0},
		{"remove after opt-out", remove, listC, "834", http.StatusNoContent, "manual/unsubscribed", 0},
		{"add to deleted list", add, deleted, "834", http.StatusNotFound, "no rows in result set", 0},
		{"remove from list without row", remove, deleted, "834", http.StatusNoContent, "no rows in result set", 0},
		{"unknown member", add, listA, "999", http.StatusNotFound, "manual/subscribed", 0},
		{"unknown list", remove, "00000000-0000-0000-0000-000000000000", "834", http.StatusNotFound, "no rows in result set", 0},
		{"bad list id", add, "x", "834", http.StatusBadRequest, "", 0},
	}
	for _, step := range steps {
		code, response := serve(t, step.h, http.MethodPut, "", path(step.list, step.number))
		if code != step.wantCode {
			t.Errorf("%s: code = %d, want %d", step.name, code, step.wantCode)
		}
		if code == http.StatusOK && (*response.MemberNumber != 834 || len(response.Subscriptions) != 3) {
			t.Errorf("%s: response = %+v", step.name, response)
		}
		if got := state(step.list); step.wantState != "" && got != step.wantState {
			t.Errorf("%s: state = %q, want %q", step.name, got, step.wantState)
		}
		if n := count(t, pool, `SELECT count(*) FROM provider_jobs WHERE kind = 'push_subscriber'`); n != step.wantPush {
			t.Errorf("%s: push jobs = %d, want %d", step.name, n, step.wantPush)
		}
		mustExec(t, pool, `DELETE FROM provider_jobs`)
	}
}

func TestListListSubscribers(t *testing.T) {
	pool := dbtest.Pool(t)
	queries := db.New(pool)
	listID := insertList(t, pool, "A", 1, false)
	for i, status := range []string{"subscribed", "removed", "subscribed", "subscribed"} {
		mustExec(t, pool, `WITH s AS (INSERT INTO subscribers (email, name) VALUES ($1, $2) RETURNING id)
			INSERT INTO subscriptions (subscriber_id, list_id, status, source) SELECT id, $3, $4, 'manual' FROM s`,
			fmt.Sprintf("%d@example.com", i), fmt.Sprintf("Name %d", 4-i), listID, status)
	}

	get := func(list, query string) (int, listSubscribersResponse) {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "/lists/"+list+"/subscribers"+query, nil)
		request.SetPathValue("id", list)
		ListListSubscribers(queries)(recorder, request)
		var response listSubscribersResponse
		if recorder.Code == http.StatusOK {
			if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
		}
		return recorder.Code, response
	}

	code, page := get(listID, "?status=subscribed&limit=2&offset=1")
	if code != http.StatusOK || page.Total != 3 || page.Limit != 2 || page.Offset != 1 || len(page.Subscribers) != 2 {
		t.Fatalf("code = %d, page = %+v", code, page)
	}
	// By name: "Name 1", "Name 2", "Name 4"; offset 1 starts at "Name 2".
	if names := page.Subscribers[0].Name + "," + page.Subscribers[1].Name; names != "Name 2,Name 4" {
		t.Errorf("names = %s", names)
	}
	if _, all := get(listID, ""); all.Total != 4 || all.Limit != listSubscribersDefaultLimit {
		t.Errorf("all = %+v", all)
	}
	for query, want := range map[string]int{"?status=gone": http.StatusBadRequest, "?limit=0": http.StatusBadRequest} {
		if code, _ := get(listID, query); code != want {
			t.Errorf("%s: code = %d, want %d", query, code, want)
		}
	}
	if code, _ := get("00000000-0000-0000-0000-000000000000", ""); code != http.StatusNotFound {
		t.Errorf("unknown list: code = %d, want 404", code)
	}
	if code, body := get(listID, "?status=removed"); code != http.StatusOK || len(body.Subscribers) != 1 || !strings.HasPrefix(body.Subscribers[0].Email, "1@") {
		t.Errorf("removed = %+v", body)
	}
}
