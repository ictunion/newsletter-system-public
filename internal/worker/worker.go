// Package worker runs provider_jobs: it makes Listmonk match our DB for a
// subscriber (push_subscriber) or deletes a subscriber there
// (delete_subscriber). See docs/architecture.md, "Provider worker".
package worker

import (
	"context"
	"errors"
	"fmt"
	"log"
	"maps"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kubik/newsletter-system/internal/db"
	"github.com/kubik/newsletter-system/internal/listmonk"
)

const (
	// PollInterval is how often the worker looks for due jobs.
	PollInterval = 15 * time.Second
	// leaseSeconds keeps a claimed job from being claimed again while it
	// runs; a crash mid-job makes it due again after this.
	leaseSeconds = 300
	// maxAttempts failures make a job dead (about 3h of retries).
	maxAttempts = 10
	firstRetry  = 30 * time.Second
	maxRetry    = time.Hour
)

type Worker struct {
	Pool     *pgxpool.Pool
	Listmonk *listmonk.Client
}

// RunDue runs due jobs one at a time until none is left or ctx is done.
// Each job is claimed in its own short transaction; Listmonk is called
// outside any transaction. If any job succeeded, Listmonk's per-list
// subscriber counts are refreshed once at the end.
func (w Worker) RunDue(ctx context.Context) {
	queries := db.New(w.Pool)
	succeeded := false
	defer func() {
		if succeeded {
			w.refreshCounts(context.WithoutCancel(ctx))
		}
	}()
	for ctx.Err() == nil {
		job, err := queries.ClaimDueJob(ctx, leaseSeconds)
		if errors.Is(err, pgx.ErrNoRows) {
			return
		}
		if err != nil {
			log.Printf("worker: claiming job: %v", err)
			return
		}

		jobErr := w.run(ctx, job)
		// Record the outcome even if ctx was cancelled mid-job (shutdown).
		recordContext := context.WithoutCancel(ctx)
		if jobErr == nil {
			succeeded = true
			_, err = queries.DeleteFinishedJob(recordContext, db.DeleteFinishedJobParams{ID: job.ID, ClaimedAt: job.UpdatedAt})
		} else {
			log.Printf("worker: %s job %s (attempt %d): %v", job.Kind, job.ID, job.AttemptCount+1, jobErr)
			message := jobErr.Error()
			err = queries.FailJob(recordContext, db.FailJobParams{
				ID:             job.ID,
				LastError:      &message,
				MaxAttempts:    maxAttempts,
				RetryInSeconds: int32(retryDelay(job.AttemptCount + 1).Seconds()),
			})
		}
		if err != nil {
			log.Printf("worker: recording %s job %s: %v", job.Kind, job.ID, err)
			return
		}
	}
}

// refreshCounts copies Listmonk's subscriber count of every list we know,
// so the dashboard's "In Listmonk" reflects pushes right away instead of
// at the next list sync. Failures are only logged.
func (w Worker) refreshCounts(ctx context.Context) {
	lists, err := w.Listmonk.ListLists(ctx)
	if err != nil {
		log.Printf("worker: refreshing listmonk counts: %v", err)
		return
	}
	queries := db.New(w.Pool)
	for _, list := range lists {
		if err := queries.SetListmonkSubscriberCount(ctx, db.SetListmonkSubscriberCountParams{
			ListmonkID: int32(list.ID), ListmonkSubscriberCount: int32(list.SubscriberCount),
		}); err != nil {
			log.Printf("worker: refreshing listmonk count of list %d: %v", list.ID, err)
			return
		}
	}
}

// retryDelay is the wait after the attempt-th failure: 30s, doubling, at most 1h.
func retryDelay(attempt int32) time.Duration {
	delay := firstRetry
	for range attempt - 1 {
		if delay *= 2; delay >= maxRetry {
			return maxRetry
		}
	}
	return delay
}

func (w Worker) run(ctx context.Context, job db.ProviderJob) error {
	switch job.Kind {
	case "delete_subscriber":
		return w.Listmonk.DeleteSubscriber(ctx, int(*job.ListmonkSubscriberID))
	case "push_subscriber":
		return w.push(ctx, job.SubscriberID)
	}
	return fmt.Errorf("unknown job kind %q", job.Kind)
}

// push makes Listmonk match our DB for one subscriber. It only touches
// Listmonk lists we have a row for, so it never removes a membership an
// admin just added in the Listmonk UI.
func (w Worker) push(ctx context.Context, subscriberID pgtype.UUID) error {
	queries := db.New(w.Pool)
	subscriber, err := queries.GetSubscriber(ctx, subscriberID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // deleted meanwhile; the delete job takes over
	}
	if err != nil {
		return err
	}

	var current listmonk.Subscriber
	if subscriber.ListmonkSubscriberID == nil {
		rows, err := queries.ListSubscriptions(ctx, subscriberID)
		if err != nil {
			return err
		}
		created, err := w.Listmonk.CreateSubscriber(ctx, subscriber.Email, subscriber.Name, attribs(nil, subscriber), listmonkIDs(rows, "subscribed"))
		if err == nil {
			return w.linkAndImport(ctx, subscriberID, created)
		}
		if !errors.Is(err, listmonk.ErrDuplicateEmail) {
			return err
		}
		// Listmonk already knows the email: link to that subscriber and
		// import its memberships before pushing ours.
		if current, err = w.Listmonk.FindSubscriberByEmail(ctx, subscriber.Email); err != nil {
			return fmt.Errorf("finding duplicate email in listmonk: %w", err)
		}
	} else {
		current, err = w.Listmonk.GetSubscriber(ctx, int(*subscriber.ListmonkSubscriberID))
		if errors.Is(err, listmonk.ErrNotFound) {
			// Deleted in the Listmonk UI: unlink, so the next attempt creates it again.
			if err := queries.LinkListmonkSubscriber(ctx, db.LinkListmonkSubscriberParams{ID: subscriberID}); err != nil {
				return err
			}
			return fmt.Errorf("listmonk subscriber %d no longer exists; unlinked", *subscriber.ListmonkSubscriberID)
		}
		if err != nil {
			return err
		}
	}
	// The person's own opt-outs in Listmonk win over our rows, so a push
	// never resubscribes them (see ImportMemberships).
	if err := w.linkAndImport(ctx, subscriberID, current); err != nil {
		return err
	}

	rows, err := queries.ListSubscriptions(ctx, subscriberID)
	if err != nil {
		return err
	}
	if err := w.Listmonk.UpdateSubscriber(ctx, current, subscriber.Email, subscriber.Name, attribs(current.Attribs, subscriber)); err != nil {
		return err
	}
	inListmonk := map[int]string{}
	for _, list := range current.Lists {
		inListmonk[list.ID] = list.SubscriptionStatus
	}
	var add, unsubscribe, remove []int
	for _, listID := range listmonkIDs(rows, "subscribed") {
		if inListmonk[listID] != "confirmed" && current.Status != "blocklisted" {
			add = append(add, listID)
		}
	}
	for _, listID := range listmonkIDs(rows, "unsubscribed") {
		if status, ok := inListmonk[listID]; ok && status != "unsubscribed" {
			unsubscribe = append(unsubscribe, listID)
		}
	}
	for _, listID := range listmonkIDs(rows, "removed") {
		if _, ok := inListmonk[listID]; ok {
			remove = append(remove, listID)
		}
	}
	for _, change := range []struct {
		action  listmonk.ListAction
		listIDs []int
	}{{listmonk.ListAdd, add}, {listmonk.ListUnsubscribe, unsubscribe}, {listmonk.ListRemove, remove}} {
		if err := w.Listmonk.ManageLists(ctx, current.ID, change.action, change.listIDs); err != nil {
			return err
		}
	}
	return nil
}

// linkAndImport stores the Listmonk ID and imports the Listmonk subscriber's
// memberships, in one transaction.
func (w Worker) linkAndImport(ctx context.Context, subscriberID pgtype.UUID, current listmonk.Subscriber) error {
	return pgx.BeginFunc(ctx, w.Pool, func(tx pgx.Tx) error {
		queries := db.New(tx)
		listmonkID := int32(current.ID)
		if err := queries.LinkListmonkSubscriber(ctx, db.LinkListmonkSubscriberParams{ID: subscriberID, ListmonkSubscriberID: &listmonkID}); err != nil {
			return err
		}
		_, err := ImportMemberships(ctx, queries, subscriberID, current, nil)
		return err
	})
}

// ImportMemberships applies a Listmonk subscriber's memberships to our rows
// (flow 7 steps 2, 3 and 6): a membership on a list we have but no row for
// becomes a 'manual' row; a Listmonk opt-out (unsubscribed, or a
// blocklisted subscriber) makes our row 'unsubscribed'; listmonk_status is
// stored. Listmonk lists we don't have (yet) are skipped.
//
// reviveChangedBefore adds step 4 for the subscription import: a Listmonk
// membership on a row we have as 'removed' (an admin re-added them in the
// Listmonk UI) makes it 'subscribed', if the row was last changed before
// then (a later removal is newer than current). The worker passes nil, since
// there our unsent removal wins. changes counts rows created or re-statused.
func ImportMemberships(ctx context.Context, queries *db.Queries, subscriberID pgtype.UUID, current listmonk.Subscriber, reviveChangedBefore *time.Time) (changes int, err error) {
	lists, err := queries.ListAllLists(ctx)
	if err != nil {
		return 0, err
	}
	byListmonkID := map[int]pgtype.UUID{}
	for _, list := range lists {
		if list.ListmonkID != nil && !list.DeletedAt.Valid {
			byListmonkID[int(*list.ListmonkID)] = list.ID
		}
	}
	rows, err := queries.ListSubscriptions(ctx, subscriberID)
	if err != nil {
		return 0, err
	}
	ours := map[pgtype.UUID]db.ListSubscriptionsRow{}
	for _, row := range rows {
		ours[row.ListID] = row
	}

	for _, membership := range current.Lists {
		listID, known := byListmonkID[membership.ID]
		if !known {
			continue
		}
		status := "subscribed"
		if membership.SubscriptionStatus == "unsubscribed" || current.Status == "blocklisted" {
			status = "unsubscribed"
		}
		existing, ok := ours[listID]
		switch {
		case !ok:
			_, err = queries.InsertSubscription(ctx, db.InsertSubscriptionParams{SubscriberID: subscriberID, ListID: listID, Status: status, Source: "manual"})
			changes++
		case status == "unsubscribed" && existing.Status != "unsubscribed",
			reviveChangedBefore != nil && status == "subscribed" && existing.Status == "removed" && existing.UpdatedAt.Before(*reviveChangedBefore):
			err = queries.SetSubscriptionStatus(ctx, db.SetSubscriptionStatusParams{SubscriberID: subscriberID, ListID: listID, Status: status})
			changes++
		}
		if err != nil {
			return 0, err
		}
		listmonkStatus := membership.SubscriptionStatus
		if err := queries.SetListmonkStatus(ctx, db.SetListmonkStatusParams{SubscriberID: subscriberID, ListID: listID, ListmonkStatus: &listmonkStatus}); err != nil {
			return 0, err
		}
	}
	return changes, nil
}

// listmonkIDs returns the Listmonk list IDs of rows in status, skipping
// lists without a Listmonk ID or soft-deleted.
func listmonkIDs(rows []db.ListSubscriptionsRow, status string) []int {
	var ids []int
	for _, row := range rows {
		if row.Status == status && row.ListmonkID != nil && !row.ListDeletedAt.Valid {
			ids = append(ids, int(*row.ListmonkID))
		}
	}
	return ids
}

// attribs keeps the Listmonk subscriber's other attributes and sets ours:
// lang and member_number for members, nothing for non-members.
func attribs(current map[string]any, subscriber db.Subscriber) map[string]any {
	merged := maps.Clone(current)
	if merged == nil {
		merged = map[string]any{}
	}
	if subscriber.MemberNumber != nil {
		merged["member_number"] = *subscriber.MemberNumber
		if subscriber.Lang != nil {
			merged["lang"] = *subscriber.Lang
		}
	}
	return merged
}
