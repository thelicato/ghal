package server

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"dbaui/internal/github"
)

func formRequest(handler http.Handler, path string, values url.Values, cookie *http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "http://localhost"+path, strings.NewReader(values.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if cookie != nil {
		req.AddCookie(cookie)
	}
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	return res
}

func pageAlert(pkg, repo, severity, state string) github.Alert {
	alert := github.Alert{Number: 1, State: state, UpdatedAt: "2026-10-22T10:00:00Z"}
	alert.Repository.FullName = repo
	alert.Repository.Owner.Login = strings.Split(repo, "/")[0]
	alert.Dependency.Package.Name, alert.Dependency.Package.Ecosystem = pkg, "npm"
	alert.Dependency.ManifestPath = "package-lock.json"
	alert.SecurityAdvisory.Severity, alert.SecurityAdvisory.Summary = severity, "Vulnerability in "+pkg
	alert.SecurityAdvisory.CVEID = "CVE-2026-12345"
	alert.HTMLURL = "https://github.com/" + repo + "/security/dependabot/1"
	return alert
}

func TestPagesRenderWithoutFrontendBuild(t *testing.T) {
	handler := New(&fakeGitHub{}, false)
	for path, want := range map[string]string{"/": "No connections configured", "/connections": `name="token"`, "/demo": "Prototype pollution in lodash"} {
		res := request(handler, "GET", path, "", nil)
		if res.Code != 200 || !strings.Contains(res.Body.String(), want) {
			t.Fatalf("%s did not render: status=%d body=%s", path, res.Code, res.Body.String())
		}
		if res.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%s can be cached", path)
		}
		for _, removed := range []string{"Private session", "Your workspace", "Your tokens stay private", "/src/", "node_modules"} {
			if strings.Contains(res.Body.String(), removed) {
				t.Fatalf("%s contains removed UI or frontend runtime: %s", path, removed)
			}
		}
	}
	for path, media := range map[string]string{"/static/app.css": "text/css", "/static/app.js": "javascript", "/static/favicon.svg": "image/svg+xml"} {
		res := request(handler, "GET", path, "", nil)
		if res.Code != 200 || !strings.Contains(res.Header().Get("Content-Type"), media) {
			t.Fatalf("embedded asset missing: %s status=%d type=%s", path, res.Code, res.Header().Get("Content-Type"))
		}
	}
	if res := request(handler, "GET", "/static/../templates/overview.html", "", nil); res.Code != 404 {
		t.Fatal("template source is exposed by the static handler")
	}
}

func TestNativeConnectionFormsAndCachedRendering(t *testing.T) {
	var fetches atomic.Int32
	client := &fakeGitHub{fetch: func(ctx context.Context, connections []github.Connection) github.Result {
		fetches.Add(1)
		result := github.Result{FetchedAt: time.Now()}
		if len(connections) > 0 {
			result.Alerts = []github.Alert{pageAlert("native-package", "octocat/project", "high", "open")}
		}
		return result
	}}
	handler := New(client, false)
	res := request(handler, "GET", "/", "", nil)
	cookie := res.Result().Cookies()[0]
	res = formRequest(handler, "/connections", url.Values{"label": {"Work"}, "token": {"never-render-this-token"}, "accounts": {"octocat, OCTOCAT"}}, cookie)
	if res.Code != 303 || res.Header().Get("Location") != "/" {
		t.Fatalf("native connection form did not redirect: status=%d body=%s", res.Code, res.Body.String())
	}
	res = request(handler, "GET", "/?q=native-package&severity=high&state=open", "", cookie)
	if res.Code != 200 || !strings.Contains(res.Body.String(), "Vulnerability in native-package") {
		t.Fatalf("alerts were not rendered server-side: %s", res.Body.String())
	}
	if fetches.Load() != 1 {
		t.Fatal("filtering performed another GitHub fetch")
	}
	res = request(handler, "GET", "/connections", "", cookie)
	if !strings.Contains(res.Body.String(), "Authenticated as @octocat") || strings.Contains(res.Body.String(), "never-render-this-token") {
		t.Fatal("connection metadata missing or token rendered")
	}
	other := request(handler, "GET", "/", "", nil)
	if strings.Contains(other.Body.String(), "native-package") {
		t.Fatal("HTML alerts leaked across browser sessions")
	}
	listing := request(handler, "GET", "/api/connections", "", cookie)
	var connections []github.Connection
	if err := json.Unmarshal(listing.Body.Bytes(), &connections); err != nil {
		t.Fatal(err)
	}
	res = formRequest(handler, "/connections/"+connections[0].ID+"/delete", nil, cookie)
	if res.Code != 303 || res.Header().Get("Location") != "/connections" {
		t.Fatalf("native delete failed: %d", res.Code)
	}
	res = request(handler, "GET", "/", "", cookie)
	if strings.Contains(res.Body.String(), "native-package") || !strings.Contains(res.Body.String(), "No connections configured") {
		t.Fatal("disconnected alerts remain cached")
	}
}

func TestFormErrorsPreserveOnlyNonSecretFields(t *testing.T) {
	handler := New(&fakeGitHub{}, false)
	cookie := browserSession(t, handler)
	res := formRequest(handler, "/connections", url.Values{"label": {`<img src=x onerror=alert(1)>`}, "token": {"never-render-this-token"}, "accounts": {"invalid/name"}}, cookie)
	body := res.Body.String()
	if res.Code != 400 || !strings.Contains(body, "&lt;img") || strings.Contains(body, `<img src=x`) || strings.Contains(body, "never-render-this-token") {
		t.Fatalf("form validation did not escape values or leaked the PAT: status=%d", res.Code)
	}
	res = formRequest(handler, "/connections", url.Values{"token": {"invalid"}}, cookie)
	if res.Code != 422 || !strings.Contains(res.Body.String(), "GitHub rejected this token") {
		t.Fatal("GitHub token error was not rendered")
	}
}

func TestServerSideFilteringPaginationAndExport(t *testing.T) {
	var result github.Result
	for i := range 45 {
		result.Alerts = append(result.Alerts, pageAlert(fmt.Sprintf("package-%02d", i), "octocat/project", "high", "open"))
	}
	result.Alerts = append(result.Alerts, pageAlert("dismissed-package", "acme/project", "low", "auto_dismissed"))
	result.Alerts = append(result.Alerts, pageAlert("fixed-package", "acme/project", "critical", "fixed"))
	r := httptest.NewRequest("GET", "/?state=open&page=2&q=package&severity=high&owner=octocat&ecosystem=npm", nil)
	p := makePage(r, nil, result, true, false)
	if p.MatchCount != 45 || len(p.Rows) != 20 || p.Page != 2 || p.Start != 21 || p.End != 40 || p.Pages != 3 {
		t.Fatalf("wrong server pagination: count=%d rows=%d page=%d start=%d end=%d", p.MatchCount, len(p.Rows), p.Page, p.Start, p.End)
	}
	res := httptest.NewRecorder()
	writeCSV(res, p)
	records, err := csv.NewReader(strings.NewReader(res.Body.String())).ReadAll()
	if err != nil || len(records) != 46 {
		t.Fatalf("export should contain all matches across pages: rows=%d err=%v", len(records), err)
	}
	u, err := url.Parse(p.PageURL(3))
	if err != nil || u.Query().Get("page") != "3" || u.Query().Get("q") != "package" || u.Query().Get("ecosystem") != "npm" {
		t.Fatal("pagination dropped active filters")
	}
	r = httptest.NewRequest("GET", "/?state=dismissed&q=acme", nil)
	p = makePage(r, nil, result, true, false)
	if len(p.Rows) != 1 || p.Rows[0].State != "auto_dismissed" {
		t.Fatal("automatically dismissed alerts were omitted")
	}
	r = httptest.NewRequest("GET", "/?state=fixed&q=CVE-2026-12345&page=9999", nil)
	p = makePage(r, nil, result, true, false)
	if len(p.Rows) != 1 || p.Page != 1 {
		t.Fatal("CVE filtering or page bounds failed")
	}
}

func TestHTMLAndCSVRejectUnsafeContent(t *testing.T) {
	alert := pageAlert(`<script>alert(1)</script>`, "octocat/project", "high", "open")
	alert.HTMLURL = "javascript:alert(1)"
	alert.SecurityAdvisory.Summary = "  =HYPERLINK(\"https://evil.example\")"
	client := &fakeGitHub{fetch: func(context.Context, []github.Connection) github.Result {
		return github.Result{Alerts: []github.Alert{alert}}
	}}
	handler := New(client, false)
	cookie := browserSession(t, handler)
	formRequest(handler, "/connections", url.Values{"token": {"test-secret"}}, cookie)
	res := request(handler, "GET", "/", "", cookie)
	if res.Code != 200 || strings.Contains(res.Body.String(), `<script>alert(1)</script>`) || strings.Contains(res.Body.String(), `href="javascript:`) {
		t.Fatal("unsafe alert content was rendered")
	}
	res = request(handler, "GET", "/alerts.csv", "", cookie)
	records, err := csv.NewReader(strings.NewReader(res.Body.String())).ReadAll()
	if err != nil || !strings.HasPrefix(records[1][5], "'") || records[1][8] != "" {
		t.Fatal("CSV did not escape the formula or omit the unsafe link")
	}
}

func TestRefreshFormPreservesFiltersAndPartialResults(t *testing.T) {
	client := &fakeGitHub{fetch: func(context.Context, []github.Connection) github.Result {
		return github.Result{Alerts: []github.Alert{pageAlert("partial-package", "octocat/project", "high", "open")}, Errors: []github.SourceError{{Source: "restricted", Message: "Permission required"}}}
	}}
	handler := New(client, false)
	cookie := browserSession(t, handler)
	res := formRequest(handler, "/refresh?state=all&q=partial", nil, cookie)
	if res.Code != 303 || res.Header().Get("Location") != "/?state=all&q=partial" {
		t.Fatal("refresh did not preserve the current view")
	}
	res = request(handler, "GET", "/?state=all&q=partial", "", cookie)
	if !strings.Contains(res.Body.String(), "Alert totals may be incomplete") || !strings.Contains(res.Body.String(), "partial-package") {
		t.Fatal("partial results or the completeness warning was lost")
	}
}

func TestConnectionChangesDiscardInFlightResults(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	client := &fakeGitHub{fetch: func(context.Context, []github.Connection) github.Result {
		close(started)
		<-release
		return github.Result{Alerts: []github.Alert{pageAlert("stale-package", "octocat/project", "high", "open")}}
	}}
	handler := New(client, false)
	cookie := browserSession(t, handler)
	created := request(handler, "POST", "/api/connections", `{"token":"test-secret"}`, cookie)
	var conn github.Connection
	_ = json.Unmarshal(created.Body.Bytes(), &conn)
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- request(handler, "GET", "/api/alerts", "", cookie) }()
	<-started
	request(handler, "DELETE", "/api/connections/"+conn.ID, "", cookie)
	close(release)
	res := <-done
	if res.Code != 409 {
		t.Fatalf("results from a disconnected source were accepted: %d", res.Code)
	}
	res = request(handler, "GET", "/", "", cookie)
	if strings.Contains(res.Body.String(), "stale-package") {
		t.Fatal("stale alerts were cached after disconnect")
	}
}

func TestNativeFormsRejectCrossOriginWrites(t *testing.T) {
	client := &fakeGitHub{}
	handler := New(client, false)
	for _, path := range []string{"/connections", "/refresh", "/connections/example/delete"} {
		req := httptest.NewRequest("POST", "http://localhost"+path, strings.NewReader("token=test-secret"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", "https://evil.example")
		req.Header.Set("Sec-Fetch-Site", "cross-site")
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != 403 {
			t.Fatalf("cross-origin native form accepted: %s status=%d", path, res.Code)
		}
	}
	if client.loginCalls.Load() != 0 {
		t.Fatal("cross-origin form reached GitHub")
	}
}
