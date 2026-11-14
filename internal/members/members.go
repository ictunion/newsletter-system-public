package members

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/mail"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/kubik/newsletter-system/internal/db"
)

var (
	// ErrNotFound: no member with that member_number.
	ErrNotFound = errors.New("member not found")
	// ErrEmailTaken: another subscriber already has that email.
	ErrEmailTaken = errors.New("email already used by another subscriber")
	// ErrListNotFound: no such list, or (for AddToList) it is deleted.
	ErrListNotFound = errors.New("list not found")
	// ErrUnsubscribed: the member opted out of the list themselves; only
	// they can undo it.
	ErrUnsubscribed = errors.New("member unsubscribed from the list")
)

// Service runs the member flows (docs/newsletter_service_plan.md, flows 1-3)
// on a transaction-bound *db.Queries. PushEnabled is false when Listmonk is
// not configured: then no provider jobs are enqueued.
type Service struct {
	PushEnabled bool
}

// Result is what a create or update did.
type Result struct {
	Subscriber db.Subscriber
	// Created is false when the member already existed (Create changed nothing).
	Created bool
	// UnmatchedWorkplace is the member's workplace_newsletter_id when no
	// active list has it.
	UnmatchedWorkplace *int32
}

// Create is flow 1. An existing member_number changes nothing. An email held
// by a non-member subscriber attaches the member to that row, keeping its
// subscriptions; an email held by another member is ErrEmailTaken.
func (s Service) Create(ctx context.Context, queries *db.Queries, member Member) (Result, error) {
	existing, err := queries.GetSubscriberByMemberNumber(ctx, &member.MemberNumber)
	if err == nil {
		return Result{Subscriber: existing}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Result{}, err
	}

	var subscriber db.Subscriber
	holder, err := queries.GetSubscriberByEmail(ctx, member.Email)
	switch {
	case err == nil && holder.MemberNumber != nil:
		return Result{}, ErrEmailTaken
	case err == nil:
		subscriber, err = queries.SetMemberFields(ctx, setParams(holder.ID, member))
	case errors.Is(err, pgx.ErrNoRows):
		params := setParams(pgtype.UUID{}, member)
		subscriber, err = queries.CreateMember(ctx, db.CreateMemberParams{
			Email: params.Email, Name: params.Name, MemberNumber: params.MemberNumber,
			City: params.City, Lang: params.Lang,
		})
	}
	if err != nil {
		return Result{}, emailTakenOr(err)
	}

	unmatched, err := s.apply(ctx, queries, subscriber, member, true)
	return Result{Subscriber: subscriber, Created: true, UnmatchedWorkplace: unmatched}, err
}

// Update is flow 2: update the member's fields and subscriptions.
func (s Service) Update(ctx context.Context, queries *db.Queries, member Member) (Result, error) {
	subscriber, err := queries.GetSubscriberByMemberNumber(ctx, &member.MemberNumber)
	if errors.Is(err, pgx.ErrNoRows) {
		return Result{}, ErrNotFound
	}
	if err != nil {
		return Result{}, err
	}

	fieldsChanged := !SameData(subscriber, member)
	if fieldsChanged {
		if subscriber, err = queries.SetMemberFields(ctx, setParams(subscriber.ID, member)); err != nil {
			return Result{}, emailTakenOr(err)
		}
	}
	unmatched, err := s.apply(ctx, queries, subscriber, member, fieldsChanged)
	return Result{Subscriber: subscriber, UnmatchedWorkplace: unmatched}, err
}

// apply runs ApplyMember and enqueues a push when the subscriber's fields
// or subscriptions changed.
func (s Service) apply(ctx context.Context, queries *db.Queries, subscriber db.Subscriber, member Member, fieldsChanged bool) (*int32, error) {
	changed, unmatched, err := ApplyMember(ctx, queries, subscriber.ID, member)
	if err != nil {
		return nil, err
	}
	if fieldsChanged || changed {
		err = s.push(ctx, queries, subscriber.ID)
	}
	return unmatched, err
}

func (s Service) push(ctx context.Context, queries *db.Queries, subscriberID pgtype.UUID) error {
	if !s.PushEnabled {
		return nil
	}
	return queries.EnqueuePushSubscriber(ctx, subscriberID)
}

// Delete is flow 3: hard-delete the member (subscriptions and pending push
// jobs cascade) and enqueue a provider delete. A missing member is not an
// error.
func (s Service) Delete(ctx context.Context, queries *db.Queries, memberNumber int32) error {
	listmonkID, err := queries.DeleteSubscriberByMemberNumber(ctx, &memberNumber)
	return s.afterDelete(ctx, queries, listmonkID, err)
}

// DeleteSubscriber is flow 3 for any subscriber, by ID.
func (s Service) DeleteSubscriber(ctx context.Context, queries *db.Queries, id pgtype.UUID) error {
	listmonkID, err := queries.DeleteSubscriber(ctx, id)
	return s.afterDelete(ctx, queries, listmonkID, err)
}

func (s Service) afterDelete(ctx context.Context, queries *db.Queries, listmonkID *int32, err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil || listmonkID == nil || !s.PushEnabled {
		return err
	}
	return queries.EnqueueDeleteSubscriber(ctx, listmonkID)
}

// AddToList is flow 4's admin add: a missing row becomes a 'manual'
// subscription, a 'removed' row is revived (keeping its source), an
// 'unsubscribed' row is ErrUnsubscribed. An already subscribed row changes
// nothing.
func (s Service) AddToList(ctx context.Context, queries *db.Queries, listID pgtype.UUID, memberNumber int32) (db.Subscriber, error) {
	list, err := queries.GetList(ctx, listID)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && list.DeletedAt.Valid) {
		return db.Subscriber{}, ErrListNotFound
	}
	if err != nil {
		return db.Subscriber{}, err
	}
	subscriber, row, err := memberSubscription(ctx, queries, listID, memberNumber)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		_, err = queries.InsertSubscription(ctx, db.InsertSubscriptionParams{
			SubscriberID: subscriber.ID, ListID: listID, Status: "subscribed", Source: "manual",
		})
	case err != nil:
		return db.Subscriber{}, err
	case row.Status == "unsubscribed":
		return db.Subscriber{}, ErrUnsubscribed
	case row.Status == "subscribed":
		return subscriber, nil
	default:
		err = queries.SetSubscriptionStatus(ctx, db.SetSubscriptionStatusParams{SubscriberID: subscriber.ID, ListID: listID, Status: "subscribed"})
	}
	if err != nil {
		return db.Subscriber{}, err
	}
	return subscriber, s.push(ctx, queries, subscriber.ID)
}

// RemoveFromList is flow 4's admin removal: a 'subscribed' row becomes
// 'removed'. An unsubscribed, removed or missing row changes nothing.
func (s Service) RemoveFromList(ctx context.Context, queries *db.Queries, listID pgtype.UUID, memberNumber int32) error {
	if _, err := queries.GetList(ctx, listID); errors.Is(err, pgx.ErrNoRows) {
		return ErrListNotFound
	} else if err != nil {
		return err
	}
	subscriber, row, err := memberSubscription(ctx, queries, listID, memberNumber)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && row.Status != "subscribed") {
		return nil
	}
	if err != nil {
		return err
	}
	if err := queries.SetSubscriptionStatus(ctx, db.SetSubscriptionStatusParams{SubscriberID: subscriber.ID, ListID: listID, Status: "removed"}); err != nil {
		return err
	}
	return s.push(ctx, queries, subscriber.ID)
}

// AddAutomatic is one subscription_add from the reconciliation: give the
// member a 'subscribed' row on the list with this source (and rule), unless
// a row exists by now. Pushes when it inserted one. A list deleted
// meanwhile is ErrListNotFound.
func (s Service) AddAutomatic(ctx context.Context, queries *db.Queries, memberNumber int32, listID pgtype.UUID, source string, ruleID pgtype.UUID) error {
	if list, err := queries.GetList(ctx, listID); errors.Is(err, pgx.ErrNoRows) || (err == nil && list.DeletedAt.Valid) {
		return ErrListNotFound
	} else if err != nil {
		return err
	}
	subscriber, _, err := memberSubscription(ctx, queries, listID, memberNumber)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	inserted, err := queries.InsertSubscription(ctx, db.InsertSubscriptionParams{
		SubscriberID: subscriber.ID, ListID: listID, Status: "subscribed", Source: source, RuleID: ruleID,
	})
	if err != nil || inserted == 0 {
		return err
	}
	return s.push(ctx, queries, subscriber.ID)
}

// RemoveAutomatic is one subscription_remove from the reconciliation: a
// 'subscribed' rule or workplace row becomes 'removed'. Any other row (or
// none) is left alone, as ApplyMember would.
func (s Service) RemoveAutomatic(ctx context.Context, queries *db.Queries, memberNumber int32, listID pgtype.UUID) error {
	subscriber, row, err := memberSubscription(ctx, queries, listID, memberNumber)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if row.Status != "subscribed" || (row.Source != SourceRule && row.Source != SourceWorkplace) {
		return nil
	}
	if err := queries.SetSubscriptionStatus(ctx, db.SetSubscriptionStatusParams{SubscriberID: subscriber.ID, ListID: listID, Status: "removed"}); err != nil {
		return err
	}
	return s.push(ctx, queries, subscriber.ID)
}

// memberSubscription loads the member and their row on the list. A missing
// member is ErrNotFound; a missing row is pgx.ErrNoRows with the subscriber set.
func memberSubscription(ctx context.Context, queries *db.Queries, listID pgtype.UUID, memberNumber int32) (db.Subscriber, db.Subscription, error) {
	subscriber, err := queries.GetSubscriberByMemberNumber(ctx, &memberNumber)
	if errors.Is(err, pgx.ErrNoRows) {
		return subscriber, db.Subscription{}, ErrNotFound
	}
	if err != nil {
		return subscriber, db.Subscription{}, err
	}
	row, err := queries.GetSubscription(ctx, db.GetSubscriptionParams{SubscriberID: subscriber.ID, ListID: listID})
	return subscriber, row, err
}

// ApplyMember brings the subscriber's automatic subscriptions in line with
// DesiredLists: a desired list with no row gets a 'subscribed' row; a
// 'subscribed' rule or workplace row no longer desired becomes 'removed'.
// Every other row (unsubscribed, removed, manual, self) is left alone, so
// automation never revives an opt-out or an admin removal. changed reports
// whether any row was written.
func ApplyMember(ctx context.Context, queries *db.Queries, subscriberID pgtype.UUID, member Member) (changed bool, unmatchedWorkplace *int32, err error) {
	rules, err := queries.ListRules(ctx)
	if err != nil {
		return false, nil, err
	}
	lists, err := queries.ListAllLists(ctx)
	if err != nil {
		return false, nil, err
	}
	desired, unmatchedWorkplace := DesiredLists(member, rules, lists)
	if unmatchedWorkplace != nil {
		log.Printf("member %d: no active list for workplace_newsletter_id %d, skipped", member.MemberNumber, *unmatchedWorkplace)
	}

	rows, err := queries.ListSubscriptions(ctx, subscriberID)
	if err != nil {
		return false, nil, err
	}
	wanted := map[pgtype.UUID]bool{}
	for _, entry := range desired {
		wanted[entry.ListID] = true
		inserted, err := queries.InsertSubscription(ctx, db.InsertSubscriptionParams{
			SubscriberID: subscriberID, ListID: entry.ListID, Status: "subscribed", Source: entry.Source, RuleID: entry.RuleID,
		})
		if err != nil {
			return false, nil, fmt.Errorf("adding subscription: %w", err)
		}
		changed = changed || inserted > 0
	}
	for _, row := range rows {
		automatic := row.Source == SourceRule || row.Source == SourceWorkplace
		if automatic && row.Status == "subscribed" && !wanted[row.ListID] {
			if err := queries.SetSubscriptionStatus(ctx, db.SetSubscriptionStatusParams{
				SubscriberID: subscriberID, ListID: row.ListID, Status: "removed",
			}); err != nil {
				return false, nil, fmt.Errorf("removing subscription: %w", err)
			}
			changed = true
		}
	}
	return changed, unmatchedWorkplace, nil
}

func setParams(id pgtype.UUID, member Member) db.SetMemberFieldsParams {
	return db.SetMemberFieldsParams{
		ID:           id,
		Email:        member.Email,
		Name:         member.Name,
		MemberNumber: &member.MemberNumber,
		City:         member.City,
		Lang:         &member.Lang,
	}
}

// Normalize trims member's fields (an empty city becomes nil) and checks
// them: a positive member_number, a plain email address, and name and lang
// present.
func Normalize(member Member) (Member, error) {
	member.Email = strings.TrimSpace(member.Email)
	member.Name = strings.TrimSpace(member.Name)
	member.Lang = strings.TrimSpace(member.Lang)
	if member.City != nil {
		if city := strings.TrimSpace(*member.City); city != "" {
			member.City = &city
		} else {
			member.City = nil
		}
	}
	switch address, err := mail.ParseAddress(member.Email); {
	case member.MemberNumber <= 0:
		return member, errors.New("member_number must be a positive integer")
	case err != nil || address.Address != member.Email:
		return member, errors.New("email must be a plain email address")
	case member.Name == "" || member.Lang == "":
		return member, errors.New("name and lang are required")
	}
	return member, nil
}

// SameData reports whether the subscriber already stores member's data
// (email, name, city, lang).
func SameData(subscriber db.Subscriber, member Member) bool {
	params := setParams(subscriber.ID, member)
	return subscriber.Email == params.Email && subscriber.Name == params.Name &&
		equal(subscriber.City, params.City) && equal(subscriber.Lang, params.Lang)
}

func equal(a, b *string) bool {
	return (a == nil) == (b == nil) && (a == nil || *a == *b)
}

// emailTakenOr maps a unique violation on the email index to ErrEmailTaken.
func emailTakenOr(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.ConstraintName == "subscribers_lower_email_idx" {
		return ErrEmailTaken
	}
	return err
}
