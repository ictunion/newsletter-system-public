package listmonk

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

type recorded struct {
	method, path string
	query        map[string][]string
	body         map[string]any
}

// fakeListmonk answers every request with status and the JSON body response,
// recording each request.
func fakeListmonk(t *testing.T, status int, response string) (*Client, *[]recorded) {
	t.Helper()
	var requests []recorded
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "token api-user:secret" {
			t.Errorf("Authorization = %q", got)
		}
		request := recorded{method: r.Method, path: r.URL.Path, query: r.URL.Query()}
		if raw, _ := io.ReadAll(r.Body); len(raw) > 0 {
			if err := json.Unmarshal(raw, &request.body); err != nil {
				t.Errorf("request body: %v", err)
			}
		}
		requests = append(requests, request)
		w.WriteHeader(status)
		_, _ = io.WriteString(w, response)
	}))
	t.Cleanup(server.Close)
	return New(server.URL+"/", "api-user", "secret"), &requests
}

func only(t *testing.T, requests *[]recorded) recorded {
	t.Helper()
	if len(*requests) != 1 {
		t.Fatalf("got %d requests, want 1", len(*requests))
	}
	return (*requests)[0]
}

func TestListLists(t *testing.T) {
	client, requests := fakeListmonk(t, 200, `{"data":{"results":[{"id":2,"name":"Praha","description":"Pražská pobočka","subscriber_count":5,"type":"public"}]}}`)
	lists, err := client.ListLists(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if want := []List{{ID: 2, Name: "Praha", Description: "Pražská pobočka", SubscriberCount: 5}}; !reflect.DeepEqual(lists, want) {
		t.Errorf("lists = %+v, want %+v", lists, want)
	}
	request := only(t, requests)
	if request.path != "/api/lists" || request.query["per_page"][0] != "all" {
		t.Errorf("request = %+v", request)
	}
}

func TestListSubscribersByList(t *testing.T) {
	client, requests := fakeListmonk(t, 200, `{"data":{"results":[{"id":3,"email":"a@b.cz","status":"blocklisted","lists":[{"id":1,"subscription_status":"unsubscribed"}]}]}}`)
	subscribers, err := client.ListSubscribersByList(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(subscribers) != 1 || subscribers[0].Status != "blocklisted" || subscribers[0].Lists[0] != (SubscriberList{ID: 1, SubscriptionStatus: "unsubscribed"}) {
		t.Errorf("subscribers = %+v", subscribers)
	}
	request := only(t, requests)
	if request.query["list_id"][0] != "1" || request.query["per_page"][0] != "all" {
		t.Errorf("query = %v", request.query)
	}
}

func TestCreateSubscriber(t *testing.T) {
	client, requests := fakeListmonk(t, 200, `{"data":{"id":9,"email":"a@b.cz"}}`)
	created, err := client.CreateSubscriber(context.Background(), "a@b.cz", "A B", nil, []int{1, 2})
	if err != nil {
		t.Fatal(err)
	}
	if created.ID != 9 {
		t.Errorf("ID = %d", created.ID)
	}
	body := only(t, requests).body
	if body["status"] != "enabled" || body["preconfirm_subscriptions"] != true ||
		!reflect.DeepEqual(body["attribs"], map[string]any{}) || !reflect.DeepEqual(body["lists"], []any{1.0, 2.0}) {
		t.Errorf("body = %v", body)
	}
}

func TestCreateSubscriber_DuplicateEmail(t *testing.T) {
	client, _ := fakeListmonk(t, 409, `{"message":"E-mail already exists."}`)
	if _, err := client.CreateSubscriber(context.Background(), "a@b.cz", "A", nil, nil); !errors.Is(err, ErrDuplicateEmail) {
		t.Errorf("err = %v, want ErrDuplicateEmail", err)
	}
}

func TestFindSubscriberByEmail_EscapesQuotes(t *testing.T) {
	client, requests := fakeListmonk(t, 200, `{"data":{"results":[{"id":3}]}}`)
	found, err := client.FindSubscriberByEmail(context.Background(), "O'Brien@B.cz")
	if err != nil || found.ID != 3 {
		t.Fatalf("found = %+v, err = %v", found, err)
	}
	if got, want := only(t, requests).query["query"][0], "subscribers.email='o''brien@b.cz'"; got != want {
		t.Errorf("query = %q, want %q", got, want)
	}
}

func TestGetSubscriber_NotFound(t *testing.T) {
	client, requests := fakeListmonk(t, 200, `{"data":{"results":[]}}`)
	if _, err := client.GetSubscriber(context.Background(), 7); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
	if got := only(t, requests).query["query"][0]; got != "subscribers.id=7" {
		t.Errorf("query = %q", got)
	}
}

func TestUpdateSubscriber_KeepsListsAndStatus(t *testing.T) {
	client, requests := fakeListmonk(t, 200, `{"data":{}}`)
	current := Subscriber{ID: 3, Status: "blocklisted", Lists: []SubscriberList{{ID: 1, SubscriptionStatus: "unsubscribed"}, {ID: 4}}}
	if err := client.UpdateSubscriber(context.Background(), current, "new@b.cz", "New", map[string]any{"lang": "cs"}); err != nil {
		t.Fatal(err)
	}
	request := only(t, requests)
	if request.method != http.MethodPut || request.path != "/api/subscribers/3" {
		t.Errorf("request = %s %s", request.method, request.path)
	}
	if request.body["status"] != "blocklisted" || !reflect.DeepEqual(request.body["lists"], []any{1.0, 4.0}) ||
		request.body["email"] != "new@b.cz" || request.body["preconfirm_subscriptions"] != nil {
		t.Errorf("body = %v", request.body)
	}
}

func TestManageLists(t *testing.T) {
	tests := []struct {
		action     ListAction
		wantStatus any
	}{
		{ListAdd, "confirmed"},
		{ListUnsubscribe, nil},
		{ListRemove, nil},
	}
	for _, tt := range tests {
		t.Run(string(tt.action), func(t *testing.T) {
			client, requests := fakeListmonk(t, 200, `{"data":true}`)
			if err := client.ManageLists(context.Background(), 3, tt.action, []int{1, 2}); err != nil {
				t.Fatal(err)
			}
			request := only(t, requests)
			if request.path != "/api/subscribers/lists" || request.body["action"] != string(tt.action) || request.body["status"] != tt.wantStatus ||
				!reflect.DeepEqual(request.body["ids"], []any{3.0}) || !reflect.DeepEqual(request.body["target_list_ids"], []any{1.0, 2.0}) {
				t.Errorf("request = %+v", request)
			}
		})
	}
}

func TestManageLists_EmptySendsNothing(t *testing.T) {
	client, requests := fakeListmonk(t, 200, `{"data":true}`)
	if err := client.ManageLists(context.Background(), 3, ListRemove, nil); err != nil {
		t.Fatal(err)
	}
	if len(*requests) != 0 {
		t.Errorf("got %d requests, want 0", len(*requests))
	}
}

func TestDeleteSubscriber(t *testing.T) {
	tests := []struct {
		status  int
		wantErr bool
	}{
		{200, false},
		{404, false},
		{500, true},
	}
	for _, tt := range tests {
		client, requests := fakeListmonk(t, tt.status, `{"data":true}`)
		err := client.DeleteSubscriber(context.Background(), 3)
		if (err != nil) != tt.wantErr {
			t.Errorf("status %d: err = %v, wantErr %v", tt.status, err, tt.wantErr)
		}
		if request := only(t, requests); request.method != http.MethodDelete || request.path != "/api/subscribers/3" {
			t.Errorf("request = %s %s", request.method, request.path)
		}
	}
}

func TestErrorIncludesMessage(t *testing.T) {
	client, _ := fakeListmonk(t, 403, `{"message":"invalid API credentials"}`)
	_, err := client.ListLists(context.Background())
	if err == nil || statusOf(err) != 403 || err.Error() != "listmonk GET /lists: status 403: invalid API credentials" {
		t.Errorf("err = %v", err)
	}
}
