package handler

import (
	"context"
	"errors"
	"log"
	"net/http"
	"net/mail"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kubik/newsletter-system/internal/db"
	"github.com/kubik/newsletter-system/internal/members"
)

// memberFieldsRequest is the body of PUT /members/{member_number}.
type memberFieldsRequest struct {
	Email     string  `json:"email"`
	FirstName string  `json:"first_name"`
	LastName  string  `json:"last_name"`
	City      *string `json:"city"`
	Lang      string  `json:"lang"`
	// orca's workplaces.newsletter_id: the Listmonk ID of the workplace list.
	WorkplaceNewsletterID *int32 `json:"workplace_newsletter_id"`
}

// createMemberRequest is the body of POST /members.
type createMemberRequest struct {
	MemberNumber int32 `json:"member_number"`
	memberFieldsRequest
}

type subscriberResponse struct {
	ID                   pgtype.UUID        `json:"id" swaggertype:"string" format:"uuid"`
	Email                string             `json:"email"`
	Name                 string             `json:"name"`
	MemberNumber         *int32             `json:"member_number"`
	FirstName            *string            `json:"first_name"`
	LastName             *string            `json:"last_name"`
	City                 *string            `json:"city"`
	Lang                 *string            `json:"lang"`
	ListmonkSubscriberID *int32             `json:"listmonk_subscriber_id"`
	CreatedAt            time.Time          `json:"created_at"`
	UpdatedAt            time.Time          `json:"updated_at"`
	Subscriptions        []subscriptionItem `json:"subscriptions"`
}

type subscriptionItem struct {
	ListID         pgtype.UUID        `json:"list_id" swaggertype:"string" format:"uuid"`
	ListName       string             `json:"list_name"`
	ListmonkID     *int32             `json:"listmonk_id"`
	ListDeletedAt  pgtype.Timestamptz `json:"list_deleted_at" swaggertype:"string" format:"date-time"`
	Status         string             `json:"status" enums:"subscribed,unsubscribed,removed"`
	Source         string             `json:"source" enums:"rule,workplace,manual,self"`
	RuleID         pgtype.UUID        `json:"rule_id" swaggertype:"string" format:"uuid"`
	ListmonkStatus *string            `json:"listmonk_status"`
	CreatedAt      time.Time          `json:"created_at"`
	UpdatedAt      time.Time          `json:"updated_at"`
}

type memberResponse struct {
	subscriberResponse
	// Set when workplace_newsletter_id matches no active list; the other
	// lists are still applied.
	UnmatchedWorkplaceNewsletterID *int32 `json:"unmatched_workplace_newsletter_id"`
}

// CreateMember handles POST /members (flow 1).
//
// @Summary      Add a member
// @Description  Requires the update-newsletter-members role. Called by orca right after it accepts a member. Idempotent: an existing member_number answers 200 with the current state and changes nothing. An email held by a non-member subscriber attaches the member to that subscriber. Puts the member on the lists of every matching rule and on their workplace list.
// @Tags         members
// @Security     BearerAuth
// @Accept       json
// @Produce      json
// @Param        body  body  handler.createMemberRequest  true  "Member"
// @Success      200,201  {object}  handler.memberResponse
// @Failure      400,401,403,409  {object}  map[string]string
// @Router       /members [post]
func CreateMember(pool *pgxpool.Pool, service members.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body createMemberRequest
		if !decodeJSON(w, r, &body) {
			return
		}
		if body.MemberNumber <= 0 {
			writeError(w, http.StatusBadRequest, "member_number must be a positive integer")
			return
		}
		member, ok := validMember(w, body.MemberNumber, body.memberFieldsRequest)
		if !ok {
			return
		}
		saveMember(w, r, pool, member, service.Create)
	}
}

// UpdateMember handles PUT /members/{member_number} (flow 2).
//
// @Summary      Update a member
// @Description  Requires the update-newsletter-members role. Called by orca when a member's workplace, city, email, name or lang change. Updates the member and their rule and workplace lists, and returns the updated member. 409 when another subscriber has the email.
// @Tags         members
// @Security     BearerAuth
// @Accept       json
// @Produce      json
// @Param        member_number  path  int                          true  "Member number"
// @Param        body           body  handler.memberFieldsRequest  true  "Member data"
// @Success      200  {object}  handler.memberResponse
// @Failure      400,401,403,404,409  {object}  map[string]string
// @Router       /members/{member_number} [put]
func UpdateMember(pool *pgxpool.Pool, service members.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		memberNumber, ok := memberNumberParam(w, r)
		if !ok {
			return
		}
		var body memberFieldsRequest
		if !decodeJSON(w, r, &body) {
			return
		}
		member, ok := validMember(w, memberNumber, body)
		if !ok {
			return
		}
		saveMember(w, r, pool, member, service.Update)
	}
}

// saveMember runs a create or update flow in a transaction and answers with
// the resulting member.
func saveMember(w http.ResponseWriter, r *http.Request, pool *pgxpool.Pool, member members.Member,
	flow func(context.Context, *db.Queries, members.Member) (members.Result, error)) {
	var result members.Result
	var response memberResponse
	err := pgx.BeginFunc(r.Context(), pool, func(tx pgx.Tx) error {
		queries := db.New(tx)
		var err error
		if result, err = flow(r.Context(), queries, member); err != nil {
			return err
		}
		response.subscriberResponse, err = loadSubscriber(r.Context(), queries, result.Subscriber)
		return err
	})
	switch {
	case errors.Is(err, members.ErrNotFound):
		writeError(w, http.StatusNotFound, "member not found")
		return
	case errors.Is(err, members.ErrEmailTaken):
		log.Printf("member %d: email already used by another subscriber", member.MemberNumber)
		writeError(w, http.StatusConflict, "email already used by another subscriber")
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, "failed to save member")
		return
	}
	response.UnmatchedWorkplaceNewsletterID = result.UnmatchedWorkplace
	status := http.StatusOK
	if result.Created {
		status = http.StatusCreated
	}
	writeJSON(w, status, response)
}

// DeleteMember handles DELETE /members/{member_number} (flow 3).
//
// @Summary      Delete a member
// @Description  Requires the update-newsletter-members or manage-newsletters role. Hard-deletes the member and their subscriptions, and deletes them from Listmonk in the background. 204 also when the member does not exist.
// @Tags         members
// @Security     BearerAuth
// @Param        member_number  path  int  true  "Member number"
// @Success      204
// @Failure      400,401,403  {object}  map[string]string
// @Router       /members/{member_number} [delete]
func DeleteMember(pool *pgxpool.Pool, service members.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		memberNumber, ok := memberNumberParam(w, r)
		if !ok {
			return
		}
		deleteInTx(w, r, pool, func(queries *db.Queries) error { return service.Delete(r.Context(), queries, memberNumber) })
	}
}

// DeleteSubscriber handles DELETE /subscribers/{id}.
//
// @Summary      Delete a subscriber
// @Description  Requires the manage-newsletters role. Same as deleting a member, for any subscriber (e.g. a non-member's erasure request). 204 also when the subscriber does not exist.
// @Tags         subscribers
// @Security     BearerAuth
// @Param        id  path  string  true  "Subscriber ID (UUID)"
// @Success      204
// @Failure      400,401,403  {object}  map[string]string
// @Router       /subscribers/{id} [delete]
func DeleteSubscriber(pool *pgxpool.Pool, service members.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := uuidParam(w, r, "id")
		if !ok {
			return
		}
		deleteInTx(w, r, pool, func(queries *db.Queries) error { return service.DeleteSubscriber(r.Context(), queries, id) })
	}
}

func deleteInTx(w http.ResponseWriter, r *http.Request, pool *pgxpool.Pool, flow func(*db.Queries) error) {
	if err := pgx.BeginFunc(r.Context(), pool, func(tx pgx.Tx) error { return flow(db.New(tx)) }); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// GetMember handles GET /members/{member_number}.
//
// @Summary      Get a member
// @Description  Requires the view-newsletters role. The member's data and every subscription with its status, source, rule and last seen Listmonk status.
// @Tags         members
// @Security     BearerAuth
// @Produce      json
// @Param        member_number  path  int  true  "Member number"
// @Success      200  {object}  handler.subscriberResponse
// @Failure      400,401,403,404  {object}  map[string]string
// @Router       /members/{member_number} [get]
func GetMember(queries *db.Queries) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		memberNumber, ok := memberNumberParam(w, r)
		if !ok {
			return
		}
		subscriber, err := queries.GetSubscriberByMemberNumber(r.Context(), &memberNumber)
		writeSubscriber(w, r, queries, subscriber, err)
	}
}

// GetSubscriber handles GET /subscribers/{id}.
//
// @Summary      Get a subscriber
// @Description  Requires the view-newsletters role. Same as GET /members/{member_number}, for any subscriber.
// @Tags         subscribers
// @Security     BearerAuth
// @Produce      json
// @Param        id  path  string  true  "Subscriber ID (UUID)"
// @Success      200  {object}  handler.subscriberResponse
// @Failure      400,401,403,404  {object}  map[string]string
// @Router       /subscribers/{id} [get]
func GetSubscriber(queries *db.Queries) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := uuidParam(w, r, "id")
		if !ok {
			return
		}
		subscriber, err := queries.GetSubscriber(r.Context(), id)
		writeSubscriber(w, r, queries, subscriber, err)
	}
}

func writeSubscriber(w http.ResponseWriter, r *http.Request, queries *db.Queries, subscriber db.Subscriber, err error) {
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "subscriber not found")
		return
	}
	var response subscriberResponse
	if err == nil {
		response, err = loadSubscriber(r.Context(), queries, subscriber)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load subscriber")
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func loadSubscriber(ctx context.Context, queries *db.Queries, subscriber db.Subscriber) (subscriberResponse, error) {
	rows, err := queries.ListSubscriptions(ctx, subscriber.ID)
	if err != nil {
		return subscriberResponse{}, err
	}
	response := subscriberResponse{
		ID: subscriber.ID, Email: subscriber.Email, Name: subscriber.Name, MemberNumber: subscriber.MemberNumber,
		FirstName: subscriber.FirstName, LastName: subscriber.LastName, City: subscriber.City, Lang: subscriber.Lang,
		ListmonkSubscriberID: subscriber.ListmonkSubscriberID, CreatedAt: subscriber.CreatedAt, UpdatedAt: subscriber.UpdatedAt,
		Subscriptions: make([]subscriptionItem, 0, len(rows)),
	}
	for _, row := range rows {
		response.Subscriptions = append(response.Subscriptions, subscriptionItem(row))
	}
	return response, nil
}

func memberNumberParam(w http.ResponseWriter, r *http.Request) (int32, bool) {
	n, err := strconv.ParseInt(r.PathValue("member_number"), 10, 32)
	if err != nil || n <= 0 {
		writeError(w, http.StatusBadRequest, "member_number must be a positive integer")
		return 0, false
	}
	return int32(n), true
}

// validMember trims and validates member data, answering 400 itself when
// it's invalid.
func validMember(w http.ResponseWriter, memberNumber int32, body memberFieldsRequest) (members.Member, bool) {
	member := members.Member{
		MemberNumber:          memberNumber,
		Email:                 strings.TrimSpace(body.Email),
		FirstName:             strings.TrimSpace(body.FirstName),
		LastName:              strings.TrimSpace(body.LastName),
		City:                  trimmedOrNil(body.City),
		Lang:                  strings.TrimSpace(body.Lang),
		WorkplaceNewsletterID: body.WorkplaceNewsletterID,
	}
	if address, err := mail.ParseAddress(member.Email); err != nil || address.Address != member.Email {
		writeError(w, http.StatusBadRequest, "email must be a plain email address")
		return member, false
	}
	if member.FirstName == "" || member.LastName == "" || member.Lang == "" {
		writeError(w, http.StatusBadRequest, "first_name, last_name and lang are required")
		return member, false
	}
	return member, true
}
