package syncjob

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kubik/newsletter-system/internal/db"
	"github.com/kubik/newsletter-system/internal/members"
	"github.com/kubik/newsletter-system/internal/orca"
)

const KindOrcaMembers Kind = "orca_members"

// Pending change kinds: list_removed is raised by RunListmonkListSync, the
// rest by RunOrcaReconciliation.
const (
	ChangeListRemoved        = "list_removed"
	ChangeMemberNew          = "member_new"
	ChangeMemberUpdated      = "member_updated"
	ChangeMemberRemoved      = "member_removed"
	ChangeSubscriptionAdd    = "subscription_add"
	ChangeSubscriptionRemove = "subscription_remove"
)

// SubscriptionChange is the payload of subscription_add and subscription_remove.
type SubscriptionChange struct {
	Source string      `json:"source"`
	RuleID pgtype.UUID `json:"rule_id"` // subscription_add from a rule only
}

// RemovedMember is the payload of member_removed, for display.
type RemovedMember struct {
	Email string `json:"email"`
	Name  string `json:"name"`
}

// RunOrcaReconciliation diffs orca's active members against ours and raises
// pending changes for an admin to approve (flow 8). It never changes members
// or subscriptions itself:
//   - member_new: in orca, not here (payload: orca's member).
//   - member_updated: stored data differs (payload: orca's member).
//   - subscription_add / subscription_remove: data matches, but DesiredLists
//     differs from the rule and workplace rows, exactly where ApplyMember
//     would change a row.
//   - member_removed: here, not in orca.
//
// Pending member and subscription changes this run no longer detects are
// deleted, since the drift is gone.
func RunOrcaReconciliation(ctx context.Context, pool *pgxpool.Pool, client *orca.Client) error {
	return record(ctx, pool, KindOrcaMembers, func(ctx context.Context) (int, int, error) {
		orcaMembers, err := client.ListMembers(ctx)
		if err != nil {
			return 0, 0, err
		}
		changes := 0
		err = pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
			queries := db.New(tx)
			raise := func(kind string, memberNumber int32, listID pgtype.UUID, payload any) error {
				encoded, err := json.Marshal(payload)
				if err != nil {
					return err
				}
				raised, err := queries.RaisePendingChange(ctx, db.RaisePendingChangeParams{
					Kind: kind, MemberNumber: &memberNumber, ListID: listID, Payload: encoded,
				})
				if err != nil {
					return fmt.Errorf("raising %s for member %d: %w", kind, memberNumber, err)
				}
				changes += int(raised)
				return nil
			}

			subscribers, err := queries.ListMembers(ctx)
			if err != nil {
				return err
			}
			ours := map[int32]db.Subscriber{}
			for _, subscriber := range subscribers {
				ours[*subscriber.MemberNumber] = subscriber
			}
			rows, err := queries.ListMemberSubscriptions(ctx)
			if err != nil {
				return err
			}
			subscriptions := map[pgtype.UUID][]db.Subscription{}
			for _, row := range rows {
				subscriptions[row.SubscriberID] = append(subscriptions[row.SubscriberID], row)
			}
			rules, err := queries.ListRules(ctx)
			if err != nil {
				return err
			}
			lists, err := queries.ListAllLists(ctx)
			if err != nil {
				return err
			}

			inOrca := map[int32]bool{}
			for _, raw := range orcaMembers {
				inOrca[raw.MemberNumber] = true
				member, err := members.Normalize(raw)
				if err != nil {
					log.Printf("sync: orca member %d skipped: %v", raw.MemberNumber, err)
					continue
				}
				subscriber, exists := ours[member.MemberNumber]
				switch {
				case !exists:
					err = raise(ChangeMemberNew, member.MemberNumber, pgtype.UUID{}, member)
				case !members.SameData(subscriber, member):
					err = raise(ChangeMemberUpdated, member.MemberNumber, pgtype.UUID{}, member)
				default:
					err = raiseSubscriptionDrift(raise, member, subscriptions[subscriber.ID], rules, lists)
				}
				if err != nil {
					return err
				}
			}
			for memberNumber, subscriber := range ours {
				if !inOrca[memberNumber] {
					if err := raise(ChangeMemberRemoved, memberNumber, pgtype.UUID{}, RemovedMember{Email: subscriber.Email, Name: subscriber.Name}); err != nil {
						return err
					}
				}
			}
			_, err = queries.DeleteUndetectedMemberChanges(ctx)
			return err
		})
		return len(orcaMembers), changes, err
	})
}

// raiseSubscriptionDrift raises a change for each row ApplyMember would
// change: a desired list with no row, and a subscribed rule or workplace
// row no longer desired. Every other row (unsubscribed, removed, manual,
// self) is never proposed.
func raiseSubscriptionDrift(raise func(string, int32, pgtype.UUID, any) error, member members.Member,
	rows []db.Subscription, rules []db.SubscriptionRule, lists []db.NewsletterList) error {
	existing := map[pgtype.UUID]db.Subscription{}
	for _, row := range rows {
		existing[row.ListID] = row
	}
	desired, _ := members.DesiredLists(member, rules, lists)
	wanted := map[pgtype.UUID]bool{}
	for _, entry := range desired {
		wanted[entry.ListID] = true
		if _, ok := existing[entry.ListID]; !ok {
			if err := raise(ChangeSubscriptionAdd, member.MemberNumber, entry.ListID, SubscriptionChange{Source: entry.Source, RuleID: entry.RuleID}); err != nil {
				return err
			}
		}
	}
	for _, row := range rows {
		automatic := row.Source == members.SourceRule || row.Source == members.SourceWorkplace
		if automatic && row.Status == "subscribed" && !wanted[row.ListID] {
			if err := raise(ChangeSubscriptionRemove, member.MemberNumber, row.ListID, SubscriptionChange{Source: row.Source}); err != nil {
				return err
			}
		}
	}
	return nil
}
