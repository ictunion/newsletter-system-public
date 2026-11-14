package handler

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/kubik/newsletter-system/internal/db"
)

type rulesResponse struct {
	Rules []ruleItem `json:"rules"`
}

type ruleItem struct {
	ID           pgtype.UUID `json:"id" swaggertype:"string" format:"uuid"`
	Kind         string      `json:"kind" enums:"all,city"`
	ListID       pgtype.UUID `json:"list_id" swaggertype:"string" format:"uuid"`
	CityContains *string     `json:"city_contains"`
	Enabled      bool        `json:"enabled"`
	Note         *string     `json:"note"`
	CreatedAt    time.Time   `json:"created_at"`
}

// ruleRequest is the body of POST /rules and PUT /rules/{id}; PUT replaces
// every field.
type ruleRequest struct {
	Kind   string      `json:"kind" enums:"all,city"`
	ListID pgtype.UUID `json:"list_id" swaggertype:"string" format:"uuid"`
	// Required for kind city, must be empty for kind all.
	CityContains *string `json:"city_contains"`
	// Defaults to true.
	Enabled *bool   `json:"enabled"`
	Note    *string `json:"note"`
}

// ListRules handles GET /rules.
//
// @Summary      List subscription rules
// @Description  Requires the view-newsletters or manage-rules role. Every rule, oldest first. Rules put members on lists: kind all matches every member, kind city matches members whose city contains city_contains (case- and accent-insensitive). Every matching enabled rule applies.
// @Tags         rules
// @Security     BearerAuth
// @Produce      json
// @Success      200  {object}  handler.rulesResponse
// @Failure      401,403  {object}  map[string]string
// @Router       /rules [get]
func ListRules(queries *db.Queries) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rows, err := queries.ListRules(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to list rules")
			return
		}
		response := rulesResponse{Rules: make([]ruleItem, 0, len(rows))}
		for _, row := range rows {
			response.Rules = append(response.Rules, ruleItem(row))
		}
		writeJSON(w, http.StatusOK, response)
	}
}

// CreateRule handles POST /rules.
//
// @Summary      Create a subscription rule
// @Description  Requires the manage-rules role. Existing members are not re-evaluated here: the daily orca reconciliation proposes the resulting subscription changes.
// @Tags         rules
// @Security     BearerAuth
// @Accept       json
// @Produce      json
// @Param        body  body  handler.ruleRequest  true  "Rule"
// @Success      201  {object}  handler.ruleItem
// @Failure      400,401,403  {object}  map[string]string
// @Router       /rules [post]
func CreateRule(queries *db.Queries) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		params, ok := decodeRule(w, r, queries)
		if !ok {
			return
		}
		rule, err := queries.CreateRule(r.Context(), params)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to create rule")
			return
		}
		writeJSON(w, http.StatusCreated, ruleItem(rule))
	}
}

// UpdateRule handles PUT /rules/{id}.
//
// @Summary      Replace a subscription rule
// @Description  Requires the manage-rules role. Replaces every field and returns the updated rule. Existing members are not re-evaluated here: the daily orca reconciliation proposes the resulting subscription changes.
// @Tags         rules
// @Security     BearerAuth
// @Accept       json
// @Produce      json
// @Param        id    path  string               true  "Rule ID (UUID)"
// @Param        body  body  handler.ruleRequest  true  "Rule"
// @Success      200  {object}  handler.ruleItem
// @Failure      400,401,403,404  {object}  map[string]string
// @Router       /rules/{id} [put]
func UpdateRule(queries *db.Queries) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := uuidParam(w, r, "id")
		if !ok {
			return
		}
		params, ok := decodeRule(w, r, queries)
		if !ok {
			return
		}
		rule, err := queries.UpdateRule(r.Context(), db.UpdateRuleParams{
			ID: id, Kind: params.Kind, ListID: params.ListID, CityContains: params.CityContains, Enabled: params.Enabled, Note: params.Note,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "rule not found")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to update rule")
			return
		}
		writeJSON(w, http.StatusOK, ruleItem(rule))
	}
}

// DeleteRule handles DELETE /rules/{id}.
//
// @Summary      Delete a subscription rule
// @Description  Requires the manage-rules role. Subscriptions the rule created stay; their rule_id becomes null.
// @Tags         rules
// @Security     BearerAuth
// @Param        id  path  string  true  "Rule ID (UUID)"
// @Success      204
// @Failure      400,401,403,404  {object}  map[string]string
// @Router       /rules/{id} [delete]
func DeleteRule(queries *db.Queries) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := uuidParam(w, r, "id")
		if !ok {
			return
		}
		deleted, err := queries.DeleteRule(r.Context(), id)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to delete rule")
			return
		}
		if deleted == 0 {
			writeError(w, http.StatusNotFound, "rule not found")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// decodeRule decodes and validates a ruleRequest, answering 400 itself when
// it's invalid.
func decodeRule(w http.ResponseWriter, r *http.Request, queries *db.Queries) (db.CreateRuleParams, bool) {
	var body ruleRequest
	if !decodeJSON(w, r, &body) {
		return db.CreateRuleParams{}, false
	}
	params := db.CreateRuleParams{
		Kind:         body.Kind,
		ListID:       body.ListID,
		CityContains: trimmedOrNil(body.CityContains),
		Enabled:      body.Enabled == nil || *body.Enabled,
		Note:         trimmedOrNil(body.Note),
	}

	message := ""
	switch {
	case params.Kind != "all" && params.Kind != "city":
		message = "kind must be all or city"
	case params.Kind == "city" && params.CityContains == nil:
		message = "city_contains is required for kind city"
	case params.Kind == "all" && params.CityContains != nil:
		message = "city_contains must be empty for kind all"
	case !params.ListID.Valid:
		message = "list_id is required"
	}
	if message == "" {
		if active, err := queries.ListIsActive(r.Context(), params.ListID); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to check list")
			return params, false
		} else if !active {
			message = "list_id must be an existing, not deleted list"
		}
	}
	if message != "" {
		writeError(w, http.StatusBadRequest, message)
		return params, false
	}
	return params, true
}

func trimmedOrNil(s *string) *string {
	if s == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*s)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}
