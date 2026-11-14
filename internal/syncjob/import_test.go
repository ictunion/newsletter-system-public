package syncjob

import (
	"context"
	"maps"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/kubik/newsletter-system/internal/db"
	"github.com/kubik/newsletter-system/internal/dbtest"
	"github.com/kubik/newsletter-system/internal/listmonk"
	"github.com/kubik/newsletter-system/internal/listmonktest"
	"github.com/kubik/newsletter-system/internal/members"
)

func TestRunListmonkSubscriptionImport(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Pool(t)
	queries := db.New(pool)
	fake, client := listmonktest.New(t)
	lists := map[int32]pgtype.UUID{}
	for _, id := range []int32{1, 2, 3} {
		if err := queries.UpsertListmonkList(ctx, db.UpsertListmonkListParams{Name: "list", ListmonkID: id}); err != nil {
			t.Fatal(err)
		}
		fake.Lists = append(fake.Lists, listmonk.List{ID: int(id)})
	}
	all, _ := queries.ListAllLists(ctx)
	for _, list := range all {
		lists[*list.ListmonkID] = list.ID
	}

	// member creates a member with our rows (Listmonk ID -> status), linked
	// to a Listmonk subscriber with memberships unless listmonk is nil.
	member := func(number int32, email string, ours map[int32]string, listmonkStatus string, listmonkLists map[int]string) pgtype.UUID {
		t.Helper()
		result, err := members.Service{}.Create(ctx, queries, members.Member{MemberNumber: number, Email: email, Name: "A B", Lang: "cs"})
		if err != nil {
			t.Fatal(err)
		}
		id := result.Subscriber.ID
		for list, status := range ours {
			if _, err := queries.InsertSubscription(ctx, db.InsertSubscriptionParams{SubscriberID: id, ListID: lists[list], Status: status, Source: "rule"}); err != nil {
				t.Fatal(err)
			}
		}
		if listmonkLists != nil {
			listmonkID := int32(fake.AddSubscriber(email, "A B", listmonkStatus, listmonkLists))
			if err := queries.LinkListmonkSubscriber(ctx, db.LinkListmonkSubscriberParams{ID: id, ListmonkSubscriberID: &listmonkID}); err != nil {
				t.Fatal(err)
			}
		}
		return id
	}
	states := func(id pgtype.UUID) map[int32]string {
		t.Helper()
		rows, err := queries.ListSubscriptions(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		got := map[int32]string{}
		for _, row := range rows {
			got[*row.ListmonkID] = row.Source + "/" + row.Status
		}
		return got
	}

	uiAdd := member(1, "ui-add@example.com", map[int32]string{1: "subscribed"}, "enabled", map[int]string{1: "confirmed", 2: "confirmed", 99: "confirmed"})
	optOut := member(2, "opt-out@example.com", map[int32]string{1: "subscribed"}, "enabled", map[int]string{1: "unsubscribed"})
	reAdd := member(3, "re-add@example.com", map[int32]string{1: "removed"}, "enabled", map[int]string{1: "unconfirmed"})
	removal := member(4, "removal@example.com", map[int32]string{1: "subscribed", 3: "subscribed"}, "enabled", map[int]string{1: "confirmed"})
	blocklisted := member(5, "blocked@example.com", map[int32]string{1: "subscribed", 2: "subscribed"}, "blocklisted", map[int]string{1: "unsubscribed", 2: "unsubscribed"})
	pending := member(6, "pending@example.com", map[int32]string{1: "subscribed", 3: "subscribed"}, "enabled", map[int]string{1: "unsubscribed"})
	if err := queries.EnqueuePushSubscriber(ctx, pending); err != nil {
		t.Fatal(err)
	}
	unlinked := member(7, "unlinked@example.com", map[int32]string{1: "subscribed"}, "", nil)
	unlinkedListmonkID := fake.AddSubscriber("Unlinked@Example.com", "U", "enabled", map[int]string{1: "confirmed", 2: "unsubscribed"})
	fake.AddSubscriber("guest@example.com", "Event Guest", "enabled", map[int]string{2: "confirmed"})
	deletedListmonkID := fake.AddSubscriber("deleted@example.com", "Gone", "enabled", map[int]string{1: "confirmed"})
	if err := queries.EnqueueDeleteSubscriber(ctx, new(int32(deletedListmonkID))); err != nil {
		t.Fatal(err)
	}

	// An admin's add and removal made while the run fetches: the fetched
	// Listmonk state is older, so neither may be undone.
	lateAdd := member(8, "late-add@example.com", map[int32]string{1: "subscribed"}, "enabled", map[int]string{})
	lateRemove := member(9, "late-remove@example.com", map[int32]string{1: "removed"}, "enabled", map[int]string{1: "confirmed"})
	if _, err := pool.Exec(ctx, `UPDATE subscriptions SET updated_at = now() + interval '1 hour' WHERE subscriber_id IN ($1, $2)`, lateAdd, lateRemove); err != nil {
		t.Fatal(err)
	}

	if err := RunListmonkSubscriptionImport(ctx, pool, client); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		id   pgtype.UUID
		want map[int32]string
	}{
		{"Listmonk UI add becomes manual; unknown list 99 ignored", uiAdd, map[int32]string{1: "rule/subscribed", 2: "manual/subscribed"}},
		{"opt-out becomes unsubscribed", optOut, map[int32]string{1: "rule/unsubscribed"}},
		{"re-add in Listmonk revives removed", reAdd, map[int32]string{1: "rule/subscribed"}},
		{"missing in Listmonk becomes removed", removal, map[int32]string{1: "rule/subscribed", 3: "rule/removed"}},
		{"blocklisted becomes unsubscribed", blocklisted, map[int32]string{1: "rule/unsubscribed", 2: "rule/unsubscribed"}},
		{"pending push skipped", pending, map[int32]string{1: "rule/subscribed", 3: "rule/subscribed"}},
		{"unlinked member matched by email", unlinked, map[int32]string{1: "rule/subscribed", 2: "manual/unsubscribed"}},
		{"add after the fetch began is not removed", lateAdd, map[int32]string{1: "rule/subscribed"}},
		{"removal after the fetch began is not revived", lateRemove, map[int32]string{1: "rule/removed"}},
	}
	for _, tt := range tests {
		if got := states(tt.id); !maps.Equal(got, tt.want) {
			t.Errorf("%s: rows = %v, want %v", tt.name, got, tt.want)
		}
	}

	if subscriber, err := queries.GetSubscriber(ctx, unlinked); err != nil || *subscriber.ListmonkSubscriberID != int32(unlinkedListmonkID) {
		t.Errorf("unlinked member not linked: %+v, %v", subscriber, err)
	}
	guest, err := queries.GetSubscriberByEmail(ctx, "guest@example.com")
	if err != nil || guest.MemberNumber != nil || guest.Name != "Event Guest" || guest.ListmonkSubscriberID == nil {
		t.Fatalf("guest = %+v, err = %v", guest, err)
	}
	if got := states(guest.ID); !maps.Equal(got, map[int32]string{2: "manual/subscribed"}) {
		t.Errorf("guest rows = %v", got)
	}
	if _, err := queries.GetSubscriberByEmail(ctx, "deleted@example.com"); err == nil {
		t.Error("subscriber with a delete job was recreated")
	}
	rows, _ := queries.ListSubscriptions(ctx, uiAdd)
	for _, row := range rows {
		if row.ListmonkStatus == nil || *row.ListmonkStatus != "confirmed" {
			t.Errorf("listmonk_status on list %d = %v, want confirmed", *row.ListmonkID, row.ListmonkStatus)
		}
	}
	if jobs := count(t, pool, `SELECT count(*) FROM provider_jobs WHERE kind = 'push_subscriber'`); jobs != 1 {
		t.Errorf("push jobs = %d, want only the pre-existing one", jobs)
	}
	if run := lastRun(t, pool); run.status != "succeeded" || run.fetched != 10 || run.changes == 0 {
		t.Errorf("run = %+v", run)
	}

	// A second run changes nothing.
	if err := RunListmonkSubscriptionImport(ctx, pool, client); err != nil {
		t.Fatal(err)
	}
	if run := lastRun(t, pool); run.changes != 0 {
		t.Errorf("second run changes = %d, want 0", run.changes)
	}
}
