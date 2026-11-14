package handler

import (
	"encoding/json"
	"net/http"
	"slices"
	"strconv"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/kubik/newsletter-system/internal/db"
)

// ListVisibilities lists every newsletter_lists.visibility value.
var ListVisibilities = []string{"private", "members", "public"}

type listsResponse struct {
	Lists []listItem `json:"lists"`
}

type listItem struct {
	ID                      pgtype.UUID        `json:"id" swaggertype:"string" format:"uuid"`
	Name                    string             `json:"name"`
	Description             string             `json:"description"`
	Visibility              string             `json:"visibility"`
	ListmonkID              *int32             `json:"listmonk_id"`
	ListmonkSubscriberCount *int32             `json:"listmonk_subscriber_count"`
	SyncedAt                pgtype.Timestamptz `json:"synced_at" swaggertype:"string" format:"date-time"`
	DeletedAt               pgtype.Timestamptz `json:"deleted_at" swaggertype:"string" format:"date-time"`
	SubscribedCount         int64              `json:"subscribed_count"`
	UnsubscribedCount       int64              `json:"unsubscribed_count"`
	RemovedCount            int64              `json:"removed_count"`
}

// ListLists handles GET /lists.
//
// @Summary      List newsletter lists
// @Description  Requires the view-newsletters role. Every list with its visibility, our subscription count per status, and Listmonk's own subscriber count, ordered by name. Soft-deleted lists only with include_deleted=true.
// @Tags         lists
// @Security     BearerAuth
// @Produce      json
// @Param        include_deleted  query  bool  false  "Include soft-deleted lists"
// @Success      200  {object}  handler.listsResponse
// @Failure      400,401,403  {object}  map[string]string
// @Router       /lists [get]
func ListLists(queries *db.Queries) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		includeDeleted := false
		if s := r.URL.Query().Get("include_deleted"); s != "" {
			var err error
			if includeDeleted, err = strconv.ParseBool(s); err != nil {
				writeError(w, http.StatusBadRequest, "include_deleted must be true or false")
				return
			}
		}

		rows, err := queries.ListNewsletterLists(r.Context(), db.ListNewsletterListsParams{IncludeDeleted: includeDeleted})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to list lists")
			return
		}
		response := listsResponse{Lists: make([]listItem, 0, len(rows))}
		for _, row := range rows {
			response.Lists = append(response.Lists, listItem(row))
		}
		writeJSON(w, http.StatusOK, response)
	}
}

type updateListRequest struct {
	Visibility string `json:"visibility" enums:"private,members,public"`
}

// UpdateList handles PUT /lists/{id}.
//
// @Summary      Change list visibility
// @Description  Requires the manage-newsletters role. Returns the updated list.
// @Tags         lists
// @Security     BearerAuth
// @Accept       json
// @Produce      json
// @Param        id    path  string                     true  "List ID (UUID)"
// @Param        body  body  handler.updateListRequest  true  "New visibility"
// @Success      200  {object}  handler.listItem
// @Failure      400,401,403,404  {object}  map[string]string
// @Router       /lists/{id} [put]
func UpdateList(queries *db.Queries) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := uuidParam(w, r, "id")
		if !ok {
			return
		}
		var body updateListRequest
		if !decodeJSON(w, r, &body) {
			return
		}
		if !slices.Contains(ListVisibilities, body.Visibility) {
			writeError(w, http.StatusBadRequest, "visibility must be one of private, members, public")
			return
		}

		updated, err := queries.UpdateListVisibility(r.Context(), db.UpdateListVisibilityParams{ID: id, Visibility: body.Visibility})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to update list")
			return
		}
		if updated == 0 {
			writeError(w, http.StatusNotFound, "list not found")
			return
		}
		rows, err := queries.ListNewsletterLists(r.Context(), db.ListNewsletterListsParams{IncludeDeleted: true, ID: id})
		if err != nil || len(rows) != 1 {
			writeError(w, http.StatusInternalServerError, "failed to read updated list")
			return
		}
		writeJSON(w, http.StatusOK, listItem(rows[0]))
	}
}

// uuidParam parses path value name as a UUID, answering 400 when it isn't one.
func uuidParam(w http.ResponseWriter, r *http.Request, name string) (pgtype.UUID, bool) {
	var id pgtype.UUID
	if err := id.Scan(r.PathValue(name)); err != nil {
		writeError(w, http.StatusBadRequest, name+" must be a UUID")
		return id, false
	}
	return id, true
}

// decodeJSON decodes the request body into v, rejecting unknown fields and
// answering 400 on any error.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return false
	}
	return true
}
