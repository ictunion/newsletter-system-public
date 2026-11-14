// Package listmonk is a thin client for the parts of the Listmonk API
// (https://listmonk.app/docs/apis/) the worker and the Listmonk syncs use.
// Behavior noted here was checked against Listmonk v6.2 (see
// docs/newsletter_service_plan.md, "Listmonk details to verify").
package listmonk

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

var (
	// ErrDuplicateEmail is returned by CreateSubscriber when Listmonk already
	// has a subscriber with that email (Listmonk answers 409, case-insensitively).
	ErrDuplicateEmail = errors.New("listmonk: email already exists")
	// ErrNotFound is returned by GetSubscriber and FindSubscriberByEmail.
	ErrNotFound = errors.New("listmonk: subscriber not found")
)

// ListAction is the action of ManageLists.
type ListAction string

const (
	// ListAdd adds memberships as confirmed. It also resubscribes an
	// unsubscribed membership, so never send it for our unsubscribed rows.
	ListAdd         ListAction = "add"
	ListUnsubscribe ListAction = "unsubscribe"
	ListRemove      ListAction = "remove"
)

type Client struct {
	baseURL string
	auth    string
	http    *http.Client
}

// New returns a client for host (e.g. http://localhost:9000) authenticating
// as an API user (Authorization: token <user>:<token>, Listmonk v4+).
func New(host, apiUser, apiToken string) *Client {
	return &Client{
		baseURL: strings.TrimRight(host, "/") + "/api",
		auth:    "token " + apiUser + ":" + apiToken,
		http:    &http.Client{Timeout: 30 * time.Second},
	}
}

type List struct {
	ID              int    `json:"id"`
	Name            string `json:"name"`
	Description     string `json:"description"`
	SubscriberCount int    `json:"subscriber_count"`
}

type Subscriber struct {
	ID      int              `json:"id"`
	Email   string           `json:"email"`
	Name    string           `json:"name"`
	Status  string           `json:"status"` // enabled, disabled or blocklisted
	Attribs map[string]any   `json:"attribs"`
	Lists   []SubscriberList `json:"lists"`
}

type SubscriberList struct {
	ID                 int    `json:"id"`
	SubscriptionStatus string `json:"subscription_status"` // unconfirmed, confirmed or unsubscribed
}

// apiError is a non-2xx Listmonk response.
type apiError struct {
	method, path string
	status       int
	message      string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("listmonk %s %s: status %d: %s", e.method, e.path, e.status, e.message)
}

func statusOf(err error) int {
	var apiErr *apiError
	if errors.As(err, &apiErr) {
		return apiErr.status
	}
	return 0
}

// do sends body (if non-nil) as JSON and decodes the response's "data" field
// into out (if non-nil).
func (c *Client) do(ctx context.Context, method, path string, query url.Values, body, out any) error {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encoding listmonk request: %w", err)
		}
		reader = bytes.NewReader(encoded)
	}
	target := c.baseURL + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	request, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", c.auth)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.http.Do(request)
	if err != nil {
		return fmt.Errorf("listmonk %s %s: %w", method, path, err)
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode < 200 || response.StatusCode > 299 {
		var failure struct {
			Message string `json:"message"`
		}
		_ = json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&failure)
		return &apiError{method: method, path: path, status: response.StatusCode, message: failure.Message}
	}
	if out == nil {
		return nil
	}
	envelope := struct {
		Data any `json:"data"`
	}{Data: out}
	if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
		return fmt.Errorf("decoding listmonk %s %s: %w", method, path, err)
	}
	return nil
}

func (c *Client) ListLists(ctx context.Context) ([]List, error) {
	var page struct {
		Results []List `json:"results"`
	}
	err := c.do(ctx, http.MethodGet, "/lists", url.Values{"per_page": {"all"}}, nil, &page)
	return page.Results, err
}

// ListSubscribersByList returns every subscriber with a membership on the
// list, in any subscription status, each with all of its memberships.
func (c *Client) ListSubscribersByList(ctx context.Context, listID int) ([]Subscriber, error) {
	return c.querySubscribers(ctx, url.Values{"list_id": {strconv.Itoa(listID)}, "per_page": {"all"}})
}

func (c *Client) querySubscribers(ctx context.Context, query url.Values) ([]Subscriber, error) {
	var page struct {
		Results []Subscriber `json:"results"`
	}
	err := c.do(ctx, http.MethodGet, "/subscribers", query, nil, &page)
	return page.Results, err
}

// findOne runs a Listmonk subscriber SQL expression expected to match at
// most one subscriber.
func (c *Client) findOne(ctx context.Context, expression string) (Subscriber, error) {
	results, err := c.querySubscribers(ctx, url.Values{"query": {expression}})
	if err != nil {
		return Subscriber{}, err
	}
	if len(results) == 0 {
		return Subscriber{}, ErrNotFound
	}
	return results[0], nil
}

// GetSubscriber looks the subscriber up by query rather than
// GET /subscribers/{id}, which answers a missing ID with 400, not 404.
func (c *Client) GetSubscriber(ctx context.Context, id int) (Subscriber, error) {
	return c.findOne(ctx, "subscribers.id="+strconv.Itoa(id))
}

// FindSubscriberByEmail matches case-insensitively: Listmonk stores emails
// lowercased. The query is a raw SQL expression inside Listmonk, so single
// quotes are doubled to keep the email a string literal.
func (c *Client) FindSubscriberByEmail(ctx context.Context, email string) (Subscriber, error) {
	literal := strings.ReplaceAll(strings.ToLower(email), "'", "''")
	return c.findOne(ctx, "subscribers.email='"+literal+"'")
}

type subscriberBody struct {
	Email                   string         `json:"email"`
	Name                    string         `json:"name"`
	Status                  string         `json:"status"`
	Attribs                 map[string]any `json:"attribs"`
	Lists                   []int          `json:"lists"`
	PreconfirmSubscriptions bool           `json:"preconfirm_subscriptions,omitempty"`
}

// CreateSubscriber creates an enabled subscriber with confirmed memberships
// on listIDs, so no double-opt-in email goes out. A nil attribs is sent as {}.
func (c *Client) CreateSubscriber(ctx context.Context, email, name string, attribs map[string]any, listIDs []int) (Subscriber, error) {
	if attribs == nil {
		attribs = map[string]any{}
	}
	if listIDs == nil {
		listIDs = []int{}
	}
	var created Subscriber
	err := c.do(ctx, http.MethodPost, "/subscribers", nil, subscriberBody{
		Email: email, Name: name, Status: "enabled", Attribs: attribs, Lists: listIDs, PreconfirmSubscriptions: true,
	}, &created)
	if statusOf(err) == http.StatusConflict {
		return Subscriber{}, ErrDuplicateEmail
	}
	return created, err
}

// UpdateSubscriber changes email, name and attribs of current, which must
// come fresh from GetSubscriber. Listmonk's PUT replaces the membership set
// with the lists sent (omitting them clears every membership) and sets the
// status sent, so current's lists and status are sent back unchanged.
// Statuses of kept memberships, including unsubscribed, are preserved.
// Change memberships with ManageLists only.
//
// shortcut: a membership added in the Listmonk UI between GetSubscriber and
// this call is dropped; fine at our edit rate, revisit if that changes.
func (c *Client) UpdateSubscriber(ctx context.Context, current Subscriber, email, name string, attribs map[string]any) error {
	if attribs == nil {
		attribs = map[string]any{}
	}
	listIDs := make([]int, 0, len(current.Lists))
	for _, list := range current.Lists {
		listIDs = append(listIDs, list.ID)
	}
	return c.do(ctx, http.MethodPut, "/subscribers/"+strconv.Itoa(current.ID), nil, subscriberBody{
		Email: email, Name: name, Status: current.Status, Attribs: attribs, Lists: listIDs,
	}, nil)
}

// ManageLists applies action to the subscriber's memberships on listIDs.
// An empty listIDs sends nothing. Unsubscribe and remove on a list the
// subscriber is not on are no-ops in Listmonk.
func (c *Client) ManageLists(ctx context.Context, subscriberID int, action ListAction, listIDs []int) error {
	if len(listIDs) == 0 {
		return nil
	}
	body := map[string]any{"ids": []int{subscriberID}, "action": action, "target_list_ids": listIDs}
	if action == ListAdd {
		body["status"] = "confirmed"
	}
	return c.do(ctx, http.MethodPut, "/subscribers/lists", nil, body, nil)
}

// DeleteSubscriber treats a missing subscriber as deleted. Listmonk v6
// answers 200 for a missing ID; 404 is accepted too in case that changes.
func (c *Client) DeleteSubscriber(ctx context.Context, id int) error {
	err := c.do(ctx, http.MethodDelete, "/subscribers/"+strconv.Itoa(id), nil, nil, nil)
	if statusOf(err) == http.StatusNotFound {
		return nil
	}
	return err
}
