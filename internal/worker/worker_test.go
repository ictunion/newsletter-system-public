package worker

import (
	"context"
	"maps"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kubik/newsletter-system/internal/db"
	"github.com/kubik/newsletter-system/internal/dbtest"
	"github.com/kubik/newsletter-system/internal/listmonk"
	"github.com/kubik/newsletter-system/internal/listmonktest"
	"github.com/kubik/newsletter-system/internal/members"
)

type fixture struct {
	pool    *pgxpool.Pool
	queries *db.Queries
	fake    *listmonktest.Server
	worker  Worker
	lists   map[int32]pgtype.UUID // by Listmonk ID
}

// setup creates our lists with these Listmonk IDs (also known to the fake).
func setup(t *testing.T, listmonkIDs ...int32) fixture {
	t.Helper()
	pool := dbtest.Pool(t)
	fake, client := listmonktest.New(t)
	f := fixture{pool: pool, queries: db.New(pool), fake: fake, worker: Worker{Pool: pool, Listmonk: client}, lists: map[int32]pgtype.UUID{}}
	for _, id := range listmonkIDs {
		if err := f.queries.UpsertListmonkList(context.Background(), db.UpsertListmonkListParams{Name: "list", ListmonkID: id}); err != nil {
			t.Fatal(err)
		}
		fake.Lists = append(fake.Lists, listmonk.List{ID: int(id), Name: "list"})
	}
	lists, err := f.queries.ListAllLists(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, list := range lists {
		f.lists[*list.ListmonkID] = list.ID
	}
	return f
}

func (f fixture) rule(t *testing.T, listmonkID int32) {
	t.Helper()
	if _, err := f.queries.CreateRule(context.Background(), db.CreateRuleParams{Kind: "all", ListID: f.lists[listmonkID], Enabled: true}); err != nil {
		t.Fatal(err)
	}
}

func (f fixture) createMember(t *testing.T, email string) db.Subscriber {
	t.Helper()
	member := members.Member{MemberNumber: 834, Email: email, FirstName: "Jan", LastName: "Novák", Lang: "cs"}
	result, err := members.Service{PushEnabled: true}.Create(context.Background(), f.queries, member)
	if err != nil {
		t.Fatal(err)
	}
	return result.Subscriber
}

// states returns our rows as Listmonk list ID -> status.
func (f fixture) states(t *testing.T, subscriberID pgtype.UUID) map[int]string {
	t.Helper()
	rows, err := f.queries.ListSubscriptions(context.Background(), subscriberID)
	if err != nil {
		t.Fatal(err)
	}
	states := map[int]string{}
	for _, row := range rows {
		states[int(*row.ListmonkID)] = row.Status
	}
	return states
}

func (f fixture) listmonkID(t *testing.T, subscriberID pgtype.UUID) int {
	t.Helper()
	subscriber, err := f.queries.GetSubscriber(context.Background(), subscriberID)
	if err != nil || subscriber.ListmonkSubscriberID == nil {
		t.Fatalf("subscriber not linked: %+v, %v", subscriber, err)
	}
	return int(*subscriber.ListmonkSubscriberID)
}

func (f fixture) jobs(t *testing.T) []db.ProviderJob {
	t.Helper()
	rows, err := f.pool.Query(context.Background(), `SELECT * FROM provider_jobs`)
	if err != nil {
		t.Fatal(err)
	}
	var jobs []db.ProviderJob
	for rows.Next() {
		var job db.ProviderJob
		if err := rows.Scan(&job.ID, &job.Kind, &job.SubscriberID, &job.ListmonkSubscriberID, &job.Status, &job.AttemptCount,
			&job.NextAttemptAt, &job.LastError, &job.CreatedAt, &job.UpdatedAt); err != nil {
			t.Fatal(err)
		}
		jobs = append(jobs, job)
	}
	return jobs
}

func (f fixture) setRow(t *testing.T, subscriberID pgtype.UUID, listmonkID int32, status string) {
	t.Helper()
	if err := f.queries.SetSubscriptionStatus(context.Background(), db.SetSubscriptionStatusParams{
		SubscriberID: subscriberID, ListID: f.lists[listmonkID], Status: status,
	}); err != nil {
		t.Fatal(err)
	}
}

func (f fixture) push(t *testing.T, subscriberID pgtype.UUID) {
	t.Helper()
	if err := f.queries.EnqueuePushSubscriber(context.Background(), subscriberID); err != nil {
		t.Fatal(err)
	}
	f.worker.RunDue(context.Background())
}

func TestPush_Create(t *testing.T) {
	f := setup(t, 1, 2)
	f.rule(t, 1)
	subscriber := f.createMember(t, "Jan@Example.com")

	f.worker.RunDue(context.Background())

	id := f.listmonkID(t, subscriber.ID)
	if got := f.fake.Memberships(id); !maps.Equal(got, map[int]string{1: "confirmed"}) {
		t.Errorf("listmonk memberships = %v", got)
	}
	created := f.fake.Subscribers[id]
	if created.Email != "jan@example.com" || created.Name != "Jan Novák" || created.Attribs["member_number"] != 834.0 || created.Attribs["lang"] != "cs" {
		t.Errorf("listmonk subscriber = %+v", created)
	}
	rows, _ := f.queries.ListSubscriptions(context.Background(), subscriber.ID)
	if *rows[0].ListmonkStatus != "confirmed" {
		t.Errorf("listmonk_status = %v", rows[0].ListmonkStatus)
	}
	if jobs := f.jobs(t); len(jobs) != 0 {
		t.Errorf("jobs left = %+v", jobs)
	}
}

// Linking to an existing Listmonk subscriber keeps their opt-outs, imports
// memberships we lack, and never touches Listmonk lists we don't know.
func TestPush_LinkKeepsOptOuts(t *testing.T) {
	f := setup(t, 1, 2, 3)
	f.rule(t, 1)
	f.rule(t, 3)
	existing := f.fake.AddSubscriber("jan@example.com", "Jan", "enabled", map[int]string{1: "unsubscribed", 2: "confirmed", 99: "confirmed"})
	f.fake.Subscribers[existing].Attribs["source"] = "event"
	subscriber := f.createMember(t, "jan@example.com")

	f.worker.RunDue(context.Background())

	if id := f.listmonkID(t, subscriber.ID); id != existing {
		t.Fatalf("linked to %d, want %d", id, existing)
	}
	if got, want := f.states(t, subscriber.ID), map[int]string{1: "unsubscribed", 2: "subscribed", 3: "subscribed"}; !maps.Equal(got, want) {
		t.Errorf("our rows = %v, want %v", got, want)
	}
	if got, want := f.fake.Memberships(existing), map[int]string{1: "unsubscribed", 2: "confirmed", 3: "confirmed", 99: "confirmed"}; !maps.Equal(got, want) {
		t.Errorf("listmonk = %v, want %v", got, want)
	}
	if attribs := f.fake.Subscribers[existing].Attribs; attribs["source"] != "event" || attribs["member_number"] != 834.0 {
		t.Errorf("attribs = %v, want Listmonk's kept and ours set", attribs)
	}
	if jobs := f.jobs(t); len(jobs) != 0 {
		t.Errorf("jobs left = %+v", jobs)
	}
}

func TestPush_Linked(t *testing.T) {
	f := setup(t, 1, 2, 3)
	f.rule(t, 1)
	f.rule(t, 2)
	f.rule(t, 3)
	subscriber := f.createMember(t, "jan@example.com")
	f.worker.RunDue(context.Background())
	id := f.listmonkID(t, subscriber.ID)

	// An admin adds list 99 in the Listmonk UI and the person opts out of
	// list 3 there; meanwhile we remove list 2 and change the email.
	f.fake.Mu.Lock()
	lists := f.fake.Subscribers[id].Lists
	lists[slices.IndexFunc(lists, func(l listmonk.SubscriberList) bool { return l.ID == 3 })].SubscriptionStatus = "unsubscribed"
	f.fake.Subscribers[id].Lists = append(lists, listmonk.SubscriberList{ID: 99, SubscriptionStatus: "confirmed"})
	f.fake.Mu.Unlock()
	f.setRow(t, subscriber.ID, 2, "removed")
	if _, err := f.pool.Exec(context.Background(), `UPDATE subscribers SET email = 'novak@example.com'`); err != nil {
		t.Fatal(err)
	}

	f.push(t, subscriber.ID)

	if got, want := f.fake.Memberships(id), map[int]string{1: "confirmed", 3: "unsubscribed", 99: "confirmed"}; !maps.Equal(got, want) {
		t.Errorf("listmonk = %v, want %v", got, want)
	}
	if got, want := f.states(t, subscriber.ID), map[int]string{1: "subscribed", 2: "removed", 3: "unsubscribed"}; !maps.Equal(got, want) {
		t.Errorf("our rows = %v, want %v (opt-out imported, not overridden)", got, want)
	}
	if email := f.fake.Subscribers[id].Email; email != "novak@example.com" {
		t.Errorf("email = %s", email)
	}
}

func TestFailures_BackoffToDead_AndRetryDelays(t *testing.T) {
	f := setup(t, 1)
	f.createMember(t, "jan@example.com")
	f.fake.FailWith = http.StatusInternalServerError

	for attempt := int32(1); attempt <= maxAttempts; attempt++ {
		start := time.Now()
		f.worker.RunDue(context.Background())
		job := f.jobs(t)[0]
		wantStatus := "pending"
		if attempt == maxAttempts {
			wantStatus = "dead"
		}
		if job.AttemptCount != attempt || job.Status != wantStatus || job.LastError == nil {
			t.Fatalf("after attempt %d: job = %+v", attempt, job)
		}
		if delay := job.NextAttemptAt.Sub(start); delay < retryDelay(attempt)-time.Second || delay > retryDelay(attempt)+time.Second {
			t.Errorf("attempt %d: retry in %v, want %v", attempt, delay, retryDelay(attempt))
		}
		if _, err := f.pool.Exec(context.Background(), `UPDATE provider_jobs SET next_attempt_at = now()`); err != nil {
			t.Fatal(err)
		}
	}
	// A dead job is not picked up again.
	f.fake.FailWith = 0
	f.worker.RunDue(context.Background())
	if job := f.jobs(t)[0]; job.Status != "dead" || job.AttemptCount != maxAttempts {
		t.Errorf("dead job ran again: %+v", job)
	}

	want := []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute, 16 * time.Minute, 32 * time.Minute, time.Hour, time.Hour}
	for i, delay := range want {
		if got := retryDelay(int32(i + 1)); got != delay {
			t.Errorf("retryDelay(%d) = %v, want %v", i+1, got, delay)
		}
	}
}

func TestDelete_NotFoundCountsAsSuccess(t *testing.T) {
	f := setup(t)
	if err := f.queries.EnqueueDeleteSubscriber(context.Background(), new(int32(7))); err != nil {
		t.Fatal(err)
	}
	f.fake.FailWith = http.StatusNotFound
	f.worker.RunDue(context.Background())
	if jobs := f.jobs(t); len(jobs) != 0 {
		t.Errorf("jobs left = %+v", jobs)
	}

	id := f.fake.AddSubscriber("a@example.com", "A", "enabled", nil)
	f.fake.FailWith = 0
	if err := f.queries.EnqueueDeleteSubscriber(context.Background(), new(int32(id))); err != nil {
		t.Fatal(err)
	}
	f.worker.RunDue(context.Background())
	if _, ok := f.fake.Subscribers[id]; ok || len(f.jobs(t)) != 0 {
		t.Error("subscriber not deleted in listmonk, or job left")
	}
}

// A push enqueued while the worker runs the same subscriber's job keeps the
// job pending, so the newer change is pushed too.
func TestEnqueueWhileRunning_KeepsJob(t *testing.T) {
	f := setup(t)
	subscriber := f.createMember(t, "jan@example.com")
	ctx := context.Background()
	claimed, err := f.queries.ClaimDueJob(ctx, leaseSeconds)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.queries.EnqueuePushSubscriber(ctx, subscriber.ID); err != nil {
		t.Fatal(err)
	}
	if deleted, err := f.queries.DeleteFinishedJob(ctx, db.DeleteFinishedJobParams{ID: claimed.ID, ClaimedAt: claimed.UpdatedAt}); err != nil || deleted != 0 {
		t.Fatalf("deleted = %d, err = %v, want the re-enqueued job kept", deleted, err)
	}
	if jobs := f.jobs(t); len(jobs) != 1 || jobs[0].NextAttemptAt.After(time.Now()) {
		t.Errorf("jobs = %+v, want one job due now", jobs)
	}

	// Enqueueing replaces a dead push instead of adding a second job.
	if _, err := f.pool.Exec(ctx, `UPDATE provider_jobs SET status = 'dead'`); err != nil {
		t.Fatal(err)
	}
	if err := f.queries.EnqueuePushSubscriber(ctx, subscriber.ID); err != nil {
		t.Fatal(err)
	}
	if jobs := f.jobs(t); len(jobs) != 1 || jobs[0].Status != "pending" {
		t.Errorf("jobs = %+v, want one pending job", jobs)
	}
}
