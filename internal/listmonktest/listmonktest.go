// Package listmonktest runs a fake Listmonk API with in-memory state, for
// tests of code that drives internal/listmonk. It mimics the Listmonk v6.2
// behavior recorded in docs/architecture.md ("Listmonk behavior we rely on"),
// the same way internal/keycloaktest fakes Keycloak.
package listmonktest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/kubik/newsletter-system/internal/listmonk"
)

// Server is the fake. Lock Mu to read or change its state from a test while
// requests may be running.
type Server struct {
	Mu sync.Mutex
	// Lists are returned by GET /api/lists (SubscriberCount is computed).
	Lists []listmonk.List
	// Subscribers by ID.
	Subscribers map[int]*listmonk.Subscriber
	// FailWith, when non-zero, answers every request with this status.
	FailWith int
	// Requests records "METHOD /path" of every request.
	Requests []string

	nextID int
}

// New starts the fake and returns it with a client pointed at it.
func New(t *testing.T) (*Server, *listmonk.Client) {
	t.Helper()
	fake := &Server{Subscribers: map[int]*listmonk.Subscriber{}, nextID: 1}
	server := httptest.NewServer(http.HandlerFunc(fake.serve))
	t.Cleanup(server.Close)
	return fake, listmonk.New(server.URL, "user", "token")
}

// AddSubscriber stores a subscriber as if created in the Listmonk UI and
// returns its ID. memberships maps list ID to subscription status.
func (s *Server) AddSubscriber(email, name, status string, memberships map[int]string) int {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	subscriber := &listmonk.Subscriber{ID: s.nextID, Email: strings.ToLower(email), Name: name, Status: status, Attribs: map[string]any{}}
	s.nextID++
	for listID, subscriptionStatus := range memberships {
		subscriber.Lists = append(subscriber.Lists, listmonk.SubscriberList{ID: listID, SubscriptionStatus: subscriptionStatus})
	}
	s.Subscribers[subscriber.ID] = subscriber
	return subscriber.ID
}

// Memberships returns subscriber id's list ID -> subscription status.
func (s *Server) Memberships(id int) map[int]string {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	memberships := map[int]string{}
	if subscriber, ok := s.Subscribers[id]; ok {
		for _, list := range subscriber.Lists {
			memberships[list.ID] = list.SubscriptionStatus
		}
	}
	return memberships
}

var (
	subscriberPath = regexp.MustCompile(`^/api/subscribers/(\d+)$`)
	idQuery        = regexp.MustCompile(`^subscribers\.id=(\d+)$`)
	emailQuery     = regexp.MustCompile(`^subscribers\.email='((?:[^']|'')*)'$`)
)

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	s.Requests = append(s.Requests, r.Method+" "+r.URL.Path)
	if s.FailWith != 0 {
		fail(w, s.FailWith, "injected failure")
		return
	}

	switch match := subscriberPath.FindStringSubmatch(r.URL.Path); {
	case r.Method == http.MethodGet && r.URL.Path == "/api/lists":
		s.listLists(w)
	case r.Method == http.MethodGet && r.URL.Path == "/api/subscribers":
		s.querySubscribers(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/api/subscribers":
		s.create(w, r)
	case r.Method == http.MethodPut && r.URL.Path == "/api/subscribers/lists":
		s.manageLists(w, r)
	case r.Method == http.MethodPut && match != nil:
		id, _ := strconv.Atoi(match[1])
		s.update(w, r, id)
	case r.Method == http.MethodDelete && match != nil:
		id, _ := strconv.Atoi(match[1])
		delete(s.Subscribers, id)
		ok(w, true)
	default:
		fail(w, http.StatusNotFound, "not implemented in fake")
	}
}

func (s *Server) listLists(w http.ResponseWriter) {
	lists := slices.Clone(s.Lists)
	for i := range lists {
		lists[i].SubscriberCount = 0
		for _, subscriber := range s.Subscribers {
			if slices.ContainsFunc(subscriber.Lists, func(l listmonk.SubscriberList) bool { return l.ID == lists[i].ID }) {
				lists[i].SubscriberCount++
			}
		}
	}
	ok(w, map[string]any{"results": lists})
}

func (s *Server) querySubscribers(w http.ResponseWriter, r *http.Request) {
	results := []listmonk.Subscriber{}
	query, listID := r.URL.Query().Get("query"), r.URL.Query().Get("list_id")
	for _, subscriber := range s.sorted() {
		switch {
		case listID != "":
			id, _ := strconv.Atoi(listID)
			if !slices.ContainsFunc(subscriber.Lists, func(l listmonk.SubscriberList) bool { return l.ID == id }) {
				continue
			}
		case idQuery.MatchString(query):
			if strconv.Itoa(subscriber.ID) != idQuery.FindStringSubmatch(query)[1] {
				continue
			}
		case emailQuery.MatchString(query):
			if subscriber.Email != strings.ReplaceAll(emailQuery.FindStringSubmatch(query)[1], "''", "'") {
				continue
			}
		default:
			fail(w, http.StatusBadRequest, "fake: unsupported query "+query)
			return
		}
		results = append(results, *subscriber)
	}
	ok(w, map[string]any{"results": results})
}

type subscriberBody struct {
	Email      string         `json:"email"`
	Name       string         `json:"name"`
	Status     string         `json:"status"`
	Attribs    map[string]any `json:"attribs"`
	Lists      []int          `json:"lists"`
	Preconfirm bool           `json:"preconfirm_subscriptions"`
}

func (s *Server) create(w http.ResponseWriter, r *http.Request) {
	var body subscriberBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	email := strings.ToLower(body.Email)
	for _, subscriber := range s.Subscribers {
		if subscriber.Email == email {
			fail(w, http.StatusConflict, "E-mail already exists.")
			return
		}
	}
	status := "unconfirmed"
	if body.Preconfirm {
		status = "confirmed"
	}
	subscriber := &listmonk.Subscriber{ID: s.nextID, Email: email, Name: body.Name, Status: body.Status, Attribs: body.Attribs}
	s.nextID++
	for _, listID := range body.Lists {
		subscriber.Lists = append(subscriber.Lists, listmonk.SubscriberList{ID: listID, SubscriptionStatus: status})
	}
	s.Subscribers[subscriber.ID] = subscriber
	ok(w, subscriber)
}

// update replaces the membership set with body.Lists, keeping statuses of
// kept lists; omitting lists clears them, as in Listmonk.
func (s *Server) update(w http.ResponseWriter, r *http.Request, id int) {
	subscriber, found := s.Subscribers[id]
	if !found {
		fail(w, http.StatusBadRequest, "Subscriber ("+strconv.Itoa(id)+": ) not found")
		return
	}
	var body subscriberBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	subscriber.Email, subscriber.Name, subscriber.Status, subscriber.Attribs = strings.ToLower(body.Email), body.Name, body.Status, body.Attribs
	var kept []listmonk.SubscriberList
	for _, listID := range body.Lists {
		status := "unconfirmed"
		if i := slices.IndexFunc(subscriber.Lists, func(l listmonk.SubscriberList) bool { return l.ID == listID }); i >= 0 {
			status = subscriber.Lists[i].SubscriptionStatus
		}
		if body.Status == "blocklisted" {
			status = "unsubscribed"
		}
		kept = append(kept, listmonk.SubscriberList{ID: listID, SubscriptionStatus: status})
	}
	subscriber.Lists = kept
	ok(w, subscriber)
}

func (s *Server) manageLists(w http.ResponseWriter, r *http.Request) {
	var body struct {
		IDs     []int  `json:"ids"`
		Action  string `json:"action"`
		ListIDs []int  `json:"target_list_ids"`
		Status  string `json:"status"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	for _, id := range body.IDs {
		subscriber, found := s.Subscribers[id]
		if !found {
			fail(w, http.StatusInternalServerError, "violates foreign key constraint")
			return
		}
		for _, listID := range body.ListIDs {
			i := slices.IndexFunc(subscriber.Lists, func(l listmonk.SubscriberList) bool { return l.ID == listID })
			switch {
			case body.Action == "add" && i >= 0:
				subscriber.Lists[i].SubscriptionStatus = body.Status
			case body.Action == "add":
				subscriber.Lists = append(subscriber.Lists, listmonk.SubscriberList{ID: listID, SubscriptionStatus: body.Status})
			case body.Action == "unsubscribe" && i >= 0:
				subscriber.Lists[i].SubscriptionStatus = "unsubscribed"
			case body.Action == "remove" && i >= 0:
				subscriber.Lists = slices.Delete(subscriber.Lists, i, i+1)
			}
		}
	}
	ok(w, true)
}

func (s *Server) sorted() []*listmonk.Subscriber {
	subscribers := make([]*listmonk.Subscriber, 0, len(s.Subscribers))
	for _, subscriber := range s.Subscribers {
		subscribers = append(subscribers, subscriber)
	}
	slices.SortFunc(subscribers, func(a, b *listmonk.Subscriber) int { return a.ID - b.ID })
	return subscribers
}

func ok(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
}

func fail(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"message": message})
}
