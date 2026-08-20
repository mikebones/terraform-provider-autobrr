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

func (c *client) listIRCNetworks() ([]ircNetwork, error) {
	var networks []ircNetwork
	if err := c.do(http.MethodGet, "/api/irc", nil, &networks); err != nil {
		return nil, err
	}
	return networks, nil
}

// createIRCNetwork: POST /api/irc's handler (internal/http/irc.go
// storeNetwork()) returns 204 No Content on success - NOT the created
// network (confirmed by reading it: `h.encoder.NoContent(w)`, no body at
// all). Decoding an empty body into *ircNetwork silently leaves every
// field at its Go zero value (port 0, tls false, channels nil, enabled
// false...), which is exactly what caused a real "provider produced
// inconsistent result after apply" error creating the Orpheus (OPS)
// network live - terraform correctly detected id/port/tls/channels/
// enabled all reverting to zero and refused to accept it. There is no
// id in the response to re-fetch by, either, so the only way back to the
// real created row is GET /api/irc (list) filtered by name - relies on
// network names being unique, which autobrr's own UI already assumes
// (it has no other way to disambiguate networks either).
func (c *client) createIRCNetwork(n *ircNetwork) (*ircNetwork, error) {
	if err := c.do(http.MethodPost, "/api/irc", n, nil); err != nil {
		return nil, err
	}
	networks, err := c.listIRCNetworks()
	if err != nil {
		return nil, fmt.Errorf("created network but failed to look it up by name: %w", err)
	}
	for i := range networks {
		if networks[i].Name == n.Name {
			return &networks[i], nil
		}
	}
	return nil, fmt.Errorf("created network %q but could not find it in the network list afterward", n.Name)
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
	Label                    string `json:"label,omitempty"`
	DownloadPath             string `json:"download_path,omitempty"`
	Paused                   bool   `json:"paused,omitempty"`
	// WEBHOOK-type fields. autobrr's own WebhookHeaders is deliberately
	// NOT modeled here - confirmed live (2026-08-20, gomission-snatch
	// action) that autobrr's own action-execution code never applies it
	// to the outgoing request at all, a dead field in autobrr's own
	// source, so there's nothing real for this provider to manage.
	WebhookHost   string `json:"webhook_host,omitempty"`
	WebhookType   string `json:"webhook_type,omitempty"`
	WebhookMethod string `json:"webhook_method,omitempty"`
	WebhookData   string `json:"webhook_data,omitempty"`
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

// --- Indexers ---
// GET /api/indexer has no per-id route (only the flat "/" list, same shape
// as actions) - getIndexer lists and filters.
//
// The GET list response is NOT the same shape as what POST/PUT
// send/expect. GET returns domain.IndexerDefinition - settings there is
// an ARRAY of {name, value, ...} objects, the template's schema merged
// with the instance's stored values for display (confirmed live:
// decoding it into the flat map[string]string that POST/PUT actually use
// fails outright with "cannot unmarshal array into Go struct field
// indexer.settings of type map[string]string"). POST/PUT read/write the
// bare domain.Indexer instead, where settings really is a flat map. This
// client has two wire types as a result: indexerListEntry (GET, for
// reading) converts down to the same indexer shape (write) that
// createIndexer/updateIndexer use - callers only ever see the flat type.
//
// identifier is server-generated on CREATE ONLY, from
// slug("implementation-name") (confirmed in autobrr's own
// internal/indexer/service.go Store()) - Update() does NOT regenerate it,
// it just persists whatever identifier the request body carries. So a
// create must leave identifier unset/empty and read back whatever the
// server assigns; an update must always send back the identifier already
// in state, never blank (an empty identifier on update would persist as
// empty, breaking the indexer).

type indexer struct {
	ID                 int64             `json:"id"`
	Name               string            `json:"name"`
	Identifier         string            `json:"identifier"`
	IdentifierExternal string            `json:"identifier_external"`
	Enabled            bool              `json:"enabled"`
	Implementation     string            `json:"implementation"`
	BaseURL            string            `json:"base_url,omitempty"`
	Settings           map[string]string `json:"settings,omitempty"`
}

type indexerListSetting struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type indexerListEntry struct {
	ID                 int64                `json:"id"`
	Name               string               `json:"name"`
	Identifier         string               `json:"identifier"`
	IdentifierExternal string               `json:"identifier_external"`
	Enabled            bool                 `json:"enabled"`
	Implementation     string               `json:"implementation"`
	BaseURL            string               `json:"base_url,omitempty"`
	Settings           []indexerListSetting `json:"settings,omitempty"`
}

func (e indexerListEntry) toIndexer() indexer {
	var settings map[string]string
	if len(e.Settings) > 0 {
		settings = make(map[string]string, len(e.Settings))
		for _, s := range e.Settings {
			settings[s.Name] = s.Value
		}
	}
	return indexer{
		ID:                 e.ID,
		Name:               e.Name,
		Identifier:         e.Identifier,
		IdentifierExternal: e.IdentifierExternal,
		Enabled:            e.Enabled,
		Implementation:     e.Implementation,
		BaseURL:            e.BaseURL,
		Settings:           settings,
	}
}

func (c *client) listIndexers() ([]indexer, error) {
	var entries []indexerListEntry
	if err := c.do(http.MethodGet, "/api/indexer", nil, &entries); err != nil {
		return nil, err
	}
	indexers := make([]indexer, len(entries))
	for i, e := range entries {
		indexers[i] = e.toIndexer()
	}
	return indexers, nil
}

func (c *client) getIndexer(id int64) (*indexer, error) {
	indexers, err := c.listIndexers()
	if err != nil {
		return nil, err
	}
	for i := range indexers {
		if indexers[i].ID == id {
			return &indexers[i], nil
		}
	}
	return nil, nil // not found - caller treats as deleted
}

func (c *client) createIndexer(i *indexer) (*indexer, error) {
	var out indexer
	if err := c.do(http.MethodPost, "/api/indexer", i, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *client) updateIndexer(i *indexer) error {
	return c.do(http.MethodPut, "/api/indexer/"+strconv.FormatInt(i.ID, 10), i, nil)
}

func (c *client) deleteIndexer(id int64) error {
	return c.do(http.MethodDelete, "/api/indexer/"+strconv.FormatInt(id, 10), nil, nil)
}

// --- Feeds ---
// GET /api/feeds is also list-only (no per-id route) - getFeed lists and
// filters, same shape as actions/indexers.
//
// CRITICAL (confirmed live 2026-08-16, see project memory
// autobrr-rss-feed-api-gotchas): POST /api/feeds returns 201 with a
// populated response body even when the feed silently fails to persist -
// autobrr's FeedRepo.Store only ever reads the FLAT IndexerID field
// (domain.Feed.IndexerID, json "indexer_id"). The nested Indexer field
// (json "indexer") is display-only, populated by the server on READ from
// a join - it is never consulted on write. Always set IndexerID; never
// rely on Indexer being read on write, and never round-trip Indexer back
// into a write body.
//
// url/api_key/cookie are NOT redacted on GET (unlike IRC auth_password
// and download-client api_key) - safe to read back directly, no
// preserve-prior-state workaround needed here.
//
// Mirror-image gotcha on READ: GET responses never populate the flat
// IndexerID field at all (confirmed live: absent from the JSON, not just
// zero) - only the nested Indexer join has it there. resource_feed.go's
// feedToModel recovers the real value from Indexer.ID when IndexerID
// comes back empty. So: write with IndexerID (Indexer is ignored),
// read via Indexer.ID (IndexerID is absent) - the two fields are each
// only reliable in one direction, never both.

type feedIndexerRef struct {
	ID                 int    `json:"id"`
	Name               string `json:"name"`
	Identifier         string `json:"identifier"`
	IdentifierExternal string `json:"identifier_external"`
}

type feedSettings struct {
	DownloadType string `json:"download_type,omitempty"`
}

type feed struct {
	ID        int             `json:"id"`
	Name      string          `json:"name"`
	Indexer   *feedIndexerRef `json:"indexer,omitempty"` // read-only, see comment above
	IndexerID int             `json:"indexer_id,omitempty"`
	Type      string          `json:"type"`
	Enabled   bool            `json:"enabled"`
	URL       string          `json:"url"`
	Interval  int             `json:"interval"`
	Timeout   int             `json:"timeout"`
	MaxAge    int             `json:"max_age"`
	ApiKey    string          `json:"api_key,omitempty"`
	Cookie    string          `json:"cookie,omitempty"`
	Settings  *feedSettings   `json:"settings,omitempty"`
}

func (c *client) listFeeds() ([]feed, error) {
	var feeds []feed
	if err := c.do(http.MethodGet, "/api/feeds", nil, &feeds); err != nil {
		return nil, err
	}
	return feeds, nil
}

func (c *client) getFeed(id int) (*feed, error) {
	feeds, err := c.listFeeds()
	if err != nil {
		return nil, err
	}
	for i := range feeds {
		if feeds[i].ID == id {
			return &feeds[i], nil
		}
	}
	return nil, nil // not found - caller treats as deleted
}

// createFeed: POST /api/feeds' handler echoes back its own decoded request
// pointer as the response body (internal/http/feed.go's store()), but
// FeedRepo.Store (internal/database/feed.go) mutates that SAME pointer's
// ID field via `RETURNING id` before the handler writes the response - so
// the id DOES come back correctly despite the request/response being the
// literal same object, no re-list-and-match needed for that part.
//
// What does NOT come back correctly: FeedRepo.Store's INSERT column list
// omits "cookie" and "max_age" entirely (confirmed by reading it) - only
// Update's column list includes them. A fresh create silently drops
// those two fields regardless of what's sent, exactly like
// resource_filter.go's indexer-association gap. resource_feed.go's
// Create() works around this the same way: create, then immediately
// update with the full desired state, then re-fetch.
func (c *client) createFeed(f *feed) (*feed, error) {
	var out feed
	if err := c.do(http.MethodPost, "/api/feeds", f, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *client) updateFeed(f *feed) error {
	return c.do(http.MethodPut, "/api/feeds/"+strconv.Itoa(f.ID), f, nil)
}

func (c *client) deleteFeed(id int) error {
	return c.do(http.MethodDelete, "/api/feeds/"+strconv.Itoa(id), nil, nil)
}
