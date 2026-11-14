// Package syncjob runs the background syncs (Listmonk lists, Listmonk
// subscriptions, orca members), recording each run in sync_runs.
package syncjob

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kubik/newsletter-system/internal/db"
	"github.com/kubik/newsletter-system/internal/listmonk"
	"github.com/kubik/newsletter-system/internal/worker"
)

// Kind is a sync_runs.kind.
type Kind string

const (
	KindListmonkLists         Kind = "listmonk_lists"
	KindListmonkSubscriptions Kind = "listmonk_subscriptions"
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
					Kind: ChangeListRemoved, ListID: list.ID, Payload: payload,
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

// RunListmonkSubscriptionImport makes Listmonk UI changes and people's own
// opt-outs part of our state (flow 7). For every subscriber on a linked list:
// find ours by Listmonk ID, then by email (and link), else create a
// non-member; then import their memberships (worker.ImportMemberships with
// step 4). Finally our 'subscribed' rows missing from Listmonk become
// 'removed' (step 5). Subscribers with a push job (pending or dead) are
// skipped, since our unsent change wins, and so are Listmonk subscribers
// with a delete job, since we already deleted them. Steps 4 and 5 skip rows
// changed after the fetch began: they are newer than what was fetched.
//
// Each subscriber is imported in its own transaction, so the API is never
// blocked for long; the run as a whole is not atomic, and rerunning it is safe.
func RunListmonkSubscriptionImport(ctx context.Context, pool *pgxpool.Pool, client *listmonk.Client) error {
	return record(ctx, pool, KindListmonkSubscriptions, func(ctx context.Context) (int, int, error) {
		queries := db.New(pool)
		lists, err := queries.ListAllLists(ctx)
		if err != nil {
			return 0, 0, err
		}
		pushJobs, err := queries.ListSubscriberIDsWithPushJobs(ctx)
		if err != nil {
			return 0, 0, err
		}
		deleteJobs, err := queries.ListListmonkIDsWithDeleteJobs(ctx)
		if err != nil {
			return 0, 0, err
		}

		fetchStarted, err := queries.DBNow(ctx)
		if err != nil {
			return 0, 0, err
		}
		// Fetch everything before writing anything, so a failed fetch can't
		// make step 5 remove rows.
		subscribers := map[int]listmonk.Subscriber{}
		members := map[pgtype.UUID]map[int32]bool{} // our list ID -> Listmonk subscriber IDs on it
		var linked []db.NewsletterList
		for _, list := range lists {
			if list.ListmonkID == nil || list.DeletedAt.Valid {
				continue
			}
			linked = append(linked, list)
			onList, err := client.ListSubscribersByList(ctx, int(*list.ListmonkID))
			if err != nil {
				return 0, 0, err
			}
			members[list.ID] = map[int32]bool{}
			for _, subscriber := range onList {
				subscribers[subscriber.ID] = subscriber
				members[list.ID][int32(subscriber.ID)] = true
			}
		}

		changes := 0
		for _, subscriber := range subscribers {
			if slices.Contains(deleteJobs, int32(subscriber.ID)) {
				continue
			}
			var imported int
			err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
				imported, err = importSubscriber(ctx, db.New(tx), subscriber, pushJobs, fetchStarted)
				return err
			})
			if err != nil {
				return len(subscribers), changes, fmt.Errorf("importing listmonk subscriber %d: %w", subscriber.ID, err)
			}
			changes += imported
		}

		for _, list := range linked {
			rows, err := queries.ListLinkedSubscribedOnList(ctx, db.ListLinkedSubscribedOnListParams{ListID: list.ID, ChangedBefore: fetchStarted})
			if err != nil {
				return len(subscribers), changes, err
			}
			for _, row := range rows {
				if members[list.ID][row.ListmonkSubscriberID] || slices.Contains(pushJobs, row.ID) {
					continue
				}
				if err := queries.SetSubscriptionStatus(ctx, db.SetSubscriptionStatusParams{SubscriberID: row.ID, ListID: list.ID, Status: "removed"}); err != nil {
					return len(subscribers), changes, err
				}
				changes++
			}
		}
		return len(subscribers), changes, nil
	})
}

// importSubscriber is flow 7 steps 1-4 and 6 for one Listmonk subscriber.
func importSubscriber(ctx context.Context, queries *db.Queries, current listmonk.Subscriber, pushJobs []pgtype.UUID, fetchStarted time.Time) (int, error) {
	listmonkID := int32(current.ID)
	subscriber, err := queries.GetSubscriberByListmonkID(ctx, &listmonkID)
	link := false
	if errors.Is(err, pgx.ErrNoRows) {
		subscriber, err = queries.GetSubscriberByEmail(ctx, current.Email)
		link = err == nil
		if link && subscriber.ListmonkSubscriberID != nil {
			log.Printf("sync: listmonk subscriber %d has the email of our subscriber %s, linked to listmonk %d; skipped",
				current.ID, subscriber.ID, *subscriber.ListmonkSubscriberID)
			return 0, nil
		}
	}
	changes := 0
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		if subscriber, err = queries.CreateNonMember(ctx, db.CreateNonMemberParams{Email: current.Email, Name: current.Name, ListmonkSubscriberID: &listmonkID}); err != nil {
			return 0, err
		}
		changes++
	case err != nil:
		return 0, err
	case slices.Contains(pushJobs, subscriber.ID):
		return 0, nil
	case link:
		if err := queries.LinkListmonkSubscriber(ctx, db.LinkListmonkSubscriberParams{ID: subscriber.ID, ListmonkSubscriberID: &listmonkID}); err != nil {
			return 0, err
		}
		changes++
	}
	imported, err := worker.ImportMemberships(ctx, queries, subscriber.ID, current, &fetchStarted)
	return changes + imported, err
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
