# dbaui

DependaBot Alerts UI. A small Go web application that brings Dependabot alerts from multiple GitHub accounts and organizations into one dashboard.

## Run locally

Requires Go 1.25+.

```sh
make build
./bin/dbaui
```

Open [http://127.0.0.1:8080](http://127.0.0.1:8080). Add a connection with a PAT and account or organization names. Leave both source fields empty to use the token's own account. Add more connections for additional PATs. The [sample alerts](http://127.0.0.1:8080/demo) page lets you inspect the interface without credentials.

## Features

- Aggregate alerts across multiple PATs, personal accounts, and organizations.
- Fetch every page of repositories and alerts, including open, fixed, dismissed, and automatically dismissed alerts.
- Deduplicate alerts shared by multiple connections and retain the newest version.
- Search packages, repositories, advisories, and CVEs. Filter by severity, state, source, and ecosystem.
- Inspect vulnerable versions and available patches, open alerts on GitHub, and export the filtered results to CSV.
- Show per-source failures and preserve successfully fetched pages when another page or source fails.
- Render pages, submit connection forms, filter, paginate, and export CSV without JavaScript. A small optional script adds submit feedback.

Adding or removing a connection refreshes the cached alerts. Use **Refresh alerts** to fetch changes from GitHub. Search, filters, pagination, and CSV export use the latest result stored in the browser's server-side session, without repeating GitHub requests. Changing connections invalidates that result; an in-flight refresh cannot restore alerts from a disconnected source.

## GitHub access

Fine-grained PATs need **Dependabot alerts: read** and repository access for each selected source. Classic PATs need `security_events` (`public_repo` can be sufficient for public repositories); account discovery also needs access to the repositories themselves. Organization-wide fetching requires an organization owner or security manager. Authorize the PAT for organization SSO when required. See [GitHub's Dependabot API permissions](https://docs.github.com/en/rest/dependabot/alerts).

Personal account discovery uses the authenticated user's owned and collaborator repositories, then selects repositories matching the configured account names. Only repositories visible to the PAT can be included. GitHub does not expose inaccessible repositories, so even a successful fetch cannot establish complete coverage beyond the token's access. Dependabot must be enabled on the repositories. Organization access failures appear in the dashboard; there is no silent fallback to a partial list of public repositories.

## Sessions and deployment

PATs and fetched alerts stay in server memory, isolated by a random HttpOnly, SameSite=Strict session cookie. Connections expire after 12 hours, are removed from the session store on the next session request, and are lost when the server restarts. Tokens are never stored in browser storage, written to disk, or included in connection responses or HTML. The app does not provide durable configuration or application login.

The server listens on loopback by default. To serve behind an HTTPS reverse proxy, use `./bin/dbaui -addr :8080 -secure-cookies` and protect access at the proxy. Requests to GitHub remain read-only. Each refresh has a five-minute budget; timeout and permission failures are reported alongside any alerts already retrieved. Multiple application instances do not share sessions.

## Development

Run the application:

```sh
go run ./cmd/dbaui
```

Go's `html/template` renders the pages in `web/templates/`. Styles and the optional script live in `web/static/`. Both are embedded in the Go binary. Restart `go run` or rebuild the binary after changing them.

chi is the only Go dependency. The UI adapts shadcn/ui design tokens and component styles to native HTML and plain CSS. It preserves the visual style without requiring React components. Node.js, npm, Tailwind compilation, and frontend build tools are not required. The existing JSON endpoints under `/api` remain available.

## Checks

```sh
make test
make build
```

Go tests cover GitHub pagination and deduplication, partial results, credential safety, HTML escaping, native forms, server-side filtering and pagination, CSV export, session isolation and expiration, cross-origin writes, and concurrent refreshes. Tests use mock GitHub responses and do not require a real PAT.
