package members

import (
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/kubik/newsletter-system/internal/db"
)

func uuid(b byte) pgtype.UUID { return pgtype.UUID{Bytes: [16]byte{b}, Valid: true} }
func ptr[T any](v T) *T       { return &v }

var (
	listAll     = uuid(1)
	listPraha   = uuid(2)
	listBrno    = uuid(3)
	listWork    = uuid(4)
	listDeleted = uuid(5)

	testLists = []db.NewsletterList{
		{ID: listAll, ListmonkID: ptr[int32](10)},
		{ID: listPraha, ListmonkID: ptr[int32](11)},
		{ID: listBrno, ListmonkID: ptr[int32](12)},
		{ID: listWork, ListmonkID: ptr[int32](13)},
		{ID: listDeleted, ListmonkID: ptr[int32](14), DeletedAt: pgtype.Timestamptz{Valid: true}},
	}

	ruleAll      = db.SubscriptionRule{ID: uuid(101), Kind: "all", ListID: listAll, Enabled: true}
	rulePraha    = db.SubscriptionRule{ID: uuid(102), Kind: "city", ListID: listPraha, CityContains: ptr("praha"), Enabled: true}
	ruleBrno     = db.SubscriptionRule{ID: uuid(103), Kind: "city", ListID: listBrno, CityContains: ptr("Brno"), Enabled: true}
	rulePlzen    = db.SubscriptionRule{ID: uuid(104), Kind: "city", ListID: listBrno, CityContains: ptr("plzen"), Enabled: true}
	ruleDisabled = db.SubscriptionRule{ID: uuid(105), Kind: "all", ListID: listPraha, Enabled: false}
	ruleOnDelete = db.SubscriptionRule{ID: uuid(106), Kind: "all", ListID: listDeleted, Enabled: true}
	ruleBlank    = db.SubscriptionRule{ID: uuid(107), Kind: "city", ListID: listPraha, CityContains: ptr("  "), Enabled: true}
	ruleWorkList = db.SubscriptionRule{ID: uuid(108), Kind: "city", ListID: listWork, CityContains: ptr("ostrava"), Enabled: true}
)

func rule(r db.SubscriptionRule) Desired {
	return Desired{ListID: r.ListID, Source: SourceRule, RuleID: r.ID}
}

func TestDesiredLists(t *testing.T) {
	tests := []struct {
		name          string
		city          *string
		workplace     *int32
		rules         []db.SubscriptionRule
		want          []Desired
		wantUnmatched *int32
	}{
		{"no rules, no workplace", ptr("Praha"), nil, nil, nil, nil},
		{"union of matching rules", ptr("Praha 4"), nil, []db.SubscriptionRule{ruleAll, rulePraha, ruleBrno},
			[]Desired{rule(ruleAll), rule(rulePraha)}, nil},
		{"case insensitive", ptr("PRAHA"), nil, []db.SubscriptionRule{rulePraha, ruleBrno},
			[]Desired{rule(rulePraha)}, nil},
		{"rule value case insensitive", ptr("brno-sever"), nil, []db.SubscriptionRule{ruleBrno},
			[]Desired{rule(ruleBrno)}, nil},
		{"accent insensitive", ptr("Plzeň 3"), nil, []db.SubscriptionRule{rulePlzen},
			[]Desired{rule(rulePlzen)}, nil},
		{"no city matches only all", nil, nil, []db.SubscriptionRule{ruleAll, rulePraha},
			[]Desired{rule(ruleAll)}, nil},
		{"disabled rule ignored", ptr("Praha"), nil, []db.SubscriptionRule{ruleDisabled}, nil, nil},
		{"blank city rule matches nobody", ptr("Praha"), nil, []db.SubscriptionRule{ruleBlank}, nil, nil},
		{"deleted list ignored", ptr("Praha"), nil, []db.SubscriptionRule{ruleOnDelete}, nil, nil},
		{"workplace by listmonk_id", nil, ptr[int32](13), nil,
			[]Desired{{ListID: listWork, Source: SourceWorkplace}}, nil},
		{"unknown workplace skipped and reported", ptr("Praha"), ptr[int32](99), []db.SubscriptionRule{rulePraha},
			[]Desired{rule(rulePraha)}, ptr[int32](99)},
		{"workplace on deleted list reported", nil, ptr[int32](14), nil, nil, ptr[int32](14)},
		{"two rules on one list give one entry", ptr("Plzeň, Brno"), nil, []db.SubscriptionRule{ruleBrno, rulePlzen},
			[]Desired{rule(ruleBrno)}, nil},
		{"rule wins over workplace on same list", ptr("Ostrava"), ptr[int32](13), []db.SubscriptionRule{ruleWorkList},
			[]Desired{rule(ruleWorkList)}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			member := Member{MemberNumber: 1, City: tt.city, WorkplaceNewsletterID: tt.workplace}
			got, unmatched := DesiredLists(member, tt.rules, testLists)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("desired = %+v, want %+v", got, tt.want)
			}
			if !reflect.DeepEqual(unmatched, tt.wantUnmatched) {
				t.Errorf("unmatched = %v, want %v", unmatched, tt.wantUnmatched)
			}
		})
	}
}
