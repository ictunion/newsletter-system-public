package handler

import (
	"context"
	"log"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/kubik/newsletter-system/internal/db"
)

// syncRunsPerKind is how many recent runs GET /sync-runs returns per kind.
const syncRunsPerKind = 10

type syncRunsResponse struct {
	Runs []syncRunItem `json:"runs"`
}

type syncRunItem struct {
	ID              pgtype.UUID        `json:"id" swaggertype:"string" format:"uuid"`
	Kind            string             `json:"kind"`
	Status          string             `json:"status"`
	StartedAt       time.Time          `json:"started_at"`
	FinishedAt      pgtype.Timestamptz `json:"finished_at" swaggertype:"string" format:"date-time"`
	ItemsFetched    int32              `json:"items_fetched"`
	ChangesDetected int32              `json:"changes_detected"`
	Error           *string            `json:"error"`
}

// ListSyncRuns handles GET /sync-runs.
//
// @Summary      List recent sync runs
// @Description  Requires the view-newsletters role. The latest 10 runs of each sync kind, grouped by kind, newest first.
// @Tags         sync-runs
// @Security     BearerAuth
// @Produce      json
// @Success      200  {object}  handler.syncRunsResponse
// @Failure      401,403  {object}  map[string]string
// @Router       /sync-runs [get]
func ListSyncRuns(queries *db.Queries) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rows, err := queries.ListRecentSyncRuns(r.Context(), syncRunsPerKind)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to list sync runs")
			return
		}
		response := syncRunsResponse{Runs: make([]syncRunItem, 0, len(rows))}
		for _, row := range rows {
			response.Runs = append(response.Runs, syncRunItem(row))
		}
		writeJSON(w, http.StatusOK, response)
	}
}

// Syncs maps each enabled sync kind (sync_runs.kind) to the function that
// runs it. A kind whose provider isn't configured is left out.
type Syncs map[string]func(context.Context) error

// TriggerSync handles POST /sync-runs/{kind}. The sync runs in the
// background under appContext (cancelled on shutdown), since it can outlast
// the request; its outcome shows up in GET /sync-runs.
//
// @Summary      Start a sync now
// @Description  Requires the manage-newsletters role. Starts the sync in the background and answers 202; the run shows up in GET /sync-runs. 404 for an unknown kind or one whose provider is not configured, 409 while a run of that kind is still running.
// @Tags         sync-runs
// @Security     BearerAuth
// @Param        kind  path  string  true  "Sync kind"  Enums(listmonk_lists, listmonk_subscriptions)
// @Success      202
// @Failure      401,403,404,409  {object}  map[string]string
// @Router       /sync-runs/{kind} [post]
func TriggerSync(appContext context.Context, queries *db.Queries, syncs Syncs) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		kind := r.PathValue("kind")
		sync, ok := syncs[kind]
		if !ok {
			writeError(w, http.StatusNotFound, "unknown or disabled sync kind")
			return
		}
		// The sync_runs_running_idx index is the real guard; this check only
		// lets the caller see a 409 instead of a run that fails in the log.
		running, err := queries.SyncRunIsRunning(r.Context(), kind)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to check sync runs")
			return
		}
		if running {
			writeError(w, http.StatusConflict, "sync already running")
			return
		}
		go func() {
			if err := sync(appContext); err != nil {
				log.Printf("sync: %s (manual): %v", kind, err)
			}
		}()
		w.WriteHeader(http.StatusAccepted)
	}
}
