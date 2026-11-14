package members

import (
	"context"
	"errors"
	"maps"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kubik/newsletter-system/internal/db"
	"github.com/kubik/newsletter-system/internal/dbtest"
)

// createLists makes one list per listmonk ID and returns their IDs by listmonk ID.
func createLists(t *testing.T, queries *db.Queries, listmonkIDs ...int32) map[int32]pgtype.UUID {
	t.Helper()
	ctx := context.Background()
	for _, id := range listmonkIDs {
		if err := queries.UpsertListmonkList(ctx, db.UpsertListmonkListParams{Name: "list", ListmonkID: id}); err != nil {
			t.Fatal(err)
		}
	}
	lists, err := queries.ListAllLists(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ids := map[int32]pgtype.UUID{}
	for _, list := range lists {
		ids[*list.ListmonkID] = list.ID
	}
	return ids
}

func subscriptionStates(t *testing.T, queries *db.Queries, subscriberID pgtype.UUID) map[pgtype.UUID]string {
	t.Helper()
	rows, err := queries.ListSubscriptions(context.Background(), subscriberID)
	if err != nil {
		t.Fatal(err)
	}
	states := map[pgtype.UUID]string{}
	for _, row := range rows {
		states[row.ListID] = row.Source + "/" + row.Status
	}
	return states
}

func jobCount(t *testing.T, pool *pgxpool.Pool, kind string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM provider_jobs WHERE kind = $1`, kind).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func testMember(workplace *int32) Member {
	return Member{MemberNumber: 834, Email: "jan@example.com", Name: "Jan Novák", City: ptr("Praha 4"), Lang: "cs", WorkplaceNewsletterID: workplace}
}

func TestApplyMember_WorkplaceMove(t *testing.T) {
	ctx := context.Background()
	queries := dbtest.Tx(t)
	lists := createLists(t, queries, 13, 14)
	result, err := Service{}.Create(ctx, queries, testMember(ptr[int32](13)))
	if err != nil {
		t.Fatal(err)
	}
	id := result.Subscriber.ID

	changed, unmatched, err := ApplyMember(ctx, queries, id, testMember(ptr[int32](14)))
	if err != nil || !changed || unmatched != nil {
		t.Fatalf("changed = %v, unmatched = %v, err = %v", changed, unmatched, err)
	}
	want := map[pgtype.UUID]string{lists[13]: "workplace/removed", lists[14]: "workplace/subscribed"}
	if got := subscriptionStates(t, queries, id); !maps.Equal(got, want) {
		t.Errorf("states = %v, want %v", got, want)
	}

	// Moving back does not revive the removed row.
	if _, _, err := ApplyMember(ctx, queries, id, testMember(ptr[int32](13))); err != nil {
		t.Fatal(err)
	}
	want = map[pgtype.UUID]string{lists[13]: "workplace/removed", lists[14]: "workplace/removed"}
	if got := subscriptionStates(t, queries, id); !maps.Equal(got, want) {
		t.Errorf("after moving back: states = %v, want %v", got, want)
	}
}

func TestApplyMember_LeavesNonAutomaticRowsAlone(t *testing.T) {
	ctx := context.Background()
	queries := dbtest.Tx(t)
	lists := createLists(t, queries, 1, 2, 3)
	for _, listmonkID := range []int32{1, 2} {
		if _, err := queries.CreateRule(ctx, db.CreateRuleParams{Kind: "all", ListID: lists[listmonkID], Enabled: true}); err != nil {
			t.Fatal(err)
		}
	}
	result, err := Service{}.Create(ctx, queries, testMember(nil))
	if err != nil {
		t.Fatal(err)
	}
	id := result.Subscriber.ID
	// List 1: the member opted out. List 2: an admin removed them. List 3: an admin added them.
	set := func(list pgtype.UUID, status string) {
		if err := queries.SetSubscriptionStatus(ctx, db.SetSubscriptionStatusParams{SubscriberID: id, ListID: list, Status: status}); err != nil {
			t.Fatal(err)
		}
	}
	set(lists[1], "unsubscribed")
	set(lists[2], "removed")
	if _, err := queries.InsertSubscription(ctx, db.InsertSubscriptionParams{SubscriberID: id, ListID: lists[3], Status: "subscribed", Source: "manual"}); err != nil {
		t.Fatal(err)
	}
	want := subscriptionStates(t, queries, id)

	changed, _, err := ApplyMember(ctx, queries, id, testMember(nil))
	if err != nil || changed {
		t.Fatalf("changed = %v, err = %v, want no change", changed, err)
	}
	if got := subscriptionStates(t, queries, id); !maps.Equal(got, want) {
		t.Errorf("states = %v, want %v", got, want)
	}
}

func TestService_PushOnlyWhenChanged(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Pool(t)
	queries := db.New(pool)
	createLists(t, queries, 13, 14)
	service := Service{PushEnabled: true}
	clearJobs := func() {
		if _, err := pool.Exec(ctx, `DELETE FROM provider_jobs`); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := service.Create(ctx, queries, testMember(ptr[int32](13))); err != nil {
		t.Fatal(err)
	}
	if n := jobCount(t, pool, "push_subscriber"); n != 1 {
		t.Errorf("after create: push jobs = %d, want 1", n)
	}
	clearJobs()

	steps := []struct {
		name     string
		member   Member
		wantPush int
	}{
		{"same data", testMember(ptr[int32](13)), 0},
		{"workplace moved", testMember(ptr[int32](14)), 1},
		{"email changed", func() Member { m := testMember(ptr[int32](14)); m.Email = "novak@example.com"; return m }(), 1},
		{"same again", func() Member { m := testMember(ptr[int32](14)); m.Email = "novak@example.com"; return m }(), 0},
	}
	for _, step := range steps {
		if _, err := service.Update(ctx, queries, step.member); err != nil {
			t.Fatalf("%s: %v", step.name, err)
		}
		if n := jobCount(t, pool, "push_subscriber"); n != step.wantPush {
			t.Errorf("%s: push jobs = %d, want %d", step.name, n, step.wantPush)
		}
		clearJobs()
	}

	// Without Listmonk nothing is enqueued.
	if _, err := (Service{}).Update(ctx, queries, testMember(ptr[int32](13))); err != nil {
		t.Fatal(err)
	}
	if n := jobCount(t, pool, "push_subscriber"); n != 0 {
		t.Errorf("push disabled: push jobs = %d, want 0", n)
	}
}

func TestService_CreateAttachesAndConflicts(t *testing.T) {
	ctx := context.Background()
	queries := dbtest.Tx(t)

	// A non-member with the same email (any case) becomes the member.
	guest, err := queries.CreateMember(ctx, db.CreateMemberParams{Email: "JAN@example.com", Name: "Guest"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := Service{}.Create(ctx, queries, testMember(nil))
	if err != nil || !result.Created || result.Subscriber.ID != guest.ID || *result.Subscriber.MemberNumber != 834 || result.Subscriber.Name != "Jan Novák" {
		t.Fatalf("attach: result = %+v, err = %v", result, err)
	}

	// The same member_number again changes nothing.
	again := testMember(nil)
	again.Email = "other@example.com"
	result, err = Service{}.Create(ctx, queries, again)
	if err != nil || result.Created || result.Subscriber.Email != "jan@example.com" {
		t.Errorf("repeat: result = %+v, err = %v", result, err)
	}

	// Another member with that email conflicts, on create and on update.
	other := testMember(nil)
	other.MemberNumber, other.Email = 835, "eva@example.com"
	if _, err := (Service{}).Create(ctx, queries, other); err != nil {
		t.Fatal(err)
	}
	other.Email = "Jan@Example.com"
	if _, err := (Service{}).Update(ctx, queries, other); !errors.Is(err, ErrEmailTaken) {
		t.Errorf("update to taken email: err = %v, want ErrEmailTaken", err)
	}
}

func TestService_UpdateUnknown(t *testing.T) {
	if _, err := (Service{}).Update(context.Background(), dbtest.Tx(t), testMember(nil)); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}
