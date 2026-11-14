package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kubik/newsletter-system/internal/db"
	"github.com/kubik/newsletter-system/internal/dbtest"
	"github.com/kubik/newsletter-system/internal/members"
)

func TestJobs(t *testing.T) {
	pool := dbtest.Pool(t)
	queries := db.New(pool)
	serve(t, CreateMember(pool, members.Service{PushEnabled: true}), http.MethodPost, janBody, nil)
	mustExec(t, pool, `UPDATE provider_jobs SET status = 'dead', attempt_count = 10, last_error = 'boom'`)
	if err := queries.EnqueueDeleteSubscriber(context.Background(), new(int32(42))); err != nil {
		t.Fatal(err)
	}

	list := func(query string) (int, jobsResponse) {
		recorder := httptest.NewRecorder()
		ListJobs(queries)(recorder, httptest.NewRequest(http.MethodGet, "/jobs"+query, nil))
		var response jobsResponse
		if recorder.Code == http.StatusOK {
			if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
		}
		return recorder.Code, response
	}
	retry := func(id string) (int, jobItem) {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/jobs/"+id+"/retry", nil)
		request.SetPathValue("id", id)
		RetryJob(queries)(recorder, request)
		var job jobItem
		if recorder.Code == http.StatusOK {
			if err := json.Unmarshal(recorder.Body.Bytes(), &job); err != nil {
				t.Fatal(err)
			}
		}
		return recorder.Code, job
	}

	code, dead := list("?status=dead")
	if code != http.StatusOK || dead.Total != 1 || len(dead.Jobs) != 1 {
		t.Fatalf("dead: code = %d, response = %+v", code, dead)
	}
	if job := dead.Jobs[0]; job.Kind != "push_subscriber" || *job.SubscriberEmail != "jan@example.com" || *job.SubscriberMemberNumber != 834 ||
		job.AttemptCount != 10 || *job.LastError != "boom" {
		t.Errorf("dead job = %+v", job)
	}
	if _, all := list(""); all.Total != 2 {
		t.Errorf("all jobs = %d, want 2", all.Total)
	}
	if code, _ := list("?status=done"); code != http.StatusBadRequest {
		t.Errorf("bad status: code = %d, want 400", code)
	}

	deadID := dead.Jobs[0].ID.String()
	code, retried := retry(deadID)
	if code != http.StatusOK || retried.Status != "pending" || retried.AttemptCount != 0 || *retried.LastError != "boom" {
		t.Errorf("retry: code = %d, job = %+v", code, retried)
	}
	if code, _ := retry(deadID); code != http.StatusConflict {
		t.Errorf("retry pending job: code = %d, want 409", code)
	}
	if code, _ := retry("00000000-0000-0000-0000-000000000000"); code != http.StatusNotFound {
		t.Errorf("retry unknown job: code = %d, want 404", code)
	}
}
