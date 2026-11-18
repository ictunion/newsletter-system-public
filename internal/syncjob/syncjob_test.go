package syncjob

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kubik/newsletter-system/internal/db"
	"github.com/kubik/newsletter-system/internal/dbtest"
	"github.com/kubik/newsletter-system/internal/listmonk"
)

// fakeLists serves GET /api/lists with *body (status 200) or *status.
func fakeLists(t *testing.T, body *string, status *int) *listmonk.Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/lists" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(*status)
		_, _ = w.Write([]byte(*body))
	}))
	t.Cleanup(server.Close)
	return listmonk.New(server.URL, "u", "t")
}

type listRow struct {
	name, description, visibility string
	count                         int32
	deleted                       bool
}

func listByListmonkID(t *testing.T, pool *pgxpool.Pool, listmonkID int) listRow {
	t.Helper()
	var row listRow
	err := pool.QueryRow(context.Background(),
		`SELECT name, description, visibility, listmonk_subscriber_count, deleted_at IS NOT NULL FROM newsletter_lists WHERE listmonk_id = $1`,
		listmonkID).Scan(&row.name, &row.description, &row.visibility, &row.count, &row.deleted)
	if err != nil {
		t.Fatalf("list %d: %v", listmonkID, err)
	}
	return row
}

type runRow struct {
	status           string
	fetched, changes int32
	error            *string
}

func lastRun(t *testing.T, pool *pgxpool.Pool) runRow {
	t.Helper()
	var row runRow
	err := pool.QueryRow(context.Background(),
		`SELECT status, items_fetched, changes_detected, error FROM sync_runs ORDER BY started_at DESC LIMIT 1`,
	).Scan(&row.status, &row.fetched, &row.changes, &row.error)
	if err != nil {
		t.Fatalf("last run: %v", err)
	}
	return row
}

func count(t *testing.T, pool *pgxpool.Pool, sql string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), sql).Scan(&n); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return n
}

func exec(t *testing.T, pool *pgxpool.Pool, sql string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), sql); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

const pendingListRemoved = `SELECT count(*) FROM pending_changes WHERE kind = 'list_removed' AND status = 'pending'`

func TestRunListmonkListSync(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Pool(t)
	status := 200
	body := `{"data":{"results":[
		{"id":1,"name":"Praha","description":"Pražská pobočka","subscriber_count":5},
		{"id":2,"name":"Brno","description":"","subscriber_count":3,"type":"public"}]}}`
	client := fakeLists(t, &body, &status)

	// New lists are created as private (public if public in Listmonk), with
	// Listmonk's fields copied.
	if err := RunListmonkListSync(ctx, pool, client); err != nil {
		t.Fatal(err)
	}
	if got, want := listByListmonkID(t, pool, 1), (listRow{"Praha", "Pražská pobočka", "private", 5, false}); got != want {
		t.Errorf("list 1 = %+v, want %+v", got, want)
	}
	if got := listByListmonkID(t, pool, 2).visibility; got != "public" {
		t.Errorf("list 2 visibility = %q, want public", got)
	}
	if run := lastRun(t, pool); run.status != "succeeded" || run.fetched != 2 || run.changes != 0 {
		t.Errorf("run = %+v", run)
	}

	// Upsert keeps visibility and updates the copied fields; a list missing
	// from Listmonk raises list_removed and is not soft-deleted.
	exec(t, pool, `UPDATE newsletter_lists SET visibility = 'members' WHERE listmonk_id = 1`)
	body = `{"data":{"results":[{"id":1,"name":"Praha 2","description":"d","subscriber_count":6}]}}`
	for range 2 { // re-detection refreshes the same pending change
		if err := RunListmonkListSync(ctx, pool, client); err != nil {
			t.Fatal(err)
		}
	}
	if got, want := listByListmonkID(t, pool, 1), (listRow{"Praha 2", "d", "members", 6, false}); got != want {
		t.Errorf("list 1 = %+v, want %+v", got, want)
	}
	if listByListmonkID(t, pool, 2).deleted {
		t.Error("list 2 soft-deleted without approval")
	}
	if n := count(t, pool, pendingListRemoved); n != 1 {
		t.Errorf("pending list_removed = %d, want 1", n)
	}
	if run := lastRun(t, pool); run.fetched != 1 || run.changes != 1 {
		t.Errorf("run = %+v", run)
	}

	// A rejected list_removed with the same payload is not raised again.
	exec(t, pool, `UPDATE pending_changes SET status = 'rejected', decided_at = now(), decided_by = 'admin'`)
	if err := RunListmonkListSync(ctx, pool, client); err != nil {
		t.Fatal(err)
	}
	if n := count(t, pool, pendingListRemoved); n != 0 {
		t.Errorf("pending list_removed after reject = %d, want 0", n)
	}
	if run := lastRun(t, pool); run.changes != 0 {
		t.Errorf("run = %+v", run)
	}

	// A list made public in Listmonk becomes public here, overriding ours.
	body = `{"data":{"results":[{"id":1,"name":"Praha 2","description":"d","subscriber_count":6,"type":"public"}]}}`
	if err := RunListmonkListSync(ctx, pool, client); err != nil {
		t.Fatal(err)
	}
	if got := listByListmonkID(t, pool, 1).visibility; got != "public" {
		t.Errorf("list 1 visibility = %q, want public", got)
	}
}

func TestRunListmonkListSync_Reappear(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Pool(t)
	status := 200
	body := `{"data":{"results":[]}}`
	client := fakeLists(t, &body, &status)

	exec(t, pool, `INSERT INTO newsletter_lists (name, listmonk_id, visibility, deleted_at) VALUES ('Old', 1, 'public', now())`)
	exec(t, pool, `INSERT INTO newsletter_lists (name, listmonk_id) VALUES ('Flaky', 2)`)
	if err := RunListmonkListSync(ctx, pool, client); err != nil {
		t.Fatal(err)
	}
	// Only the active list is reported; the soft-deleted one already is.
	if n := count(t, pool, pendingListRemoved); n != 1 {
		t.Fatalf("pending list_removed = %d, want 1", n)
	}

	body = `{"data":{"results":[{"id":1,"name":"Old","subscriber_count":0},{"id":2,"name":"Flaky","subscriber_count":0}]}}`
	if err := RunListmonkListSync(ctx, pool, client); err != nil {
		t.Fatal(err)
	}
	if got := listByListmonkID(t, pool, 1); got.deleted || got.visibility != "public" {
		t.Errorf("reappeared list = %+v, want not deleted, visibility kept", got)
	}
	if n := count(t, pool, pendingListRemoved); n != 0 {
		t.Errorf("pending list_removed after reappear = %d, want 0", n)
	}
}

func TestRunListmonkListSync_ListmonkError(t *testing.T) {
	pool := dbtest.Pool(t)
	status, body := 500, `{"message":"boom"}`
	err := RunListmonkListSync(context.Background(), pool, fakeLists(t, &body, &status))
	if err == nil {
		t.Fatal("want error")
	}
	if run := lastRun(t, pool); run.status != "failed" || run.error == nil || *run.error != err.Error() {
		t.Errorf("run = %+v, err = %v", run, err)
	}
}

func TestRunsDoNotOverlap_AndFailStaleRuns(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Pool(t)
	status, body := 200, `{"data":{"results":[]}}`
	client := fakeLists(t, &body, &status)

	exec(t, pool, `INSERT INTO sync_runs (kind) VALUES ('listmonk_lists')`)
	if err := RunListmonkListSync(ctx, pool, client); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("err = %v, want ErrAlreadyRunning", err)
	}

	if err := FailStaleRuns(ctx, db.New(pool)); err != nil {
		t.Fatal(err)
	}
	if run := lastRun(t, pool); run.status != "failed" {
		t.Errorf("stale run = %+v, want failed", run)
	}
	if err := RunListmonkListSync(ctx, pool, client); err != nil {
		t.Errorf("after FailStaleRuns: %v", err)
	}
}
