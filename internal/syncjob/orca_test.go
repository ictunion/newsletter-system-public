package syncjob

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kubik/newsletter-system/internal/db"
	"github.com/kubik/newsletter-system/internal/dbtest"
	"github.com/kubik/newsletter-system/internal/members"
	"github.com/kubik/newsletter-system/internal/orca"
)

// fakeOrca serves *list to the sync token and returns its URL.
func fakeOrca(t *testing.T, list *[]members.Member) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/sync/newsletter/members" || r.Header.Get("Authorization") != "Bearer sync-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(*list)
	}))
	t.Cleanup(server.Close)
	return server.URL + "/"
}

type change struct {
	kind         string
	memberNumber int32
	listmonkID   *int32
	payload      string
}

func pendingChanges(t *testing.T, pool *pgxpool.Pool) []change {
	t.Helper()
	rows, err := pool.Query(context.Background(), `
		SELECT change.kind, change.member_number, list.listmonk_id, change.payload::text
		FROM pending_changes AS change LEFT JOIN newsletter_lists AS list ON list.id = change.list_id
		WHERE change.status = 'pending' ORDER BY change.member_number, change.kind`)
	if err != nil {
		t.Fatal(err)
	}
	var changes []change
	for rows.Next() {
		var c change
		if err := rows.Scan(&c.kind, &c.memberNumber, &c.listmonkID, &c.payload); err != nil {
			t.Fatal(err)
		}
		changes = append(changes, c)
	}
	return changes
}

func summary(changes []change) []string {
	var out []string
	for _, c := range changes {
		s := fmt.Sprintf("%s %d", c.kind, c.memberNumber)
		if c.listmonkID != nil {
			s += fmt.Sprintf(" list%d", *c.listmonkID)
		}
		out = append(out, s)
	}
	return out
}

func TestRunOrcaReconciliation(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Pool(t)
	queries := db.New(pool)
	for _, id := range []int32{1, 3} { // list 1: rule "all"; list 3: a workplace
		if err := queries.UpsertListmonkList(ctx, db.UpsertListmonkListParams{Name: "list", ListmonkID: id}); err != nil {
			t.Fatal(err)
		}
	}
	lists, _ := queries.ListAllLists(ctx)
	listID := map[int32]pgtype.UUID{}
	for _, list := range lists {
		listID[*list.ListmonkID] = list.ID
	}
	person := func(number int32, city string, workplace *int32) members.Member {
		return members.Member{MemberNumber: number, Email: string(rune('a'+number)) + "@example.com", Name: "F L",
			City: &city, Lang: "cs", WorkplaceNewsletterID: workplace}
	}
	create := func(m members.Member) members.Result {
		t.Helper()
		result, err := members.Service{}.Create(ctx, queries, m)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}

	// Before the rule exists: 5 gets only its workplace row; 6 opted out of list 1.
	create(person(5, "Praha", new(int32(3))))
	six := create(person(6, "Praha", nil))
	if _, err := queries.InsertSubscription(ctx, db.InsertSubscriptionParams{SubscriberID: six.Subscriber.ID, ListID: listID[1], Status: "unsubscribed", Source: "rule"}); err != nil {
		t.Fatal(err)
	}
	if _, err := queries.CreateRule(ctx, db.CreateRuleParams{Kind: "all", ListID: listID[1], Enabled: true}); err != nil {
		t.Fatal(err)
	}
	for _, number := range []int32{1, 2, 3} {
		create(person(number, "Praha", nil))
	}

	orcaMembers := []members.Member{
		person(1, " Praha ", nil), // same after trimming
		person(2, "Brno", nil),    // city changed
		person(4, "Praha", nil),   // new
		person(5, "Praha", nil),   // workplace left, rule list missing
		person(6, "Praha", nil),   // opted out: no change proposed
		{MemberNumber: 7, Email: "not an email", Name: "X Y", Lang: "cs"}, // invalid: skipped
	}
	client := orca.New(fakeOrca(t, &orcaMembers), "sync-token")
	run := func() {
		t.Helper()
		if err := RunOrcaReconciliation(ctx, pool, client); err != nil {
			t.Fatal(err)
		}
	}

	run()
	want := []string{"member_updated 2", "member_removed 3", "member_new 4", "subscription_add 5 list1", "subscription_remove 5 list3"}
	if got := summary(pendingChanges(t, pool)); !slices.Equal(got, want) {
		t.Fatalf("changes = %v, want %v", got, want)
	}
	changes := pendingChanges(t, pool)
	if !strings.Contains(changes[2].payload, `"member_number": 4`) || !strings.Contains(changes[3].payload, `"source": "rule"`) ||
		!strings.Contains(changes[4].payload, `"source": "workplace"`) || !strings.Contains(changes[1].payload, `"email": "d@example.com"`) {
		t.Errorf("payloads = %+v", changes)
	}
	if r := lastRun(t, pool); r.status != "succeeded" || r.fetched != 6 || r.changes != 5 {
		t.Errorf("run = %+v", r)
	}

	// Re-detection refreshes the payload instead of adding a row.
	orcaMembers[2].Name = "F Nová"
	run()
	changes = pendingChanges(t, pool)
	if len(changes) != 5 || !strings.Contains(changes[2].payload, "Nová") {
		t.Errorf("after re-detection: %+v", changes)
	}

	// A rejected change with the same payload is not raised again; a new payload is.
	if _, err := pool.Exec(ctx, `UPDATE pending_changes SET status = 'rejected', decided_at = now(), decided_by = 'admin' WHERE kind = 'member_new'`); err != nil {
		t.Fatal(err)
	}
	run()
	if got := summary(pendingChanges(t, pool)); slices.Contains(got, "member_new 4") {
		t.Errorf("rejected change raised again: %v", got)
	}
	orcaMembers[2].Lang = "en"
	run()
	if got := summary(pendingChanges(t, pool)); !slices.Contains(got, "member_new 4") {
		t.Errorf("changed payload not raised: %v", got)
	}

	// A drift fixed meanwhile (orca's PUT arrived) drops its pending change.
	if _, err := (members.Service{}).Update(ctx, queries, person(2, "Brno", nil)); err != nil {
		t.Fatal(err)
	}
	run()
	if got := summary(pendingChanges(t, pool)); slices.Contains(got, "member_updated 2") {
		t.Errorf("stale change kept: %v", got)
	}
}

func TestRunOrcaReconciliation_Unauthorized(t *testing.T) {
	pool := dbtest.Pool(t)
	none := []members.Member{}
	wrong := orca.New(fakeOrca(t, &none), "wrong")
	if err := RunOrcaReconciliation(context.Background(), pool, wrong); err == nil {
		t.Fatal("want error for a rejected token")
	}
	if r := lastRun(t, pool); r.status != "failed" {
		t.Errorf("run = %+v", r)
	}
}
