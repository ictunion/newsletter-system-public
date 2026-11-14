// Package syncjob runs the background syncs (Listmonk lists, Listmonk
// subscriptions, orca members), recording each run in sync_runs.
package syncjob

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kubik/newsletter-system/internal/db"
	"github.com/kubik/newsletter-system/internal/listmonk"
)

// Kind is a sync_runs.kind.
type Kind string

const (
	KindListmonkLists Kind = "listmonk_lists"
)

// ErrAlreadyRunning is returned when a run of the same kind is still running.
var ErrAlreadyRunning = errors.New("sync already running")

// FailStaleRuns marks runs left 'running' by a crash or restart as failed.
// Call it once at startup, before any sync starts.
//
// Assumes a single server instance (the deployment runs one): with several,
// one instance's startup would fail another's live run.
func FailStaleRuns(ctx context.Context, queries *db.Queries) error {
	failed, err := queries.FailStaleSyncRuns(ctx)
	if failed > 0 {
		log.Printf("sync: marked %d interrupted run(s) failed", failed)
	}
	return err
}

// record runs sync inside a sync_runs row: 'running' while it works, then
// 'succeeded' or 'failed' with its counts or error.
func record(ctx context.Context, pool *pgxpool.Pool, kind Kind, sync func(context.Context) (fetched, changes int, err error)) error {
	queries := db.New(pool)
	runID, err := queries.StartSyncRun(ctx, string(kind))
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.ConstraintName == "sync_runs_running_idx" {
			return ErrAlreadyRunning
		}
		return fmt.Errorf("starting %s run: %w", kind, err)
	}

	fetched, changes, syncErr := sync(ctx)
	finish := db.FinishSyncRunParams{ID: runID, Status: "succeeded", ItemsFetched: int32(fetched), ChangesDetected: int32(changes)}
	if syncErr != nil {
		message := syncErr.Error()
		finish.Status, finish.Error = "failed", &message
	}
	// Record the outcome even when ctx was cancelled mid-run (shutdown).
	if err := queries.FinishSyncRun(context.WithoutCancel(ctx), finish); err != nil {
		return errors.Join(syncErr, fmt.Errorf("finishing %s run: %w", kind, err))
	}
	return syncErr
}

// RunListmonkListSync copies Listmonk's lists into newsletter_lists
// (upsert by listmonk_id; new lists are 'private', a reappearing list gets
// deleted_at cleared) and raises a list_removed pending change for each
// active list Listmonk no longer returns.
func RunListmonkListSync(ctx context.Context, pool *pgxpool.Pool, client *listmonk.Client) error {
	return record(ctx, pool, KindListmonkLists, func(ctx context.Context) (int, int, error) {
		lists, err := client.ListLists(ctx)
		if err != nil {
			return 0, 0, err
		}
		changes := 0
		err = pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
			queries := db.New(tx)
			seen := make([]int32, 0, len(lists))
			for _, list := range lists {
				seen = append(seen, int32(list.ID))
				if err := queries.UpsertListmonkList(ctx, db.UpsertListmonkListParams{
					Name:                    list.Name,
					Description:             list.Description,
					ListmonkID:              int32(list.ID),
					ListmonkSubscriberCount: int32(list.SubscriberCount),
				}); err != nil {
					return fmt.Errorf("upserting listmonk list %d: %w", list.ID, err)
				}
			}
			if _, err := queries.DeleteResolvedListRemovedChanges(ctx, seen); err != nil {
				return err
			}
			missing, err := queries.ListMissingListmonkLists(ctx, seen)
			if err != nil {
				return err
			}
			for _, list := range missing {
				payload, _ := json.Marshal(map[string]any{"listmonk_id": list.ListmonkID, "name": list.Name})
				raised, err := queries.RaisePendingChange(ctx, db.RaisePendingChangeParams{
					Kind: "list_removed", ListID: list.ID, Payload: payload,
				})
				if err != nil {
					return fmt.Errorf("raising list_removed for list %d: %w", list.ListmonkID, err)
				}
				changes += int(raised)
			}
			return nil
		})
		return len(lists), changes, err
	})
}

// Every runs fn now and then every interval, until ctx is done. It blocks;
// start it in its own goroutine.
func Every(ctx context.Context, interval time.Duration, fn func(context.Context)) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		fn(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
