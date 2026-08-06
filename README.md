# terraform-provider-autobrr

A minimal Terraform provider for [autobrr](https://autobrr.com/), covering
IRC networks, download clients (including native `*arr` push targets like
`SONARR`/`RADARR`, not just torrent clients), actions, and a deliberately
narrow filter schema.

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

## Resources

### `autobrr_irc_network`

`id`, `name`, `enabled`, `server`, `port`, `tls`, `nick`, `auth_mechanism`,
`auth_account`, `auth_password` (sensitive, never read back),
`invite_command`, `channels` (list of channel names - all managed
channels are enabled).

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
`category`, `label`, `download_path`, `paused`.

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
