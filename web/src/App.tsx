import { useCallback, useEffect, useRef, useState, type FormEvent, type ReactNode } from "react"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Card } from "@/components/ui/card"
import { Input } from "@/components/ui/input"
import { Icon } from "@/components/icons"
import { api, githubURL, type Alert, type AlertResult, type Connection } from "@/lib/api"
import { demoAlerts, demoConnections } from "@/lib/demo"
import { cn } from "@/lib/utils"

const severities = ["critical", "high", "medium", "low"]
const emptyResult: AlertResult = { alerts: [], errors: [], fetched_at: "" }
const pageSize = 20

function relativeDate(value: string) {
  const days = Math.max(0, Math.floor((Date.now() - Date.parse(value)) / 86400000))
  if (!Number.isFinite(days)) return "Unknown"
  return days === 0 ? "Today" : days === 1 ? "Yesterday" : `${days}d ago`
}

function stateLabel(state: string) {
  return state === "auto_dismissed" ? "Auto-dismissed" : state.charAt(0).toUpperCase() + state.slice(1)
}

export default function App() {
  const [connections, setConnections] = useState<Connection[]>([])
  const [result, setResult] = useState<AlertResult>(emptyResult)
  const [view, setView] = useState<"overview" | "connections">("overview")
  const [demo, setDemo] = useState(false)
  const [refreshing, setRefreshing] = useState(true)
  const [error, setError] = useState("")
  const [showConnect, setShowConnect] = useState(false)
  const [deleting, setDeleting] = useState("")
  const [query, setQuery] = useState("")
  const [state, setState] = useState("open")
  const [severity, setSeverity] = useState("all")
  const [owner, setOwner] = useState("all")
  const [ecosystem, setEcosystem] = useState("all")
  const [page, setPage] = useState(1)
  const fetching = useRef(false)

  const refresh = useCallback(async () => {
    if (fetching.current) return
    fetching.current = true
    setRefreshing(true)
    setError("")
    try {
      const current = await api<Connection[]>("/connections")
      setConnections(current)
      setResult(await api<AlertResult>("/alerts"))
    } catch (error) {
      setError(error instanceof Error ? error.message : "Could not load alerts. Please try again.")
    } finally {
      fetching.current = false
      setRefreshing(false)
    }
  }, [])

  useEffect(() => { void refresh() }, [refresh])
  useEffect(() => { setPage(1) }, [query, state, severity, owner, ecosystem, demo])

  const activeConnections = demo ? demoConnections : connections
  const alerts = demo ? demoAlerts : result.alerts
  const issues = demo ? [] : result.errors
  const open = alerts.filter(alert => alert.state === "open")
  const owners = [...new Set(alerts.map(alert => alert.repository.owner.login))].sort()
  const ecosystems = [...new Set(alerts.map(alert => alert.dependency.package.ecosystem))].sort()
  const search = query.trim().toLowerCase()
  const filtered = alerts.filter(alert =>
    (state === "all" || alert.state === state || (state === "dismissed" && alert.state === "auto_dismissed")) &&
    (severity === "all" || alert.security_advisory.severity === severity) &&
    (owner === "all" || alert.repository.owner.login === owner) &&
    (ecosystem === "all" || alert.dependency.package.ecosystem === ecosystem) &&
    (!search || [alert.repository.full_name, alert.dependency.package.name, alert.security_advisory.summary, alert.security_advisory.cve_id, alert.security_advisory.ghsa_id].join(" ").toLowerCase().includes(search)),
  )
  const totalPages = Math.max(1, Math.ceil(filtered.length / pageSize))
  const currentPage = Math.min(page, totalPages)
  const paged = filtered.slice((currentPage - 1) * pageSize, currentPage * pageSize)
  const repoCount = new Set(open.map(alert => alert.repository.full_name)).size
  const busy = refreshing || Boolean(deleting)

  function resetFilters() {
    setQuery("")
    setSeverity("all")
    setOwner("all")
    setEcosystem("all")
  }

  async function removeConnection(id: string) {
    setDeleting(id)
    setError("")
    try {
      await api(`/connections/${id}`, { method: "DELETE" })
      // Remove old results immediately so disconnected sources never linger.
      setConnections(current => current.filter(connection => connection.id !== id))
      setResult(emptyResult)
      await refresh()
    } catch (error) {
      setError(error instanceof Error ? error.message : "Could not disconnect this source.")
    } finally { setDeleting("") }
  }

  function exportCSV() {
    const rows = [
      ["Repository", "Package", "Ecosystem", "Severity", "State", "Summary", "Advisory", "Fixed version", "URL"],
      ...filtered.map(alert => [alert.repository.full_name, alert.dependency.package.name, alert.dependency.package.ecosystem, alert.security_advisory.severity, alert.state, alert.security_advisory.summary, alert.security_advisory.cve_id || alert.security_advisory.ghsa_id, alert.security_vulnerability.first_patched_version?.identifier || "", githubURL(alert.html_url) || ""]),
    ]
    const csv = rows.map(row => row.map(value => {
      const safe = /^[=+\-@\t\r\n]/.test(value) ? `'${value}` : value
      return `"${safe.replaceAll('"', '""')}"`
    }).join(",")).join("\r\n")
    const url = URL.createObjectURL(new Blob([csv], { type: "text/csv;charset=utf-8;" }))
    const link = document.createElement("a")
    link.href = url
    link.download = `dbaui-${demo ? "demo-" : ""}${new Date().toISOString().slice(0, 10)}.csv`
    link.click()
    setTimeout(() => URL.revokeObjectURL(url), 1000)
  }

  return (
    <div className="app-shell">
      <a href="#main" className="skip-link">Skip to content</a>
      <aside className="sidebar">
        <a href="/" className="brand" aria-label="dbaui home">
          <span className="brand-mark"><Icon name="shield" /></span>
          <span>dbaui<span className="brand-dot">.</span></span>
          <span className="brand-version">v0.1</span>
        </a>
        <div className="workspace"><span className="workspace-icon"><Icon name="globe" /></span><div><strong>Your workspace</strong><span>Dependency security</span></div></div>
        <p className="nav-label">WORKSPACE</p>
        <nav aria-label="Main navigation">
          <button className={cn("nav-item", view === "overview" && "active")} onClick={() => setView("overview")} aria-current={view === "overview" ? "page" : undefined}><Icon name="grid" />Overview<span className="nav-counter">{open.length}</span></button>
          <button className={cn("nav-item", view === "connections" && "active")} onClick={() => setView("connections")} aria-current={view === "connections" ? "page" : undefined}><Icon name="key" />Connections<span className="nav-counter">{activeConnections.length}</span></button>
        </nav>
        <div className="sidebar-bottom">
          <div className="session-note"><Icon name="lock" /><strong>Your tokens stay private</strong><p>Stored in server memory for this browser session. Never saved to disk.</p></div>
          <a className="docs-link" href="https://docs.github.com/en/code-security/dependabot/dependabot-alerts/about-dependabot-alerts" target="_blank" rel="noreferrer">About Dependabot<Icon name="arrow" /></a>
          <div className="sidebar-footer"><span className="avatar">D</span><div><strong>DependaBot Alerts UI</strong><span>A little clarity for your dependencies.</span></div></div>
        </div>
      </aside>

      <div className="main-shell">
        <header className="topbar"><div><span className="text-muted-foreground">Workspace</span><Icon name="chevron" /><span>{view === "overview" ? "Overview" : "Connections"}</span></div><span className="privacy-label"><span className="status-dot" />{demo ? "Demo workspace" : "Private session"}</span></header>
        <main id="main" className="main-content">
          <div className="page-heading">
            <div><p className="eyebrow">YOUR DEPENDENCIES, IN FOCUS</p><h1>{view === "overview" ? "Security overview" : "Your connections"}</h1><p className="page-subtitle">{view === "overview" ? "All your Dependabot alerts. One clear view." : "Bring your GitHub accounts and organizations together."}</p></div>
            <div className="heading-actions">
              {!demo && view === "overview" && <Button variant="outline" onClick={() => void refresh()} disabled={busy}><Icon name="refresh" className={refreshing ? "spin" : ""} />{refreshing ? "Refreshing..." : "Refresh alerts"}</Button>}
              <Button onClick={() => { setDemo(false); setShowConnect(true) }} disabled={busy}><Icon name="plus" />Add connection</Button>
            </div>
          </div>

          {error && <div className="message error-message" role="alert"><Icon name="alert" /><div><strong>We couldn't complete that request.</strong><p>{error}</p>{result.fetched_at && <p>Previously fetched alerts remain visible until the next successful refresh.</p>}</div><Button size="sm" variant="outline" disabled={busy} onClick={() => void refresh()}>Try again</Button></div>}
          {demo && <div className="message demo-message"><Icon name="globe" /><div><strong>You're exploring a demo workspace.</strong><p>Illustrative alerts and connections. No GitHub data is being fetched.</p></div><Button size="sm" variant="outline" onClick={() => { setDemo(false); resetFilters() }}>Leave demo</Button></div>}
          {issues.length > 0 && <div className="message warning-message" role="status"><Icon name="alert" /><div><strong>Some sources couldn't be fully loaded. These totals may be incomplete.</strong><details><summary>Review {issues.length} source {issues.length === 1 ? "issue" : "issues"}</summary><ul>{issues.map((issue, i) => <li key={i}><strong>{activeConnections.find(conn => conn.id === issue.connection_id)?.label}: {issue.source}</strong><span>{issue.message}</span></li>)}</ul></details></div></div>}

          {view === "connections" ? (
            <>
              <div className="section-intro"><h2>Connected sources <Badge className="neutral-badge">{activeConnections.length}</Badge></h2><p>Each connection pairs a personal access token with the sources it can read.</p></div>
              {activeConnections.length === 0 ? <EmptyConnections onConnect={() => setShowConnect(true)} onDemo={() => { setDemo(true); resetFilters() }} /> : <div className="connection-grid">{activeConnections.map(connection => <Card className="connection-card" key={connection.id}><div className="connection-title"><span className="connection-avatar"><Icon name="github" /></span><div><h3>{connection.label}</h3><p>Authenticated as @{connection.login}</p></div><Badge className="connected-badge">Connected</Badge></div><div className="source-chips">{connection.accounts.map(account => <Badge className="neutral-badge" key={account}><Icon name="github" />{account}</Badge>)}{connection.organizations.map(org => <Badge className="neutral-badge" key={org}><Icon name="globe" />{org}</Badge>)}</div><div className="connection-footer"><span><Icon name="lock" />{demo ? "Demo connection" : "Session only"}</span><Button variant="ghost" size="sm" className="text-destructive" onClick={() => void removeConnection(connection.id)} disabled={demo || busy}><Icon name="trash" />{deleting === connection.id ? "Disconnecting..." : "Disconnect"}</Button></div></Card>)}</div>}
              <Card className="permissions-note"><Icon name="key" /><div><h3>A small note on permissions</h3><p>Fine-grained PATs need read access to Dependabot alerts for the selected repositories. Classic PATs need <code>security_events</code>. Organization-wide alerts require an organization owner or security manager, and SSO authorization may also be required.</p><a href="https://docs.github.com/en/rest/dependabot/alerts" target="_blank" rel="noreferrer">GitHub's permissions guide<Icon name="arrow" /></a></div></Card>
            </>
          ) : (
            <>
              <div className="overview-caption"><span className="live-label"><span className={cn("status-dot", activeConnections.length === 0 && "inactive")} />{activeConnections.length} {activeConnections.length === 1 ? "connection" : "connections"}</span><span className="caption-divider" /><span>{repoCount} {repoCount === 1 ? "repository" : "repositories"} with open alerts</span>{result.fetched_at && !demo && <span className="last-sync"><Icon name="clock" />Last synced {new Date(result.fetched_at).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" })}</span>}</div>
              <div className="stats-grid">{severities.map(level => {
                const count = open.filter(alert => alert.security_advisory.severity === level).length
                return <button key={level} className={cn("stat-card", severity === level && "selected")} onClick={() => { setSeverity(severity === level ? "all" : level); setState("open") }} aria-pressed={severity === level}><div className="stat-top"><span>{stateLabel(level)}</span><span className={cn("severity-symbol", level)}><Icon name={level === "critical" || level === "high" ? "alert" : "shield"} /></span></div><strong className={refreshing ? "loading-number" : ""}>{refreshing && !demo && alerts.length === 0 ? "..." : count}</strong><div className="stat-footer"><span className={cn("severity-dot", level)} />{level === "critical" ? "Needs immediate attention" : level === "high" ? "Prioritize these next" : level === "medium" ? "Review and plan a fix" : "Keep on your radar"}</div></button>
              })}</div>

              <Card className="alerts-card">
                <div className="alerts-heading"><div><h2>Dependabot alerts <Badge className="neutral-badge">{alerts.length}</Badge></h2><p>Find what matters. Get back to building.</p></div><Button variant="outline" size="sm" onClick={exportCSV} disabled={filtered.length === 0}><Icon name="download" />Export CSV</Button></div>
                <div className="alerts-tabs" role="group" aria-label="Filter by alert state">{["open", "fixed", "dismissed", "all"].map(tab => <button className={cn("alert-tab", state === tab && "selected")} key={tab} onClick={() => setState(tab)} aria-pressed={state === tab}>{tab === "all" ? "All alerts" : stateLabel(tab)}<span>{tab === "all" ? alerts.length : alerts.filter(alert => alert.state === tab || (tab === "dismissed" && alert.state === "auto_dismissed")).length}</span></button>)}</div>
                <div className="filters"><div className="search-input"><Icon name="search" /><Input aria-label="Search alerts" placeholder="Search packages, repositories, CVEs..." value={query} onChange={event => setQuery(event.target.value)} /></div><Filter label="Severity" value={severity} onChange={setSeverity} options={severities} /><Filter label="Source" value={owner} onChange={setOwner} options={owners} /><Filter label="Ecosystem" value={ecosystem} onChange={setEcosystem} options={ecosystems} /></div>

                {refreshing && alerts.length === 0 && !demo ? <div className="loading-state" role="status"><Icon name="refresh" className="spin" /><h3>Gathering your alerts</h3><p>Checking your connected accounts and organizations.</p></div> : activeConnections.length === 0 ? <EmptyConnections onConnect={() => setShowConnect(true)} onDemo={() => { setDemo(true); resetFilters() }} /> : filtered.length === 0 ? <div className="empty-state"><span className="empty-icon"><Icon name={issues.length > 0 ? "alert" : "check"} /></span><h3>{issues.length > 0 ? "No alerts to display" : alerts.length === 0 ? "A little peace of mind" : "Nothing here right now"}</h3><p>{issues.length > 0 ? "Review the source issues above to check what could not be loaded." : alerts.length === 0 ? "No Dependabot alerts were returned for your connected sources." : "No alerts match this view. Try a different state or clear your filters."}</p>{alerts.length > 0 && <Button variant="outline" onClick={resetFilters}>Clear filters</Button>}</div> : <div className="table-scroll"><table><thead><tr><th>Dependency / vulnerability</th><th>Repository</th><th>Severity</th><th>Status</th><th>Updated</th><th><span className="sr-only">GitHub link</span></th></tr></thead><tbody>{paged.map(alert => <AlertRow key={`${alert.repository.full_name}#${alert.number}`} alert={alert} />)}</tbody></table></div>}
                <div className="table-footer"><span>{filtered.length === 0 ? "0 alerts" : `Showing ${(currentPage - 1) * pageSize + 1}-${Math.min(currentPage * pageSize, filtered.length)} of ${filtered.length} alerts`}</span><div><Button variant="outline" size="sm" disabled={currentPage === 1} onClick={() => setPage(currentPage - 1)}>Previous</Button><span>{currentPage} / {totalPages}</span><Button variant="outline" size="sm" disabled={currentPage >= totalPages} onClick={() => setPage(currentPage + 1)}>Next</Button></div></div>
              </Card>
              <div className="overview-footer"><Icon name="shield" /><span>A clear view of your dependency health.</span><span className="footer-note">Read-only access. Fix alerts on GitHub.</span></div>
            </>
          )}
        </main>
      </div>
      {showConnect && <ConnectDialog onClose={() => setShowConnect(false)} onSaved={() => { setShowConnect(false); setDemo(false); resetFilters(); void refresh() }} />}
    </div>
  )
}

function Filter({ label, value, onChange, options }: { label: string; value: string; onChange: (value: string) => void; options: string[] }) {
  return <select className="filter-select" aria-label={label} value={value} onChange={event => onChange(event.target.value)}><option value="all">{label === "Severity" ? "All severities" : label === "Source" ? "All sources" : "All ecosystems"}</option>{options.map(option => <option key={option} value={option}>{label === "Severity" ? stateLabel(option) : option}</option>)}</select>
}

function EmptyConnections({ onConnect, onDemo }: { onConnect: () => void; onDemo: () => void }) {
  return <div className="empty-state onboarding"><span className="empty-icon"><Icon name="github" /></span><Badge className="onboarding-badge">LESS NOISE. MORE CLARITY.</Badge><h3>Your dependency health starts here.</h3><p>Connect a GitHub account or organization to see all your Dependabot alerts in one place.</p><div className="empty-actions"><Button onClick={onConnect}><Icon name="plus" />Connect GitHub</Button><Button variant="ghost" onClick={onDemo}>Explore demo<Icon name="arrow" /></Button></div><span className="empty-footnote"><Icon name="lock" />Your PAT stays in server memory for this session.</span></div>
}

function AlertRow({ alert }: { alert: Alert }) {
  const [expanded, setExpanded] = useState(false)
  const severity = alert.security_advisory.severity
  const url = githubURL(alert.html_url)
  return <><tr className={expanded ? "expanded-row" : ""}><td><button className="dependency-button" onClick={() => setExpanded(!expanded)} aria-expanded={expanded}><span className="package-icon"><Icon name="package" /></span><span><span className="dependency-name">{alert.dependency.package.name}<span className="ecosystem-label">{alert.dependency.package.ecosystem}</span></span><span className="vulnerability-summary">{alert.security_advisory.summary}</span></span><Icon name="chevron" className={cn("expand-chevron", expanded && "rotated")} /></button></td><td><span className="repository-name">{alert.repository.full_name}</span><span className="manifest-path">{alert.dependency.manifest_path}</span></td><td><Badge className={cn("severity-badge", severity)}><span className={cn("severity-dot", severity)} />{stateLabel(severity)}</Badge></td><td><span className="alert-status"><span className={cn("state-dot", `state-${alert.state}`)} />{stateLabel(alert.state)}</span></td><td className="updated-cell"><time dateTime={alert.updated_at} title={new Date(alert.updated_at).toLocaleString()}>{relativeDate(alert.updated_at)}</time></td><td>{url && <a className="external-link" href={url} target="_blank" rel="noreferrer" aria-label={`Open ${alert.dependency.package.name} alert #${alert.number} on GitHub`}><Icon name="arrow" /></a>}</td></tr>{expanded && <tr className="detail-row"><td colSpan={6}><div className="alert-detail"><Detail label="Advisory">{alert.security_advisory.cve_id || alert.security_advisory.ghsa_id}</Detail><Detail label="Vulnerable versions">{alert.security_vulnerability.vulnerable_version_range || "Not specified"}</Detail><Detail label="First patched version">{alert.security_vulnerability.first_patched_version?.identifier || "No patch available"}</Detail><Detail label="Dependency scope">{alert.dependency.scope || "Not specified"}</Detail></div></td></tr>}</>
}

function Detail({ label, children }: { label: string; children: ReactNode }) {
  return <div><span>{label}</span><strong>{children}</strong></div>
}

function ConnectDialog({ onClose, onSaved }: { onClose: () => void; onSaved: () => void }) {
  const dialog = useRef<HTMLDialogElement>(null)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState("")
  const [token, setToken] = useState("")

  useEffect(() => {
    const element = dialog.current!
    element.showModal()
    return () => element.close()
  }, [])

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const form = new FormData(event.currentTarget)
    const split = (value: FormDataEntryValue | null) => String(value || "").split(/[,\s]+/).map(value => value.trim()).filter(Boolean)
    setSaving(true)
    setError("")
    try {
      await api<Connection>("/connections", { method: "POST", body: JSON.stringify({ label: form.get("label"), token, accounts: split(form.get("accounts")), organizations: split(form.get("organizations")) }) })
      setToken("")
      onSaved()
    } catch (error) {
      setError(error instanceof Error ? error.message : "Could not connect to GitHub.")
    } finally { setSaving(false) }
  }

  return <dialog ref={dialog} className="connect-dialog" aria-labelledby="connect-title" aria-describedby="connect-description" onCancel={event => { if (saving) event.preventDefault(); else onClose() }}>
    <form onSubmit={event => void submit(event)}>
      <div className="dialog-header"><span className="dialog-icon"><Icon name="key" /></span><Button variant="ghost" size="icon" aria-label="Close connection dialog" disabled={saving} onClick={onClose}><Icon name="close" /></Button></div>
      <h2 id="connect-title">Make a connection.</h2><p id="connect-description">Add a GitHub token and choose the accounts or organizations you'd like to keep an eye on.</p>
      {error && <div className="form-error" role="alert">{error}</div>}
      <fieldset disabled={saving}>
        <label htmlFor="connection-label">Connection name <span>optional</span></label><Input id="connection-label" name="label" placeholder="e.g. Work or Personal" maxLength={80} autoFocus />
        <label htmlFor="connection-token">Personal access token</label><Input id="connection-token" name="token" type="password" placeholder="github_pat_... or ghp_..." autoComplete="new-password" spellCheck={false} required maxLength={512} value={token} onChange={event => setToken(event.target.value)} aria-describedby="token-help" /><p id="token-help" className="field-help">Read access to Dependabot alerts is required. <a href="https://github.com/settings/personal-access-tokens/new" target="_blank" rel="noreferrer">Create a token<Icon name="arrow" /></a></p>
        <label htmlFor="connection-accounts">Personal accounts <span>optional</span></label><Input id="connection-accounts" name="accounts" placeholder="octocat, another-account" autoComplete="off" spellCheck={false} />
        <label htmlFor="connection-orgs">Organizations <span>optional</span></label><Input id="connection-orgs" name="organizations" placeholder="your-org, another-org" autoComplete="off" spellCheck={false} /><p className="field-help">Separate names with commas. Leave both fields blank to use the token's own account. Organization-wide alerts require an owner or security manager.</p>
      </fieldset>
      <div className="dialog-privacy"><Icon name="lock" /><p>Tokens stay in server memory for this browser session, up to 12 hours. Restarting the server clears every connection.</p></div>
      <div className="dialog-footer"><Button variant="outline" disabled={saving} onClick={onClose}>Cancel</Button><Button type="submit" disabled={saving}>{saving ? <Icon name="refresh" className="spin" /> : <Icon name="github" />}{saving ? "Connecting..." : "Connect GitHub"}</Button></div>
    </form>
  </dialog>
}
