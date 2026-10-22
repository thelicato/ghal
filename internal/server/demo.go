package server

import (
	"net/http"
	"time"

	"github.com/thelicato/ghal/internal/github"
)

func demoPageData(r *http.Request) pageData {
	connections := []github.Connection{
		{ID: "demo-work", Label: "Work", Login: "octocat", Organizations: []string{"acme", "acme-labs"}},
		{ID: "demo-personal", Label: "Personal", Login: "octocat", Accounts: []string{"octocat"}},
	}
	samples := []struct{ pkg, ecosystem, summary, severity, repo, patch, state string }{
		{"lodash", "npm", "Prototype pollution in lodash", "critical", "acme/platform", "4.17.21", "open"},
		{"requests", "pip", "Unintended credential disclosure in requests", "high", "acme/data-pipeline", "2.32.4", "open"},
		{"golang.org/x/net", "go", "HTTP/2 rapid reset can cause resource exhaustion", "high", "acme/api-gateway", "0.17.0", "open"},
		{"vite", "npm", "Development server can expose local files", "medium", "acme-labs/console", "6.2.6", "open"},
		{"rack", "rubygems", "Unbounded resource consumption in query parsing", "medium", "acme/billing", "3.1.12", "open"},
		{"axios", "npm", "Server-side request forgery via absolute URLs", "high", "octocat/starter", "1.8.2", "open"},
		{"pillow", "pip", "Out-of-bounds read when processing image files", "low", "acme-labs/image-worker", "", "open"},
		{"esbuild", "npm", "Development server permits cross-origin requests", "medium", "acme/platform", "0.25.0", "fixed"},
		{"braces", "npm", "Uncontrolled resource consumption", "high", "octocat/starter", "3.0.3", "dismissed"},
	}
	result := github.Result{FetchedAt: time.Now().UTC()}
	for i, sample := range samples {
		alert := github.Alert{Number: 100 + i, State: sample.state, UpdatedAt: time.Now().Add(-time.Duration(i+1) * 24 * time.Hour).UTC().Format(time.RFC3339)}
		alert.Repository.FullName = sample.repo
		for j, c := range sample.repo {
			if c == '/' {
				alert.Repository.Owner.Login = sample.repo[:j]
				break
			}
		}
		alert.Dependency.Package.Name, alert.Dependency.Package.Ecosystem = sample.pkg, sample.ecosystem
		alert.Dependency.ManifestPath = map[string]string{"go": "go.mod", "pip": "requirements.txt", "rubygems": "Gemfile.lock", "npm": "package-lock.json"}[sample.ecosystem]
		alert.Dependency.Scope = "runtime"
		alert.SecurityAdvisory.Summary, alert.SecurityAdvisory.Severity, alert.SecurityAdvisory.GHSAID = sample.summary, sample.severity, "Illustrative advisory"
		if sample.patch != "" {
			alert.SecurityVulnerability.FirstPatchedVersion = &struct {
				Identifier string `json:"identifier"`
			}{sample.patch}
			alert.SecurityVulnerability.VulnerableVersionRange = "< " + sample.patch
		}
		result.Alerts = append(result.Alerts, alert)
	}
	p := makePage(r, connections, result, true, false)
	p.Demo, p.BasePath = true, "/demo"
	for i := range p.Summaries {
		p.Summaries[i].URL = p.URL("stat", p.Summaries[i].Severity)
	}
	return p
}
