package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kubik/newsletter-system/internal/changes"
	"github.com/kubik/newsletter-system/internal/db"
	"github.com/kubik/newsletter-system/internal/members"
)

const (
	changesDefaultLimit = 100
	changesMaxLimit     = 1000
	// maxBulkApprove caps POST /changes/approve.
	maxBulkApprove = 1000
)

var (
	ChangeKinds    = []string{"member_new", "member_updated", "member_removed", "subscription_add", "subscription_remove", "list_removed"}
	ChangeStatuses = []string{"pending", "approved", "rejected"}
)

type changesResponse struct {
	Total   int64        `json:"total"`
	Limit   int32        `json:"limit"`
	Offset  int32        `json:"offset"`
	Changes []changeItem `json:"changes"`
}

type changeItem struct {
	ID           pgtype.UUID `json:"id" swaggertype:"string" format:"uuid"`
	Kind         string      `json:"kind" enums:"member_new,member_updated,member_removed,subscription_add,subscription_remove,list_removed"`
	MemberNumber *int32      `json:"member_number"`
	ListID       pgtype.UUID `json:"list_id" swaggertype:"string" format:"uuid"`
	// Set for list changes and subscription changes.
	ListName *string `json:"list_name,omitempty"`
	// member_new, member_updated: orca's member. member_removed: {email, name}.
	// subscription_add: {source, rule_id}. subscription_remove: {source}.
	// list_removed: {listmonk_id, name}.
	Payload    json.RawMessage    `json:"payload" swaggertype:"object"`
	Status     string             `json:"status" enums:"pending,approved,rejected"`
	DetectedAt time.Time          `json:"detected_at"`
	DecidedAt  pgtype.Timestamptz `json:"decided_at" swaggertype:"string" format:"date-time"`
	// Keycloak subject of the deciding admin.
	DecidedBy *string `json:"decided_by"`
}

func changeItemOf(change db.PendingChange) changeItem {
	return changeItem{
		ID: change.ID, Kind: change.Kind, MemberNumber: change.MemberNumber, ListID: change.ListID, Payload: change.Payload,
		Status: change.Status, DetectedAt: change.DetectedAt, DecidedAt: change.DecidedAt, DecidedBy: change.DecidedBy,
	}
}

// ListChanges handles GET /changes.
//
// @Summary      List pending changes
// @Description  Requires the view-newsletters role. Drift found by the orca reconciliation and the Listmonk list sync, newest first. Pending ones wait for an admin to approve or reject them.
// @Tags         changes
// @Security     BearerAuth
// @Produce      json
// @Param        status  query  string  false  "Only this status"  Enums(pending, approved, rejected)
// @Param        kind    query  string  false  "Only this kind"  Enums(member_new, member_updated, member_removed, subscription_add, subscription_remove, list_removed)
// @Param        limit   query  int     false  "Max results, default 100, capped at 1000"
// @Param        offset  query  int     false  "Default 0"
// @Success      200  {object}  handler.changesResponse
// @Failure      400,401,403  {object}  map[string]string
// @Router       /changes [get]
func ListChanges(queries *db.Queries) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		var params db.ListPendingChangesParams
		for _, filter := range []struct {
			name    string
			allowed []string
			target  **string
		}{{"status", ChangeStatuses, &params.Status}, {"kind", ChangeKinds, &params.Kind}} {
			if s := query.Get(filter.name); s != "" {
				if !slices.Contains(filter.allowed, s) {
					writeError(w, http.StatusBadRequest, "unknown "+filter.name)
					return
				}
				*filter.target = &s
			}
		}
		var ok bool
		if params.Lim, params.Off, ok = pageParams(w, query, changesDefaultLimit, changesMaxLimit); !ok {
			return
		}
		rows, err := queries.ListPendingChanges(r.Context(), params)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to list changes")
			return
		}
		response := changesResponse{Limit: params.Lim, Offset: params.Off, Changes: make([]changeItem, 0, len(rows))}
		for _, row := range rows {
			response.Total = row.TotalCount
			item := changeItemOf(db.PendingChange{
				ID: row.ID, Kind: row.Kind, MemberNumber: row.MemberNumber, ListID: row.ListID, Payload: row.Payload,
				Status: row.Status, DetectedAt: row.DetectedAt, DecidedAt: row.DecidedAt, DecidedBy: row.DecidedBy,
			})
			item.ListName = row.ListName
			response.Changes = append(response.Changes, item)
		}
		writeJSON(w, http.StatusOK, response)
	}
}

// ApproveChange handles POST /changes/{id}/approve.
//
// @Summary      Approve a pending change
// @Description  Requires the manage-newsletters role. Applies the change (member_new adds the member, member_updated updates them, member_removed deletes them, subscription_add/remove change that one subscription, list_removed soft-deletes the list) and returns it. 409 when it is already decided, or when it can't be applied any more (the error says why); it then stays pending.
// @Tags         changes
// @Security     BearerAuth
// @Produce      json
// @Param        id  path  string  true  "Change ID (UUID)"
// @Success      200  {object}  handler.changeItem
// @Failure      400,401,403,404,409  {object}  map[string]string
// @Router       /changes/{id}/approve [post]
func ApproveChange(pool *pgxpool.Pool, service members.Service) http.HandlerFunc {
	return decideChange(pool, func(r *http.Request, queries *db.Queries, id pgtype.UUID, decidedBy string) (db.PendingChange, error) {
		return changes.Approve(r.Context(), queries, service, id, decidedBy)
	})
}

// RejectChange handles POST /changes/{id}/reject.
//
// @Summary      Reject a pending change
// @Description  Requires the manage-newsletters role. Changes nothing else; the same change with an identical payload is not raised again. Returns the change. 409 when it is already decided.
// @Tags         changes
// @Security     BearerAuth
// @Produce      json
// @Param        id  path  string  true  "Change ID (UUID)"
// @Success      200  {object}  handler.changeItem
// @Failure      400,401,403,404,409  {object}  map[string]string
// @Router       /changes/{id}/reject [post]
func RejectChange(pool *pgxpool.Pool) http.HandlerFunc {
	return decideChange(pool, func(r *http.Request, queries *db.Queries, id pgtype.UUID, decidedBy string) (db.PendingChange, error) {
		return changes.Reject(r.Context(), queries, id, decidedBy)
	})
}

func decideChange(pool *pgxpool.Pool, decide func(*http.Request, *db.Queries, pgtype.UUID, string) (db.PendingChange, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := uuidParam(w, r, "id")
		if !ok {
			return
		}
		decidedBy, ok := callerSub(w, r)
		if !ok {
			return
		}
		var decided db.PendingChange
		err := pgx.BeginFunc(r.Context(), pool, func(tx pgx.Tx) error {
			var err error
			decided, err = decide(r, db.New(tx), id, decidedBy)
			return err
		})
		if status, message := changeErrorStatus(err); status != 0 {
			writeError(w, status, message)
			return
		}
		writeJSON(w, http.StatusOK, changeItemOf(decided))
	}
}

type bulkApproveRequest struct {
	IDs []pgtype.UUID `json:"ids" swaggertype:"array,string"`
}

type bulkApproveResponse struct {
	Approved []changeItem        `json:"approved"`
	Failed   []bulkApproveFailed `json:"failed"`
}

type bulkApproveFailed struct {
	ID    pgtype.UUID `json:"id" swaggertype:"string" format:"uuid"`
	Error string      `json:"error"`
}

// ApproveChanges handles POST /changes/approve.
//
// @Summary      Approve several pending changes
// @Description  Requires the manage-newsletters role. Approves each change on its own, as POST /changes/{id}/approve does; one failing does not stop the others. Answers 200 with what was approved and what failed, and why. At most 1000 IDs.
// @Tags         changes
// @Security     BearerAuth
// @Accept       json
// @Produce      json
// @Param        body  body  handler.bulkApproveRequest  true  "Change IDs"
// @Success      200  {object}  handler.bulkApproveResponse
// @Failure      400,401,403  {object}  map[string]string
// @Router       /changes/approve [post]
func ApproveChanges(pool *pgxpool.Pool, service members.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body bulkApproveRequest
		if !decodeJSON(w, r, &body) {
			return
		}
		if len(body.IDs) == 0 || len(body.IDs) > maxBulkApprove {
			writeError(w, http.StatusBadRequest, "ids must hold 1 to 1000 change IDs")
			return
		}
		decidedBy, ok := callerSub(w, r)
		if !ok {
			return
		}
		response := bulkApproveResponse{Approved: []changeItem{}, Failed: []bulkApproveFailed{}}
		for _, id := range body.IDs {
			var approved db.PendingChange
			err := pgx.BeginFunc(r.Context(), pool, func(tx pgx.Tx) error {
				var err error
				approved, err = changes.Approve(r.Context(), db.New(tx), service, id, decidedBy)
				return err
			})
			if status, message := changeErrorStatus(err); status != 0 {
				response.Failed = append(response.Failed, bulkApproveFailed{ID: id, Error: message})
				continue
			}
			response.Approved = append(response.Approved, changeItemOf(approved))
		}
		writeJSON(w, http.StatusOK, response)
	}
}

// changeErrorStatus maps a decide error to a status and message; 0 for no error.
func changeErrorStatus(err error) (int, string) {
	switch {
	case err == nil:
		return 0, ""
	case errors.Is(err, changes.ErrNotFound):
		return http.StatusNotFound, "change not found"
	case errors.Is(err, changes.ErrNotPending):
		return http.StatusConflict, "change is already decided"
	case errors.Is(err, members.ErrEmailTaken), errors.Is(err, members.ErrNotFound), errors.Is(err, members.ErrListNotFound):
		return http.StatusConflict, err.Error()
	}
	return http.StatusInternalServerError, "failed to decide change"
}

// callerSub returns the authenticated caller's Keycloak sub.
func callerSub(w http.ResponseWriter, r *http.Request) (string, bool) {
	claims, ok := ClaimsFromContext(r.Context())
	if !ok || claims.Subject == "" {
		writeError(w, http.StatusUnauthorized, "no authenticated caller")
		return "", false
	}
	return claims.Subject, true
}
