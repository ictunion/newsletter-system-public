package handler

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	// The status is already sent; a failed body write can't be reported.
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

// dateParam parses an optional YYYY-MM-DD query param: empty means "no
// filter" (a NULL pgtype.Date), ok is false only for a malformed value.
func dateParam(v string) (pgtype.Date, bool) {
	if v == "" {
		return pgtype.Date{}, true
	}
	t, err := time.Parse("2006-01-02", v)
	if err != nil {
		return pgtype.Date{}, false
	}
	return pgtype.Date{Time: t, Valid: true}, true
}

// pageParams parses the optional limit/offset query params: limit defaults
// to defaultLimit and is capped at maxLimit, offset defaults to 0. ok is
// false (and a 400 already written) for a malformed value.
func pageParams(w http.ResponseWriter, query url.Values, defaultLimit, maxLimit int32) (limit, offset int32, ok bool) {
	limit = defaultLimit
	if s := query.Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 {
			writeError(w, http.StatusBadRequest, "limit must be a positive integer")
			return 0, 0, false
		}
		limit = int32(min(n, int(maxLimit)))
	}
	if s := query.Get("offset"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 0 {
			writeError(w, http.StatusBadRequest, "offset must be a non-negative integer")
			return 0, 0, false
		}
		offset = int32(n)
	}
	return limit, offset, true
}
