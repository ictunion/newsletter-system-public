// Package changes decides pending changes: approving one applies it through
// the same code as orca's direct calls (members.Service), rejecting one
// records the decision so the same change is not raised again.
package changes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/kubik/newsletter-system/internal/db"
	"github.com/kubik/newsletter-system/internal/members"
	"github.com/kubik/newsletter-system/internal/syncjob"
)

var (
	ErrNotFound   = errors.New("change not found")
	ErrNotPending = errors.New("change is already decided")
	// ErrStale: the member was updated after the change was detected, so its
	// payload is older than what we store. The next reconciliation re-checks.
	ErrStale = errors.New("member changed since the change was detected")
)

// Approve applies the pending change and marks it approved by decidedBy (a
// Keycloak sub). Run it in a transaction: the change row is locked, and a
// failure to apply leaves the change pending.
func Approve(ctx context.Context, queries *db.Queries, service members.Service, id pgtype.UUID, decidedBy string) (db.PendingChange, error) {
	change, err := lockPending(ctx, queries, id)
	if err != nil {
		return change, err
	}
	if err := apply(ctx, queries, service, change); err != nil {
		return change, fmt.Errorf("applying %s: %w", change.Kind, err)
	}
	return queries.DecidePendingChange(ctx, db.DecidePendingChangeParams{ID: id, Status: "approved", DecidedBy: &decidedBy})
}

// Reject marks the pending change rejected by decidedBy, changing nothing else.
func Reject(ctx context.Context, queries *db.Queries, id pgtype.UUID, decidedBy string) (db.PendingChange, error) {
	if change, err := lockPending(ctx, queries, id); err != nil {
		return change, err
	}
	return queries.DecidePendingChange(ctx, db.DecidePendingChangeParams{ID: id, Status: "rejected", DecidedBy: &decidedBy})
}

func lockPending(ctx context.Context, queries *db.Queries, id pgtype.UUID) (db.PendingChange, error) {
	change, err := queries.LockPendingChange(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return change, ErrNotFound
	}
	if err == nil && change.Status != "pending" {
		return change, ErrNotPending
	}
	return change, err
}

func apply(ctx context.Context, queries *db.Queries, service members.Service, change db.PendingChange) error {
	switch change.Kind {
	case syncjob.ChangeMemberNew, syncjob.ChangeMemberUpdated:
		var member members.Member
		if err := json.Unmarshal(change.Payload, &member); err != nil {
			return err
		}
		if change.Kind == syncjob.ChangeMemberNew {
			_, err := service.Create(ctx, queries, member)
			return err
		}
		subscriber, err := queries.GetSubscriberByMemberNumber(ctx, &member.MemberNumber)
		if err == nil && subscriber.UpdatedAt.After(change.DetectedAt) {
			return ErrStale
		}
		_, err = service.Update(ctx, queries, member)
		return err
	case syncjob.ChangeMemberRemoved:
		return service.Delete(ctx, queries, *change.MemberNumber)
	case syncjob.ChangeSubscriptionAdd:
		var payload syncjob.SubscriptionChange
		if err := json.Unmarshal(change.Payload, &payload); err != nil {
			return err
		}
		return service.AddAutomatic(ctx, queries, *change.MemberNumber, change.ListID, payload.Source, payload.RuleID)
	case syncjob.ChangeSubscriptionRemove:
		return service.RemoveAutomatic(ctx, queries, *change.MemberNumber, change.ListID)
	case syncjob.ChangeListRemoved:
		return queries.SoftDeleteList(ctx, change.ListID)
	}
	return fmt.Errorf("unknown change kind %q", change.Kind)
}
