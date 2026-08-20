# terraform-provider-autobrr

A minimal Terraform provider for [autobrr](https://autobrr.com/), covering
IRC networks, indexers, feeds, download clients (including native `*arr`
push targets like `SONARR`/`RADARR`, not just torrent clients), actions,
and a deliberately narrow filter schema.

**Local-only** - not published to any Terraform registry. Installed via a
filesystem mirror (see below), not `terraform init`'s normal registry
download.

## Why this exists, and why the filter schema is narrow

autobrr's real `filter` table has ~70 columns - most of it fine-grained
quality-tuning (codecs, HDR formats, log scores, release-group allow/deny
lists, etc.) that genuinely benefits from being live-editable in autobrr's
own UI without a `terraform apply` fighting it. This provider's
`autobrr_filter` resource intentionally only manages the *structural*
fields: `enabled`, `indexer_ids`, `resolutions`, `announce_types`,
`except_releases`. Extend it if you need more, but think about whether a
given field is something you actually want Terraform-owned vs.
UI-tunable first.

## Real API quirks this provider works around

autobrr's REST API has a few sharp edges, found the hard way building
this - documented here so they don't get "fixed" back into bugs later:

- **`GET` responses redact `pass`/`auth.password` (IRC networks) and
  `settings.apikey` (download clients)** to the literal string
  `"<redacted>"`. This provider's `Read()` never writes that back into
  state - it always preserves whatever's already there. Practical
  consequence: right after `terraform import`, these fields start empty
  in state (nothing to preserve yet), so the *first* `apply` after an
  import will show a diff setting them to whatever your `.tf` config
  says - expected, one-time, not a bug.
- **`PUT /api/actions/{id}` doesn't update in place** - it calls the same
  internal `Store()` autobrr uses for creation, which silently *inserts a
  new row* instead. `autobrr_action` marks every mutable attribute
  `RequiresReplace()` so Terraform's own destroy-then-create replace
  machinery owns the resulting id change, rather than this provider
  hand-rolling a delete+create inside `Update()` (which was tried first -
  it produces a real `"provider produced inconsistent result"` protocol
  error, since Terraform's plan assumed `id` would stay the same through
  an in-place update).
- **`POST /api/filters` only creates the bare filter row** - indexer
  associations are silently dropped unless a follow-up `PUT` is sent.
  `autobrr_filter`'s `Create()` does the `POST` then immediately follows
  up with a full `PUT` before re-fetching, so a `terraform apply` never
  leaves a filter with an empty `indexer_ids`.
- **`codecs`/`sources`/`containers` are `NOT NULL` columns with no
  server-side default applied when omitted from a request** - even though
  they have a SQL `DEFAULT '{}'`, autobrr's own insert code binds them
  explicitly, so leaving them out of a `POST /api/filters` body 500s with
  `NOT NULL constraint failed`. `autobrr_filter` always sends empty
  arrays for all three, even though none of them are exposed in this
  provider's schema.
- **A feed's link to its indexer is a flat `indexer_id` field, not the
  nested `indexer` object `GET` responses show** (confirmed live
  2026-08-16: `POST /api/feeds` returned `201` with a populated-looking
  body, but the feed silently never persisted a real indexer link and
  vanished on the next read). autobrr's `FeedRepo.Store` only ever reads
  `indexer_id`; the nested `indexer` object is a read-only join the
  server fills in for display. `autobrr_feed`'s schema has no nested
  indexer block at all, on purpose - `indexer_id` is the only thing that
  can ever link a feed to its indexer.
- **A feed's `POST /api/feeds` create path silently drops `cookie` and
  `max_age`** - `FeedRepo.Store`'s `INSERT` column list omits both
  (confirmed by reading it), while `Update`'s column list includes them.
  Same shape as the filter indexer-association gap above:
  `autobrr_feed`'s `Create()` does the `POST` then immediately follows up
  with a full `PUT` before re-fetching.
- **An indexer's `identifier` is server-generated on `CREATE` only**,
  from `slug(implementation-name)` (confirmed in autobrr's own
  `internal/indexer/service.go` `Store()`) - `Update()` does not
  regenerate it, it just persists whatever identifier the request body
  already carries. Only `rss`/`torznab`/`newznab` implementations get
  this auto-generation (`isImplFeed` in autobrr's own code) -
  `autobrr_indexer` clears any configured `identifier` before create for
  those three only. **`irc` is NOT auto-generated at all** - a real
  built-in tracker definition (BTN, PTP, Redacted, Orpheus/OPS, etc.)
  is looked up by `identifier` exactly as configured (`mapIndexer()`
  calls `getDefinitionByName(indexer.Identifier)` directly for
  non-feed implementations), so `identifier` is REQUIRED for `irc` and
  must match the tracker's real identifier from
  `internal/indexer/definitions/*.yaml` in autobrr's own source (e.g.
  `"ops"`, not a made-up value) - get it wrong and the indexer row gets
  created with no parse rules/settings schema attached, silently, no
  error.
  A tracker with no built-in autobrr definition (e.g. a private
  RSS-only tracker) uses `implementation = "rss"` with no `base_url` -
  this is the same shape the UI calls "Generic RSS" when you add one by
  hand; autobrr has no dedicated resource for it, it's a normal indexer
  row like any other.
- **`POST /api/irc` returns `204 No Content` on success - not the
  created network** (confirmed live 2026-08-16 adding Orpheus/OPS: a
  real `"provider produced inconsistent result after apply"` error, with
  `port`/`tls`/`channels`/`enabled` all reverting to their Go zero
  values, because there was no response body to decode in the first
  place). `internal/http/irc.go`'s `storeNetwork()` handler literally
  calls `h.encoder.NoContent(w)`. There's no id in the (empty) response
  to re-fetch by either, so `autobrr_irc_network`'s `createIRCNetwork`
  lists all networks (`GET /api/irc`) and matches by `name` instead -
  relies on network names being unique, same assumption autobrr's own UI
  already makes (it has no other way to disambiguate networks either).
- **An indexer's `settings` can't be trusted from ANY API response, on
  create or read** - `GET /api/indexer` redacts `type: "secret"` setting
  values to `"<redacted>"` (confirmed live: OPS's real `torrent_pass`/
  `api_key` came back that way), and even the `POST`/`PUT` response
  isn't safe either (a real "provider produced inconsistent result"
  apply error on `settings` specifically, creating OPS). `autobrr_indexer`
  now applies the same "always preserve from prior state/plan, never
  trust the API" rule this provider already uses for IRC
  `auth_password` and download-client `api_key` - see `indexerToModel`'s
  doc comment.
- **`WEBHOOK`-type actions' `webhook_host`/`webhook_data`/etc. are, unlike
  every secret field above, genuinely NOT redacted on `GET`** (confirmed
  live 2026-08-20 adding the gomission-snatch action, real URL/apikey came
  back plain) - `actionToModel` reads them straight from the API response,
  no preserve-from-prior-state workaround needed, unlike `filter_id`
  (still omitted from every `GET /api/actions` entry, still needs the
  existing prior-state fallback). Also worth knowing if you're using this:
  autobrr's own action-execution code (`internal/action/run.go`'s
  `webhook()`) never actually applies the `webhook_headers` field to the
  outgoing request at all - a dead field in autobrr's own source, not
  something this provider chose to omit. If a webhook target needs auth,
  it has to come from a token embedded in `webhook_host` (query param) or
  `webhook_data` (body), not a header.

## Resources

### `autobrr_irc_network`

`id`, `name`, `enabled`, `server`, `port`, `tls`, `nick`, `auth_mechanism`,
`auth_account`, `auth_password` (sensitive, never read back),
`invite_command`, `channels` (list of channel names - all managed
channels are enabled).

### `autobrr_indexer`

`id`, `name`, `identifier` (computed, server-generated, never set this),
`identifier_external` (defaults to `name`), `enabled`, `implementation`
(`irc`/`rss`/`torznab`/`newznab`, `RequiresReplace`), `base_url` (only
meaningful for `irc`), `settings` (sensitive map - e.g. an IRC-type
private tracker's `authkey`/`torrent_pass`/`api_key`; empty for a plain
`rss` indexer, whose real credentials live on the paired
`autobrr_feed.url` instead).

An indexer and its feed are always separate API objects in autobrr - see
`autobrr_feed` below and the quirks section above for how they link.

### `autobrr_feed`

`id`, `name`, `indexer_id` (required - the only link to its
`autobrr_indexer`, see the quirks section above), `type`
(`RSS`/`TORZNAB`/`NEWZNAB`), `enabled`, `url` (sensitive - for RSS
trackers this usually carries the entire auth story as query params),
`interval`, `timeout`, `max_age`, `api_key` (sensitive), `cookie`
(sensitive), `download_type` (`MAGNET`/`TORRENT`, flattened out of
autobrr's nested `settings` object).

### `autobrr_download_client`

`id`, `name`, `type` (`SONARR`/`RADARR`/`TRANSMISSION`/`DELUGE_V2`/etc -
see autobrr's `DownloadClientType`), `enabled`, `host`, `port`, `tls`,
`api_key` (sensitive, never read back - for `*arr`-type clients).

### `autobrr_action`

`id`, `name`, `type` (`SONARR`/`RADARR`/`DELUGE_V2`/`TEST`/etc), `enabled`,
`filter_id`, `client_id`, `external_download_client_id` (for
`SONARR`/`RADARR` actions - overrides which download client the target
`*arr` app itself uses for this specific push, letting you scope pushed
releases to a *different* client than that app's default/RSS-driven one),
`category`, `label`, `download_path`, `paused`, `webhook_host` (sensitive
- for `WEBHOOK`-type actions, the destination URL; a shared-secret token
typically has to be embedded here as a query param, since
`webhook_headers` is a dead field in autobrr's own code - see the quirks
section), `webhook_type`, `webhook_method` (autobrr's own `webhook()`
hardcodes `POST` regardless, confirmed via source - set for API-shape
completeness), `webhook_data` (the POST body, run through autobrr's own
Go-template engine against the matched release, e.g. `{{.DownloadURL}}`).

**`category` vs `label`**: these are two different, real autobrr fields.
`category` is qBittorrent-style category assignment. `label` is what
actually sets a Transmission torrent's label(s) - confirmed against
autobrr's own `internal/action/transmission.go`: only `action.Label`
gets sent to `TorrentSet`'s `Labels` field; `Category` is never read by
the Transmission action type at all. `label` is autobrr's own generic
assignment mechanism, independent of any `*arr` app's category field
(e.g. Sonarr's `tvCategory`) - it works identically for filters with no
`*arr` in the loop (e.g. a plain music filter driving a `TRANSMISSION`
action directly).

Note: `filter_id` is also never trusted from `GET /api/actions` - it's
omitted from every entry in that response entirely (confirmed live, not
just when empty), so it's preserved from prior state the same way the
sensitive fields are, for the same reason.

### `autobrr_filter`

`id`, `name`, `enabled`, `indexer_ids` (list of autobrr's own indexer ids,
not Prowlarr's), `resolutions`, `announce_types`, `except_releases`. See
"why the filter schema is narrow" above.

## Provider configuration

```hcl
provider "autobrr" {
  endpoint  = "https://autobrr.example.com"
  api_token = var.autobrr_api_token  # mint via autobrr's UI: Settings -> API Keys
}
```

Auth is a single static `X-API-Token` header once a key exists - bootstrapping
the *first* key still requires a one-time manual UI login, since autobrr's
API auth only accepts its own `api_key` table or an authenticated session
cookie (no way to skip this even if autobrr has OIDC/SSO configured for its
UI).

## Installing locally (filesystem mirror)

```bash
go build -o terraform-provider-autobrr_v0.1.0 .
mkdir -p ~/.terraform.d/plugins/local/costascomputers/autobrr/0.1.0/<os_arch>/
mv terraform-provider-autobrr_v0.1.0[.exe] ~/.terraform.d/plugins/local/costascomputers/autobrr/0.1.0/<os_arch>/
```

Then reference it as:

```hcl
terraform {
  required_providers {
    autobrr = {
      source  = "local/costascomputers/autobrr"
      version = "0.1.0"
    }
  }
}
```

Needs a `provider_installation { filesystem_mirror { ... } }` block in
Terraform's CLI config - see `terraform-provider-syncthing`'s README for
the full explanation and the Windows-specific `%APPDATA%\terraform.rc`
gotcha (same setup applies here, same `local/costascomputers/*` include
pattern covers both providers in one block).

If you rebuild the binary, delete `.terraform.lock.hcl` in the consuming
directory and re-run `terraform init`.

## Development

```bash
go build ./...
go vet ./...
gofmt -l .
```

No test suite yet - verified so far by real `terraform import`/`plan`/`apply`
cycles against a live autobrr instance (see the consuming repo's
`terraform/live/autobrr/` for a real usage example, including `import.sh`
for adopting pre-existing IRC networks/clients/filters/actions into state).
