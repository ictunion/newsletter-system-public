package handler

import (
	"errors"
	"net/http"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kubik/newsletter-system/internal/db"
	"github.com/kubik/newsletter-system/internal/members"
)

const (
	listSubscribersDefaultLimit = 100
	listSubscribersMaxLimit     = 500
)

// SubscriptionStatuses lists every subscriptions.status value.
var SubscriptionStatuses = []string{"subscribed", "unsubscribed", "removed"}

// AddListMember handles PUT /lists/{id}/members/{member_number} (flow 4).
//
// @Summary      Add a member to a list
// @Description  Requires the manage-newsletters role. Subscribes the member to the list as a manual subscription, or re-adds them after an admin removal, and returns the member. 409 when the member unsubscribed from the list themselves: only they can undo that. 404 for an unknown member or a missing or deleted list.
// @Tags         lists
// @Security     BearerAuth
// @Produce      json
// @Param        id             path  string  true  "List ID (UUID)"
// @Param        member_number  path  int     true  "Member number"
// @Success      200  {object}  handler.subscriberResponse
// @Failure      400,401,403,404,409  {object}  map[string]string
// @Router       /lists/{id}/members/{member_number} [put]
func AddListMember(pool *pgxpool.Pool, service members.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		listID, memberNumber, ok := listMemberParams(w, r)
		if !ok {
			return
		}
		var response subscriberResponse
		err := pgx.BeginFunc(r.Context(), pool, func(tx pgx.Tx) error {
			queries := db.New(tx)
			subscriber, err := service.AddToList(r.Context(), queries, listID, memberNumber)
			if err != nil {
				return err
			}
			response, err = loadSubscriber(r.Context(), queries, subscriber)
			return err
		})
		if writeAssignmentError(w, err) {
			return
		}
		writeJSON(w, http.StatusOK, response)
	}
}

// RemoveListMember handles DELETE /lists/{id}/members/{member_number} (flow 4).
//
// @Summary      Remove a member from a list
// @Description  Requires the manage-newsletters role. Sets the member's subscription to removed. Rules and the workplace never re-add a removed subscription. A member who is not subscribed is left as is. 404 for an unknown member or list.
// @Tags         lists
// @Security     BearerAuth
// @Param        id             path  string  true  "List ID (UUID)"
// @Param        member_number  path  int     true  "Member number"
// @Success      204
// @Failure      400,401,403,404  {object}  map[string]string
// @Router       /lists/{id}/members/{member_number} [delete]
func RemoveListMember(pool *pgxpool.Pool, service members.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		listID, memberNumber, ok := listMemberParams(w, r)
		if !ok {
			return
		}
		err := pgx.BeginFunc(r.Context(), pool, func(tx pgx.Tx) error {
			return service.RemoveFromList(r.Context(), db.New(tx), listID, memberNumber)
		})
		if writeAssignmentError(w, err) {
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func listMemberParams(w http.ResponseWriter, r *http.Request) (pgtype.UUID, int32, bool) {
	listID, ok := uuidParam(w, r, "id")
	if !ok {
		return listID, 0, false
	}
	memberNumber, ok := memberNumberParam(w, r)
	return listID, memberNumber, ok
}

// writeAssignmentError answers for a failed assignment and reports whether it did.
func writeAssignmentError(w http.ResponseWriter, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, members.ErrListNotFound):
		writeError(w, http.StatusNotFound, "list not found")
	case errors.Is(err, members.ErrNotFound):
		writeError(w, http.StatusNotFound, "member not found")
	case errors.Is(err, members.ErrUnsubscribed):
		writeError(w, http.StatusConflict, "member unsubscribed from this list themselves")
	default:
		writeError(w, http.StatusInternalServerError, "failed to change subscription")
	}
	return true
}

type listSubscribersResponse struct {
	Total       int64                `json:"total"`
	Limit       int32                `json:"limit"`
	Offset      int32                `json:"offset"`
	Subscribers []listSubscriberItem `json:"subscribers"`
}

type listSubscriberItem struct {
	SubscriberID   pgtype.UUID `json:"subscriber_id" swaggertype:"string" format:"uuid"`
	Email          string      `json:"email"`
	Name           string      `json:"name"`
	MemberNumber   *int32      `json:"member_number"`
	Status         string      `json:"status" enums:"subscribed,unsubscribed,removed"`
	Source         string      `json:"source" enums:"rule,workplace,manual,self"`
	RuleID         pgtype.UUID `json:"rule_id" swaggertype:"string" format:"uuid"`
	ListmonkStatus *string     `json:"listmonk_status"`
	CreatedAt      time.Time   `json:"created_at"`
	UpdatedAt      time.Time   `json:"updated_at"`
}

// ListListSubscribers handles GET /lists/{id}/subscribers.
//
// @Summary      List a list's subscribers
// @Description  Requires the view-newsletters role. The list's subscriptions with their subscriber, by name.
// @Tags         lists
// @Security     BearerAuth
// @Produce      json
// @Param        id      path   string  true   "List ID (UUID)"
// @Param        status  query  string  false  "Only this status"  Enums(subscribed, unsubscribed, removed)
// @Param        limit   query  int     false  "Max results, default 100, capped at 500"
// @Param        offset  query  int     false  "Default 0"
// @Success      200  {object}  handler.listSubscribersResponse
// @Failure      400,401,403,404  {object}  map[string]string
// @Router       /lists/{id}/subscribers [get]
func ListListSubscribers(queries *db.Queries) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		listID, ok := uuidParam(w, r, "id")
		if !ok {
			return
		}
		query := r.URL.Query()
		params := db.ListListSubscribersParams{ListID: listID}
		if s := query.Get("status"); s != "" {
			if !slices.Contains(SubscriptionStatuses, s) {
				writeError(w, http.StatusBadRequest, "status must be one of subscribed, unsubscribed, removed")
				return
			}
			params.Status = &s
		}
		if params.Lim, params.Off, ok = pageParams(w, query, listSubscribersDefaultLimit, listSubscribersMaxLimit); !ok {
			return
		}

		if _, err := queries.GetList(r.Context(), listID); errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "list not found")
			return
		} else if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to load list")
			return
		}
		rows, err := queries.ListListSubscribers(r.Context(), params)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to list subscribers")
			return
		}
		response := listSubscribersResponse{Limit: params.Lim, Offset: params.Off, Subscribers: make([]listSubscriberItem, 0, len(rows))}
		for _, row := range rows {
			response.Total = row.TotalCount
			response.Subscribers = append(response.Subscribers, listSubscriberItem{
				SubscriberID: row.SubscriberID, Email: row.Email, Name: row.Name, MemberNumber: row.MemberNumber,
				Status: row.Status, Source: row.Source, RuleID: row.RuleID, ListmonkStatus: row.ListmonkStatus,
				CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
			})
		}
		writeJSON(w, http.StatusOK, response)
	}
}
