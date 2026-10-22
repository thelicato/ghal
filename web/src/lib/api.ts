export interface Connection {
  id: string
  label: string
  login: string
  accounts: string[]
  organizations: string[]
}

export interface Alert {
  number: number
  state: string
  html_url: string
  created_at: string
  updated_at: string
  repository: { full_name: string; owner: { login: string } }
  dependency: { package: { name: string; ecosystem: string }; manifest_path: string; scope: string }
  security_advisory: { ghsa_id: string; cve_id: string; summary: string; severity: string }
  security_vulnerability: { vulnerable_version_range: string; first_patched_version: { identifier: string } | null }
  connection_ids: string[]
}

export interface AlertResult {
  alerts: Alert[]
  errors: { connection_id: string; source: string; message: string }[]
  fetched_at: string
}

export async function api<T>(path: string, options?: RequestInit): Promise<T> {
  const response = await fetch(`/api${path}`, {
    ...options,
    headers: { "Content-Type": "application/json", ...options?.headers },
    credentials: "same-origin",
    cache: "no-store",
  })
  if (!response.ok) {
    const data = await response.json().catch(() => null)
    throw new Error(data?.error || `Request failed (HTTP ${response.status}). Please try again.`)
  }
  return response.status === 204 ? undefined as T : response.json()
}

export function githubURL(value: string): string | undefined {
  try {
    const url = new URL(value)
    return url.protocol === "https:" && url.hostname === "github.com" && !url.username && !url.password ? url.href : undefined
  } catch { return undefined }
}
