# dbaui

DependaBot Alerts UI. A small Go web application that brings Dependabot alerts from multiple GitHub accounts and organizations into one dashboard.

## Run locally

Requires Go 1.25+, Node.js 22.12+ (or a newer supported Node release), and pnpm 11.

```sh
pnpm install --frozen-lockfile
make build
./bin/dbaui
```

Open [http://127.0.0.1:8080](http://127.0.0.1:8080). Add a connection with a PAT and account or organization names. Leave both source fields empty to use the token's own account. Add more connections for additional PATs. The demo workspace lets you explore the interface without credentials.

## Features

- Aggregate alerts across multiple PATs, personal accounts, and organizations.
- Fetch every page of repositories and alerts, including open, fixed, dismissed, and automatically dismissed alerts.
- Deduplicate alerts shared by multiple connections and retain the newest version.
- Search packages, repositories, advisories, and CVEs. Filter by severity, state, source, and ecosystem.
- Inspect vulnerable versions and available patches, open alerts on GitHub, and export the filtered results to CSV.
- Show per-source failures and preserve successfully fetched pages when another page or source fails.

## GitHub access

Fine-grained PATs need **Dependabot alerts: read** and repository access for each selected source. Classic PATs need `security_events` (`public_repo` can be sufficient for public repositories); account discovery also needs access to the repositories themselves. Organization-wide fetching requires an organization owner or security manager. Authorize the PAT for organization SSO when required. See [GitHub's Dependabot API permissions](https://docs.github.com/en/rest/dependabot/alerts).

Personal account discovery uses the authenticated user's owned and collaborator repositories, then selects repositories matching the configured account names. Only repositories visible to the PAT can be included. GitHub does not expose inaccessible repositories, so even a successful fetch cannot establish complete coverage beyond the token's access. Dependabot must be enabled on the repositories. Organization access failures appear in the dashboard; there is no silent fallback to a partial list of public repositories.

## Sessions and deployment

PATs stay in server memory, isolated by a random HttpOnly, SameSite=Strict session cookie. Connections expire after 12 hours, are removed from the session store on the next API request, and are lost when the server restarts. Tokens are never stored in browser storage, written to disk, or included in connection responses. The app does not provide durable configuration or application login.

The server listens on loopback by default. To serve behind an HTTPS reverse proxy, use `./bin/dbaui -addr :8080 -secure-cookies` and protect access at the proxy. Requests to GitHub remain read-only. Each refresh has a five-minute budget; timeout and permission failures are reported alongside any alerts already retrieved. Multiple application instances do not share sessions.

## Development

Run the API and Vite in separate terminals:

```sh
go run ./cmd/dbaui
pnpm dev
```

Open Vite's printed local URL. Its `/api` proxy targets `127.0.0.1:8080`. For a production build, `make build` compiles the frontend and embeds it into one Go binary; Node.js is only needed to build or develop the UI. Rebuild and restart Go after changing embedded frontend assets.

The backend uses chi and the Go standard library. The frontend uses React, Vite, Tailwind CSS, and locally owned shadcn/ui components. Native dialogs and selects avoid additional component dependencies. No database, GitHub SDK, client router, or state management library is required.

## Checks

```sh
make test
make build
```

Go tests cover cursor pagination, partial results, deduplication, all alert states, unsafe pagination links, redirects, sanitized errors, connection validation, session isolation and expiration, cross-origin writes, and concurrent refreshes. `pnpm check` checks TypeScript. Tests use mock GitHub responses and do not require a real PAT.
