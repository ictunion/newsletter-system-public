// Package orca is a client for orca's newsletter sync endpoint, used by the
// daily reconciliation (docs/architecture.md, "orca reconciliation").
package orca

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/kubik/newsletter-system/internal/members"
)

type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

// New returns a client for orca at apiURL, authenticating with the shared
// sync token as a bearer token.
func New(apiURL, syncToken string) *Client {
	return &Client{baseURL: strings.TrimRight(apiURL, "/"), token: syncToken, http: &http.Client{Timeout: time.Minute}}
}

// ListMembers returns every active member, with the POST /members fields
// (GET /sync/newsletter/members, a JSON array).
func (c *Client) ListMembers(ctx context.Context) ([]members.Member, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/sync/newsletter/members", nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+c.token)
	response, err := c.http.Do(request)
	if err != nil {
		return nil, fmt.Errorf("fetching orca members: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetching orca members: unexpected status %d", response.StatusCode)
	}
	var list []members.Member
	if err := json.NewDecoder(response.Body).Decode(&list); err != nil {
		return nil, fmt.Errorf("decoding orca members: %w", err)
	}
	return list, nil
}
