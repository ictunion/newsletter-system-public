// Package members decides which lists a member belongs on and applies that
// to their subscription rows.
package members

import (
	"strings"
	"unicode"

	"github.com/jackc/pgx/v5/pgtype"
	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"

	"github.com/kubik/newsletter-system/internal/db"
)

// Member is the member data orca sends (POST/PUT /members, the orca sync).
type Member struct {
	MemberNumber          int32   `json:"member_number"`
	Email                 string  `json:"email"`
	Name                  string  `json:"name"`
	City                  *string `json:"city"`
	Lang                  string  `json:"lang"`
	WorkplaceNewsletterID *int32  `json:"workplace_newsletter_id"`
}

// Subscription sources set by DesiredLists (subscriptions.source).
const (
	SourceRule      = "rule"
	SourceWorkplace = "workplace"
)

// Desired is one list a member should be on, and why.
type Desired struct {
	ListID pgtype.UUID
	Source string      // SourceRule or SourceWorkplace
	RuleID pgtype.UUID // set for SourceRule
}

// DesiredLists returns the lists member belongs on: the list of every
// enabled matching rule (union), plus the list whose listmonk_id is the
// member's workplace_newsletter_id. Deleted lists never count. Each list
// appears once; when several rules, or a rule and the workplace, target the
// same list, the first rule in rules wins.
//
// unmatchedWorkplace is the member's workplace_newsletter_id when no active
// list has it, for the caller to log and report.
func DesiredLists(member Member, rules []db.SubscriptionRule, lists []db.NewsletterList) (desired []Desired, unmatchedWorkplace *int32) {
	active := map[pgtype.UUID]bool{}
	byListmonkID := map[int32]pgtype.UUID{}
	for _, list := range lists {
		if list.DeletedAt.Valid {
			continue
		}
		active[list.ID] = true
		if list.ListmonkID != nil {
			byListmonkID[*list.ListmonkID] = list.ID
		}
	}

	seen := map[pgtype.UUID]bool{}
	add := func(entry Desired) {
		if !seen[entry.ListID] {
			seen[entry.ListID] = true
			desired = append(desired, entry)
		}
	}

	city := ""
	if member.City != nil {
		city = foldCity(*member.City)
	}
	for _, rule := range rules {
		if !rule.Enabled || !active[rule.ListID] || !ruleMatches(rule, city) {
			continue
		}
		add(Desired{ListID: rule.ListID, Source: SourceRule, RuleID: rule.ID})
	}

	if member.WorkplaceNewsletterID != nil {
		if listID, ok := byListmonkID[*member.WorkplaceNewsletterID]; ok {
			add(Desired{ListID: listID, Source: SourceWorkplace})
		} else {
			unmatchedWorkplace = member.WorkplaceNewsletterID
		}
	}
	return desired, unmatchedWorkplace
}

// ruleMatches reports whether rule matches a member with the given folded
// city. Unknown kinds never match.
func ruleMatches(rule db.SubscriptionRule, foldedCity string) bool {
	switch rule.Kind {
	case "all":
		return true
	case "city":
		if rule.CityContains == nil || foldedCity == "" {
			return false
		}
		needle := foldCity(*rule.CityContains)
		return needle != "" && strings.Contains(foldedCity, needle)
	}
	return false
}

// foldCity lowercases s and strips diacritics (NFD, then drop combining
// marks), so "Plzeň", "PLZEN" and "plzen 3" compare equal on "plzen".
func foldCity(s string) string {
	folded, _, err := transform.String(transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn))), s)
	if err != nil {
		folded = s
	}
	return strings.ToLower(strings.TrimSpace(folded))
}
