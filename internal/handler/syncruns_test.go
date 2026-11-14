package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kubik/newsletter-system/internal/db"
	"github.com/kubik/newsletter-system/internal/dbtest"
)

func TestListSyncRuns(t *testing.T) {
	pool := dbtest.Pool(t)
	for i := range syncRunsPerKind + 2 {
		mustExec(t, pool, `INSERT INTO sync_runs (kind, status, started_at, finished_at, items_fetched)
			VALUES ('listmonk_lists', 'succeeded', now() - make_interval(mins => $1), now(), $1)`, i)
	}
	mustExec(t, pool, `INSERT INTO sync_runs (kind, status, error) VALUES ('orca_members', 'failed', 'boom')`)

	recorder := httptest.NewRecorder()
	ListSyncRuns(db.New(pool))(recorder, httptest.NewRequest(http.MethodGet, "/sync-runs", nil))
	var response syncRunsResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil || recorder.Code != http.StatusOK {
		t.Fatalf("code = %d, err = %v", recorder.Code, err)
	}
	if len(response.Runs) != syncRunsPerKind+1 {
		t.Fatalf("len(runs) = %d, want %d", len(response.Runs), syncRunsPerKind+1)
	}
	if first := response.Runs[0]; first.Kind != "listmonk_lists" || first.ItemsFetched != 0 || !first.FinishedAt.Valid {
		t.Errorf("first run = %+v, want newest listmonk_lists", first)
	}
	if last := response.Runs[syncRunsPerKind]; last.Kind != "orca_members" || last.Status != "failed" || *last.Error != "boom" {
		t.Errorf("last run = %+v", last)
	}
}

func TestTriggerSync(t *testing.T) {
	queries := dbtest.Tx(t)
	ran := make(chan context.Context, 1)
	syncs := Syncs{"listmonk_lists": func(ctx context.Context) error { ran <- ctx; return nil }}
	appContext := context.WithValue(context.Background(), struct{}{}, "app")

	trigger := func(kind string) int {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/sync-runs/"+kind, nil)
		request.SetPathValue("kind", kind)
		TriggerSync(appContext, queries, syncs)(recorder, request)
		return recorder.Code
	}

	if code := trigger("orca_members"); code != http.StatusNotFound {
		t.Errorf("disabled kind: code = %d, want 404", code)
	}
	if code := trigger("listmonk_lists"); code != http.StatusAccepted {
		t.Fatalf("code = %d, want 202", code)
	}
	select {
	case ctx := <-ran:
		if ctx.Value(struct{}{}) != "app" {
			t.Error("sync did not run under the app context")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("sync did not run")
	}

	if _, err := queries.StartSyncRun(context.Background(), "listmonk_lists"); err != nil {
		t.Fatal(err)
	}
	if code := trigger("listmonk_lists"); code != http.StatusConflict {
		t.Errorf("while running: code = %d, want 409", code)
	}
}
