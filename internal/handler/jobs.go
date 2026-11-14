package handler

import (
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/kubik/newsletter-system/internal/db"
)

const (
	jobsDefaultLimit = 100
	jobsMaxLimit     = 500
)

type jobsResponse struct {
	Total  int64     `json:"total"`
	Limit  int32     `json:"limit"`
	Offset int32     `json:"offset"`
	Jobs   []jobItem `json:"jobs"`
}

type jobItem struct {
	ID                   pgtype.UUID `json:"id" swaggertype:"string" format:"uuid"`
	Kind                 string      `json:"kind" enums:"push_subscriber,delete_subscriber"`
	SubscriberID         pgtype.UUID `json:"subscriber_id" swaggertype:"string" format:"uuid"`
	ListmonkSubscriberID *int32      `json:"listmonk_subscriber_id"`
	Status               string      `json:"status" enums:"pending,dead"`
	AttemptCount         int32       `json:"attempt_count"`
	NextAttemptAt        time.Time   `json:"next_attempt_at"`
	LastError            *string     `json:"last_error"`
	CreatedAt            time.Time   `json:"created_at"`
	UpdatedAt            time.Time   `json:"updated_at"`
	// The push job's subscriber, for display; null for delete jobs.
	SubscriberEmail        *string `json:"subscriber_email"`
	SubscriberMemberNumber *int32  `json:"subscriber_member_number"`
}

// ListJobs handles GET /jobs.
//
// @Summary      List provider jobs
// @Description  Requires the view-newsletters role. Jobs that push our state to Listmonk, newest first. A job is deleted once it succeeds; a dead one gave up after 10 attempts and waits for a retry.
// @Tags         jobs
// @Security     BearerAuth
// @Produce      json
// @Param        status  query  string  false  "Only this status"  Enums(pending, dead)
// @Param        limit   query  int     false  "Max results, default 100, capped at 500"
// @Param        offset  query  int     false  "Default 0"
// @Success      200  {object}  handler.jobsResponse
// @Failure      400,401,403  {object}  map[string]string
// @Router       /jobs [get]
func ListJobs(queries *db.Queries) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		var params db.ListJobsParams
		if s := query.Get("status"); s != "" {
			if s != "pending" && s != "dead" {
				writeError(w, http.StatusBadRequest, "status must be pending or dead")
				return
			}
			params.Status = &s
		}
		var ok bool
		if params.Lim, params.Off, ok = pageParams(w, query, jobsDefaultLimit, jobsMaxLimit); !ok {
			return
		}
		rows, err := queries.ListJobs(r.Context(), params)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to list jobs")
			return
		}
		response := jobsResponse{Limit: params.Lim, Offset: params.Off, Jobs: make([]jobItem, 0, len(rows))}
		for _, row := range rows {
			response.Total = row.TotalCount
			response.Jobs = append(response.Jobs, jobItem{
				ID: row.ID, Kind: row.Kind, SubscriberID: row.SubscriberID, ListmonkSubscriberID: row.ListmonkSubscriberID,
				Status: row.Status, AttemptCount: row.AttemptCount, NextAttemptAt: row.NextAttemptAt, LastError: row.LastError,
				CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
				SubscriberEmail: row.SubscriberEmail, SubscriberMemberNumber: row.SubscriberMemberNumber,
			})
		}
		writeJSON(w, http.StatusOK, response)
	}
}

// RetryJob handles POST /jobs/{id}/retry.
//
// @Summary      Retry a dead job
// @Description  Requires the manage-newsletters role. Puts a dead job back in the queue, due now, with a fresh attempt count, and returns it. 409 when the job is not dead.
// @Tags         jobs
// @Security     BearerAuth
// @Produce      json
// @Param        id  path  string  true  "Job ID (UUID)"
// @Success      200  {object}  handler.jobItem
// @Failure      400,401,403,404,409  {object}  map[string]string
// @Router       /jobs/{id}/retry [post]
func RetryJob(queries *db.Queries) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := uuidParam(w, r, "id")
		if !ok {
			return
		}
		job, err := queries.RetryJob(r.Context(), id)
		if errors.Is(err, pgx.ErrNoRows) {
			if _, err := queries.GetJob(r.Context(), id); errors.Is(err, pgx.ErrNoRows) {
				writeError(w, http.StatusNotFound, "job not found")
			} else {
				writeError(w, http.StatusConflict, "job is not dead")
			}
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to retry job")
			return
		}
		writeJSON(w, http.StatusOK, jobItem{
			ID: job.ID, Kind: job.Kind, SubscriberID: job.SubscriberID, ListmonkSubscriberID: job.ListmonkSubscriberID,
			Status: job.Status, AttemptCount: job.AttemptCount, NextAttemptAt: job.NextAttemptAt, LastError: job.LastError,
			CreatedAt: job.CreatedAt, UpdatedAt: job.UpdatedAt,
		})
	}
}
