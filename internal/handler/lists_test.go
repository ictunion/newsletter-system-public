package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kubik/newsletter-system/internal/db"
	"github.com/kubik/newsletter-system/internal/dbtest"
)

func mustExec(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

func insertList(t *testing.T, pool *pgxpool.Pool, name string, listmonkID int, deleted bool) string {
	t.Helper()
	var id string
	err := pool.QueryRow(context.Background(),
		`INSERT INTO newsletter_lists (name, listmonk_id, listmonk_subscriber_count, deleted_at)
		 VALUES ($1, $2, 7, CASE WHEN $3 THEN now() END) RETURNING id::text`, name, listmonkID, deleted).Scan(&id)
	if err != nil {
		t.Fatalf("insert list: %v", err)
	}
	return id
}

func getLists(t *testing.T, queries *db.Queries, query string) (int, listsResponse) {
	t.Helper()
	recorder := httptest.NewRecorder()
	ListLists(queries)(recorder, httptest.NewRequest(http.MethodGet, "/lists"+query, nil))
	var response listsResponse
	if recorder.Code == http.StatusOK {
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
			t.Fatalf("decoding: %v", err)
		}
	}
	return recorder.Code, response
}

func TestListLists(t *testing.T) {
	pool := dbtest.Pool(t)
	queries := db.New(pool)
	praha := insertList(t, pool, "Praha", 1, false)
	insertList(t, pool, "Archived", 2, true)
	for i, status := range []string{"subscribed", "subscribed", "unsubscribed", "removed"} {
		var subscriberID string
		if err := pool.QueryRow(context.Background(),
			`INSERT INTO subscribers (email, name) VALUES ($1, 'x') RETURNING id::text`,
			string(rune('a'+i))+"@test.cz").Scan(&subscriberID); err != nil {
			t.Fatal(err)
		}
		mustExec(t, pool, `INSERT INTO subscriptions (subscriber_id, list_id, status, source) VALUES ($1, $2, $3, 'manual')`, subscriberID, praha, status)
	}

	code, response := getLists(t, queries, "")
	if code != http.StatusOK || len(response.Lists) != 1 {
		t.Fatalf("code = %d, lists = %+v", code, response.Lists)
	}
	got := response.Lists[0]
	if got.Name != "Praha" || got.Visibility != "private" || *got.ListmonkID != 1 || *got.ListmonkSubscriberCount != 7 ||
		got.SubscribedCount != 2 || got.UnsubscribedCount != 1 || got.RemovedCount != 1 || got.DeletedAt.Valid {
		t.Errorf("list = %+v", got)
	}

	if _, response := getLists(t, queries, "?include_deleted=true"); len(response.Lists) != 2 || response.Lists[0].Name != "Archived" ||
		!response.Lists[0].DeletedAt.Valid || response.Lists[0].SubscribedCount != 0 {
		t.Errorf("with deleted = %+v", response.Lists)
	}
	if code, _ := getLists(t, queries, "?include_deleted=maybe"); code != http.StatusBadRequest {
		t.Errorf("bad include_deleted: code = %d", code)
	}
}

func TestUpdateList(t *testing.T) {
	pool := dbtest.Pool(t)
	queries := db.New(pool)
	id := insertList(t, pool, "Praha", 1, false)

	tests := []struct {
		name, id, body string
		want           int
	}{
		{"ok", id, `{"visibility":"members"}`, http.StatusOK},
		{"bad visibility", id, `{"visibility":"secret"}`, http.StatusBadRequest},
		{"unknown field", id, `{"visibility":"public","name":"x"}`, http.StatusBadRequest},
		{"bad id", "42", `{"visibility":"public"}`, http.StatusBadRequest},
		{"missing list", "00000000-0000-0000-0000-000000000000", `{"visibility":"public"}`, http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPut, "/lists/"+tt.id, strings.NewReader(tt.body))
			request.SetPathValue("id", tt.id)
			UpdateList(queries)(recorder, request)
			if recorder.Code != tt.want {
				t.Errorf("code = %d, want %d, body = %s", recorder.Code, tt.want, recorder.Body)
			}
			if tt.want == http.StatusOK {
				var got listItem
				if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil || got.Visibility != "members" || got.Name != "Praha" {
					t.Errorf("response = %+v, err = %v", got, err)
				}
			}
		})
	}

	_, response := getLists(t, queries, "")
	if response.Lists[0].Visibility != "members" {
		t.Errorf("visibility = %q, want members", response.Lists[0].Visibility)
	}
}
