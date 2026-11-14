// Package dbtest gives tests a real, migrated Postgres database instead of a
// mocked *db.Queries — a large share of this app's actual logic lives in the
// SQL itself, which mocking the query layer can't exercise. TEST_DATABASE_URL points
// at it; `make test` sets that up (db-test-init + migrate-test) and passes
// the var in, so tests never need to know how the database got there.
package dbtest

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kubik/newsletter-system/internal/db"
)

var (
	poolOnce sync.Once
	pool     *pgxpool.Pool
	poolErr  error
)

func sharedPool(t *testing.T) *pgxpool.Pool {
	t.Helper()

	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set — skipping DB-backed test (run via `make test`)")
	}

	// One pool for the whole test binary, not one per test — pgxpool already
	// manages its own connection reuse; a fresh pool per test would just add
	// connection-setup overhead for no isolation benefit (isolation comes
	// from the per-test transaction in Tx, not from the pool).
	poolOnce.Do(func() {
		pool, poolErr = pgxpool.New(context.Background(), url)
	})
	if poolErr != nil {
		t.Fatalf("dbtest: connecting to TEST_DATABASE_URL: %v", poolErr)
	}
	return pool
}

// truncateTables lists every app table a dbtest.Pool test may write to. Add
// each new table here; tables whose rows are seeded by migration must stay out.
var truncateTables = []string{
	"audit_logs",
}

// Pool returns the shared *pgxpool.Pool for code that requires the concrete
// pool type because it opens its own transactions internally — Tx's
// single-rolled-back-transaction trick doesn't work there. Isolation instead
// comes from a t.Cleanup that truncates truncateTables after the test, so
// tests using Pool must not run t.Parallel() against each other. Skips (not
// fails) if TEST_DATABASE_URL is unset.
func Pool(t *testing.T) *pgxpool.Pool {
	t.Helper()

	pool := sharedPool(t)
	t.Cleanup(func() {
		sql := "TRUNCATE TABLE " + strings.Join(truncateTables, ", ") + " RESTART IDENTITY CASCADE"
		if _, err := pool.Exec(context.Background(), sql); err != nil {
			t.Fatalf("dbtest: truncating tables: %v", err)
		}
	})
	return pool
}

// Tx returns a *db.Queries bound to a fresh transaction that's rolled back
// automatically via t.Cleanup — every test starts from the clean, migrated
// schema and none of its writes outlive it, so tests can run in any order
// without truncation or manual cleanup. Skips (not fails) the test if
// TEST_DATABASE_URL is unset. Code that requires the concrete *pgxpool.Pool
// can't be handed this tx in its place; see Pool for that case instead.
func Tx(t *testing.T) *db.Queries {
	t.Helper()

	pool := sharedPool(t)
	tx, err := pool.Begin(context.Background())
	if err != nil {
		t.Fatalf("dbtest: begin tx: %v", err)
	}
	t.Cleanup(func() {
		_ = tx.Rollback(context.Background())
	})
	return db.New(tx)
}
