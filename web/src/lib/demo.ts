import type { Alert, Connection } from "./api"

export const demoConnections: Connection[] = [
  { id: "demo-work", label: "Work", login: "alexchen", accounts: [], organizations: ["acme", "acme-labs"] },
  { id: "demo-personal", label: "Personal", login: "alexchen", accounts: ["alexchen"], organizations: [] },
]

const samples = [
  ["lodash", "npm", "Prototype pollution in lodash", "critical", "acme/platform", "4.17.21", "open"],
  ["requests", "pip", "Unintended credential disclosure in requests", "high", "acme/data-pipeline", "2.32.4", "open"],
  ["golang.org/x/net", "go", "HTTP/2 rapid reset can cause resource exhaustion", "high", "acme/api-gateway", "0.17.0", "open"],
  ["vite", "npm", "Development server can expose local files", "medium", "acme-labs/console", "6.2.6", "open"],
  ["rack", "rubygems", "Unbounded resource consumption in query parsing", "medium", "acme/billing", "3.1.12", "open"],
  ["axios", "npm", "Server-side request forgery via absolute URLs", "high", "alexchen/starter", "1.8.2", "open"],
  ["pillow", "pip", "Out-of-bounds read when processing image files", "low", "acme-labs/image-worker", null, "open"],
  ["esbuild", "npm", "Development server permits cross-origin requests", "medium", "acme/platform", "0.25.0", "fixed"],
  ["braces", "npm", "Uncontrolled resource consumption", "high", "alexchen/starter", "3.0.3", "dismissed"],
] as const

export const demoAlerts: Alert[] = samples.map(([name, ecosystem, summary, severity, repo, patched, state], i) => ({
  number: 100 + i,
  state,
  html_url: "",
  created_at: new Date(Date.now() - (i + 2) * 86400000).toISOString(),
  updated_at: new Date(Date.now() - (i + 1) * 86400000).toISOString(),
  repository: { full_name: repo, owner: { login: repo.split("/")[0] } },
  dependency: { package: { name, ecosystem }, manifest_path: ecosystem === "go" ? "go.mod" : ecosystem === "pip" ? "requirements.txt" : ecosystem === "rubygems" ? "Gemfile.lock" : "package-lock.json", scope: "runtime" },
  security_advisory: { ghsa_id: "Illustrative advisory", cve_id: "", summary, severity },
  security_vulnerability: { vulnerable_version_range: patched ? `< ${patched}` : "Affected versions", first_patched_version: patched ? { identifier: patched } : null },
  connection_ids: [repo.startsWith("alexchen/") ? "demo-personal" : "demo-work"],
}))
