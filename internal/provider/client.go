package provider

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

// client is a thin wrapper around autobrr's REST API. Auth is a single
// static X-API-Token header - no session/OAuth handling needed once a key
// exists (bootstrapping the first key is a one-time manual step, done via
// autobrr's own UI - see terraform/live/autobrr's README).
type client struct {
	endpoint   string
	apiToken   string
	httpClient *http.Client
}

func newClient(endpoint, apiToken string) *client {
	return &client{
		endpoint:   endpoint,
		apiToken:   apiToken,
		httpClient: &http.Client{Timeout: 15 * time.Second},
	}
}

func (c *client) do(method, path string, body any, out any) error {
	var reqBody io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal request body: %w", err)
		}
		reqBody = bytes.NewReader(b)
	}

	req, err := http.NewRequest(method, c.endpoint+path, reqBody)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("X-API-Token", c.apiToken)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("request %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response body: %w", err)
	}

	if resp.StatusCode >= 300 {
		return fmt.Errorf("%s %s returned %d: %s", method, path, resp.StatusCode, string(respBody))
	}

	if out != nil && len(respBody) > 0 {
		if err := json.Unmarshal(respBody, out); err != nil {
			return fmt.Errorf("decode response from %s %s: %w", method, path, err)
		}
	}
	return nil
}

// --- IRC networks ---
// GET redacts pass/auth.password as the literal string "<redacted>" -
// callers (resource Read()) must never write that back into a real
// password field. See resource_irc_network.go's Read().

type ircAuth struct {
	Mechanism string `json:"mechanism,omitempty"`
	Account   string `json:"account,omitempty"`
	Password  string `json:"password,omitempty"`
}

type ircChannel struct {
	ID       int64  `json:"id,omitempty"`
	Enabled  bool   `json:"enabled"`
	Name     string `json:"name"`
	Password string `json:"password"`
}

type ircNetwork struct {
	ID            int64        `json:"id"`
	Name          string       `json:"name"`
	Enabled       bool         `json:"enabled"`
	Server        string       `json:"server"`
	Port          int          `json:"port"`
	TLS           bool         `json:"tls"`
	TLSSkipVerify bool         `json:"tls_skip_verify"`
	Pass          string       `json:"pass"`
	Nick          string       `json:"nick"`
	Auth          ircAuth      `json:"auth"`
	InviteCommand string       `json:"invite_command"`
	Channels      []ircChannel `json:"channels"`
}

func (c *client) getIRCNetwork(id int64) (*ircNetwork, error) {
	var n ircNetwork
	if err := c.do(http.MethodGet, "/api/irc/network/"+strconv.FormatInt(id, 10), nil, &n); err != nil {
		return nil, err
	}
	return &n, nil
}

func (c *client) createIRCNetwork(n *ircNetwork) (*ircNetwork, error) {
	var out ircNetwork
	if err := c.do(http.MethodPost, "/api/irc", n, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *client) updateIRCNetwork(n *ircNetwork) error {
	return c.do(http.MethodPut, "/api/irc/network/"+strconv.FormatInt(n.ID, 10), n, nil)
}

func (c *client) deleteIRCNetwork(id int64) error {
	return c.do(http.MethodDelete, "/api/irc/network/"+strconv.FormatInt(id, 10), nil, nil)
}

// --- Download clients ---
// GET redacts settings.apikey/password the same way - same "preserve prior
// state" rule applies in resource_download_client.go's Read().

type downloadClientSettings struct {
	APIKey string `json:"apikey,omitempty"`
}

type downloadClient struct {
	ID       int32                  `json:"id"`
	Name     string                 `json:"name"`
	Type     string                 `json:"type"`
	Enabled  bool                   `json:"enabled"`
	Host     string                 `json:"host"`
	Port     int                    `json:"port"`
	TLS      bool                   `json:"tls"`
	Settings downloadClientSettings `json:"settings"`
}

func (c *client) getDownloadClient(id int32) (*downloadClient, error) {
	var d downloadClient
	if err := c.do(http.MethodGet, "/api/download_clients/"+strconv.Itoa(int(id)), nil, &d); err != nil {
		return nil, err
	}
	return &d, nil
}

func (c *client) createDownloadClient(d *downloadClient) (*downloadClient, error) {
	var out downloadClient
	if err := c.do(http.MethodPost, "/api/download_clients", d, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// updateDownloadClient: autobrr's update route is PUT /api/download_clients
// (no /{id} - the id rides in the body), confirmed from
// internal/http/download_client.go's route table.
func (c *client) updateDownloadClient(d *downloadClient) error {
	return c.do(http.MethodPut, "/api/download_clients", d, nil)
}

func (c *client) deleteDownloadClient(id int32) error {
	return c.do(http.MethodDelete, "/api/download_clients/"+strconv.Itoa(int(id)), nil, nil)
}

// --- Actions ---
// PUT /api/actions/{id} is a confirmed live bug: it calls the same
// internal Store() as create, silently inserting a new row instead of
// updating in place. resource_action.go's Update() works around this by
// deleting the old action and creating a new one - see that file.

type action struct {
	ID                       int    `json:"id"`
	Name                     string `json:"name"`
	Type                     string `json:"type"`
	Enabled                  bool   `json:"enabled"`
	FilterID                 int    `json:"filter_id"`
	ClientID                 int32  `json:"client_id,omitempty"`
	ExternalDownloadClientID int32  `json:"external_download_client_id,omitempty"`
	Category                 string `json:"category,omitempty"`
	DownloadPath             string `json:"download_path,omitempty"`
}

// listActions fetches every action across every filter - autobrr has no
// GET-by-id or GET-by-filter route for actions, only the flat collection.
func (c *client) listActions() ([]action, error) {
	var actions []action
	if err := c.do(http.MethodGet, "/api/actions", nil, &actions); err != nil {
		return nil, err
	}
	return actions, nil
}

func (c *client) getAction(id int) (*action, error) {
	actions, err := c.listActions()
	if err != nil {
		return nil, err
	}
	for i := range actions {
		if actions[i].ID == id {
			return &actions[i], nil
		}
	}
	return nil, nil // not found - caller treats as deleted
}

func (c *client) createAction(a *action) (*action, error) {
	var out action
	if err := c.do(http.MethodPost, "/api/actions", a, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *client) deleteAction(id int) error {
	return c.do(http.MethodDelete, "/api/actions/"+strconv.Itoa(id), nil, nil)
}

// --- Filters ---
// Narrow schema by design (autobrr's real filter table has ~70 columns) -
// this provider only manages enabled/indexer-scope/match-basics. Anything
// else (quality-tuning fields like genre tags) stays live-editable via the
// UI without Terraform fighting it.

type filterIndexerRef struct {
	ID int `json:"id"`
}

type filter struct {
	ID             int                `json:"id"`
	Name           string             `json:"name"`
	Enabled        bool               `json:"enabled"`
	Indexers       []filterIndexerRef `json:"indexers"`
	Resolutions    []string           `json:"resolutions"`
	AnnounceTypes  []string           `json:"announce_types"`
	ExceptReleases string             `json:"except_releases"`
	// Codecs/Sources/Containers are NOT part of this provider's schema (out
	// of scope for the narrow field set - see resource_filter.go) but
	// autobrr's filter table has NOT NULL constraints on all three with no
	// server-side default applied when the field is omitted from the
	// request entirely (confirmed live: POST /api/filters 500s with
	// "NOT NULL constraint failed: filter.codecs" otherwise) - always sent
	// as empty arrays, never omitted, never touched by Terraform.
	Codecs     []string `json:"codecs"`
	Sources    []string `json:"sources"`
	Containers []string `json:"containers"`
}

func (c *client) getFilter(id int) (*filter, error) {
	var f filter
	if err := c.do(http.MethodGet, "/api/filters/"+strconv.Itoa(id), nil, &f); err != nil {
		return nil, err
	}
	return &f, nil
}

func (c *client) createFilter(f *filter) (*filter, error) {
	var out filter
	if err := c.do(http.MethodPost, "/api/filters", f, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *client) updateFilter(f *filter) error {
	return c.do(http.MethodPut, "/api/filters/"+strconv.Itoa(f.ID), f, nil)
}

func (c *client) setFilterEnabled(id int, enabled bool) error {
	return c.do(http.MethodPut, "/api/filters/"+strconv.Itoa(id)+"/enabled", map[string]bool{"enabled": enabled}, nil)
}

func (c *client) deleteFilter(id int) error {
	return c.do(http.MethodDelete, "/api/filters/"+strconv.Itoa(id), nil, nil)
}
