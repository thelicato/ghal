# ghal

A small Go web application that brings Dependabot alerts from multiple GitHub accounts and organizations into one dashboard.

## Run locally

Requires Go 1.25+ and a C compiler such as GCC for the SQLite driver. Docker includes the required build tools.

```sh
make build
./bin/ghal
```

Open [http://127.0.0.1:8080](http://127.0.0.1:8080). Add a connection with a PAT and account or organization names. Leave both source fields empty to use the token's own account. Add more connections for additional PATs. The [sample alerts](http://127.0.0.1:8080/demo) page lets you inspect the interface without credentials.

## Docker deployment

```sh
docker compose -f compose.example.yaml up -d --build
```

Open [http://127.0.0.1:8080](http://127.0.0.1:8080). Set `GHAL_PORT` when running Compose to use a different host port. The sample runs as a non-root user, includes an HTTP and SQLite healthcheck, and stores the database and generated encryption key in the `ghal-data` named volume. Container restarts and recreation retain this volume. `docker compose -f compose.example.yaml down` also retains it; adding `-v` deletes saved connections, accepted decisions, and the key.

To build and run without Compose:

```sh
docker build -t ghal .
docker run -d --name ghal -p 127.0.0.1:8080:8080 -v ghal-data:/data ghal
```

## Features

- Aggregate alerts across multiple PATs, personal accounts, and organizations.
- Fetch every page of repositories and alerts, including open, fixed, dismissed, and automatically dismissed alerts.
- Deduplicate alerts shared by multiple connections and retain the newest version.
- Search packages, repositories, advisories, and CVEs. Filter by severity, state, source, and ecosystem.
- Click a column heading to sort in either direction. Sorting applies across all pages and to CSV exports.
- Count distinct affected repositories alongside severity totals. Summary stats cover all cached open alerts whose risks have not been accepted, regardless of table filters or pagination.
- See each alert's connection names in a sortable column and in CSV exports, including all connections when an alert is shared.
- Accept individual or selected alert risks locally and reopen them from the Accepted view. Decisions are shared across the deployment and survive restarts.
- Inspect vulnerable versions and available patches, open alerts on GitHub, and export the filtered results to CSV.
- Show per-source failures and preserve successfully fetched pages when another page or source fails.
- Save connections and refresh alerts in the background without blocking page navigation.
- Render pages, submit connection forms, filter, paginate, and export CSV without JavaScript. A small optional script checks background status and updates the page when work completes, preserving unsaved form edits.

Adding a connection saves it immediately, then verifies its PAT and fetches alerts in the background. Verification failures appear in the alert dashboard and on Connections; use **Refresh alerts** to retry, or disconnect and add a replacement token. Removing a connection also starts a background refresh. Search, filters, pagination, and CSV export keep using cached alerts during a refresh. Disconnected sources are removed from the cache immediately, and connection changes cancel obsolete work and fetch the latest configuration. Alerts are cached in memory and fetched automatically on startup when saved connections exist. Background jobs continue if you navigate away and are cancelled when the application shuts down.

**Accept risk** records a decision in ghal without dismissing the alert on GitHub. Accepted alerts leave the Open view and severity counts but remain in Accepted and All, with their original GitHub state visible. Fixed and Dismissed still show their respective GitHub states, including previously accepted alerts. Reopen removes local acceptance. Decisions follow the repository and alert number across PATs, refreshes, and reconnections; other alerts for the same vulnerability remain unaffected. CSV exports include acceptance status and time. The JSON alerts endpoint continues to return GitHub data without local acceptance metadata. Sample alerts cannot be accepted.

Use the row checkboxes and **Accept selected risks** to accept several open alerts at once. The header checkbox selects all eligible rows on the current page. Selection is limited to that page and resets when navigating, sorting, or filtering. Each batch saves all selected decisions together; if an alert is missing, no longer open, or cannot be saved, none are changed. Individual selection and submission work without JavaScript.

## GitHub access

Fine-grained PATs need **Dependabot alerts: read** and repository access for each selected source. Classic PATs need `security_events` (`public_repo` can be sufficient for public repositories); account discovery also needs access to the repositories themselves. Organization-wide fetching requires an organization owner or security manager. Authorize the PAT for organization SSO when required. See [GitHub's Dependabot API permissions](https://docs.github.com/en/rest/dependabot/alerts).

Personal account discovery uses the authenticated user's owned and collaborator repositories, then selects repositories matching the configured account names. Only repositories visible to the PAT can be included. GitHub does not expose inaccessible repositories, so even a successful fetch cannot establish complete coverage beyond the token's access. Dependabot must be enabled on the repositories. Organization access failures appear in the dashboard; there is no silent fallback to a partial list of public repositories.

## Storage and access

Connections, PATs, and accepted decisions are shared across the deployment and persist in SQLite. The default local database is `data/ghal.db`. PATs are encrypted with AES-256-GCM using a generated 32-byte key at `data/ghal.db.key`. Database and generated key files use owner-only permissions. PATs are never included in connection responses or HTML. The key is kept separately from the encrypted database; access to both files permits decryption.

Back up the database and its original key together while the application is stopped, or use SQLite-aware backup tooling. Restore both files together. An existing database will not start with a missing or incorrect key. To keep the key outside the data directory, set `GHAL_KEY_FILE` to a mounted key file containing exactly 32 raw bytes. Keep that key when recreating the container or restoring a backup.

When upgrading from the previous name, stop the old instance and reuse its database, key, and volume. Existing encrypted databases are supported. For the previous local default, use `./bin/ghal -db data/dbaui.db`. In Docker, mount the existing volume at `/data` and set `GHAL_DATABASE=/data/dbaui.db`. The new Compose volume is called `ghal-data`; map it to your existing volume before starting if you have saved connections. Configuration variables now use the `GHAL_` prefix.

Everyone who can access this instance can view alerts and manage shared connections. The app has no login. Local execution and the sample Compose port bind to loopback; use an authenticated HTTPS reverse proxy when exposing the service. Requests to GitHub remain read-only. Each refresh has a five-minute budget, and failures appear alongside any alerts already retrieved. Run one application instance per database; the alert cache is not synchronized between processes.

| Flag | Environment variable | Default |
| --- | --- | --- |
| `-addr` | `GHAL_ADDR` | `127.0.0.1:8080` (`:8080` in Docker) |
| `-db` | `GHAL_DATABASE` | `data/ghal.db` (`/data/ghal.db` in Docker) |
| `-key-file` | `GHAL_KEY_FILE` | Database path followed by `.key` |

Run `./bin/ghal -healthcheck` with the same address configuration to check a running server without opening a database or starting another server.

## Development

Run the application:

```sh
go run ./cmd/ghal
```

Go's `html/template` renders the pages in `web/templates/`. Styles and the optional script live in `web/static/`. Both are embedded in the Go binary. Restart `go run` or rebuild the binary after changing them.

The only Go dependencies are chi and `mattn/go-sqlite3`. The UI adapts shadcn/ui design tokens and component styles to native HTML and plain CSS. It preserves the visual style without requiring React components. Node.js, npm, Tailwind compilation, and frontend build tools are not required.

`GET /api/alerts` returns cached GitHub results immediately. `POST /api/refresh` starts a background refresh and returns `202 Accepted`; repeated requests share the running job. `GET /api/status` returns `refreshing`, `loaded`, and a cache `version`. `POST /api/connections` returns `201 Created` after saving, with an empty `login` until background verification succeeds. Read `/api/connections` again after the job completes for the resolved login and default sources.

## Checks

```sh
make test
make build
```

Go tests cover GitHub pagination and deduplication, partial results, encrypted SQLite storage and restart recovery, shared connections, credential safety, HTML escaping, native forms, filtering and pagination, sorting, persistent risk acceptance and reopening, CSV export, cross-origin writes, and background verification and refreshes, including cancellation and connection changes. Tests use mock GitHub responses and do not require a real PAT.

## Releases

Set `VERSION` to the release version without a `v` prefix, commit the change, and push a matching tag such as `v0.1.0`. The release workflow checks the tag, runs Go tests and vet, publishes images for Linux amd64 and arm64 to `ghcr.io/thelicato/ghal`, and generates GitHub release notes from Conventional Commits. Stable tags also update `latest`; prerelease tags do not. Node.js is only used by the release-note tool in CI.
