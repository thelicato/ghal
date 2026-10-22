package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"dbaui/internal/github"
)

type fakeGitHub struct {
	loginCalls atomic.Int32
	fetch      func(context.Context, []github.Connection) github.Result
}

func (f *fakeGitHub) Login(ctx context.Context, token string) (string, error) {
	f.loginCalls.Add(1)
	if token == "invalid" {
		return "", &github.APIError{Status: 401}
	}
	return "octocat", nil
}

func (f *fakeGitHub) Fetch(ctx context.Context, connections []github.Connection) github.Result {
	if f.fetch != nil {
		return f.fetch(ctx, connections)
	}
	return github.Result{Alerts: []github.Alert{}, Errors: []github.SourceError{}, FetchedAt: time.Now()}
}

func request(handler http.Handler, method, path, body string, cookie *http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "http://localhost"+path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func browserSession(t *testing.T, handler http.Handler) *http.Cookie {
	t.Helper()
	res := request(handler, "GET", "/api/connections", "", nil)
	cookies := res.Result().Cookies()
	if res.Code != 200 || len(cookies) != 1 {
		t.Fatalf("failed to create browser session: status=%d", res.Code)
	}
	if !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatal("session cookie is missing security attributes")
	}
	return cookies[0]
}

func TestConnectionLifecycleAndSessionIsolation(t *testing.T) {
	client := &fakeGitHub{}
	handler := New(client, false)
	first, second := browserSession(t, handler), browserSession(t, handler)
	res := request(handler, "POST", "/api/connections", `{"label":"Work","token":"test-secret","accounts":["Octocat","octocat"],"organizations":["acme"]}`, first)
	if res.Code != 201 {
		t.Fatalf("create failed: %s", res.Body.String())
	}
	var connection github.Connection
	if err := json.Unmarshal(res.Body.Bytes(), &connection); err != nil {
		t.Fatal(err)
	}
	if connection.ID == "" || connection.Label != "Work" || len(connection.Accounts) != 1 || connection.Login != "octocat" {
		t.Fatalf("unexpected connection: %+v", connection)
	}
	if strings.Contains(res.Body.String(), "test-secret") || strings.Contains(res.Body.String(), `"token"`) {
		t.Fatal("creation response exposes the PAT")
	}
	res = request(handler, "GET", "/api/connections", "", first)
	if strings.Contains(res.Body.String(), "test-secret") || !strings.Contains(res.Body.String(), "Work") {
		t.Fatal("connection listing is missing metadata or exposes the PAT")
	}
	if res.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("connection response can be cached")
	}
	res = request(handler, "GET", "/api/connections", "", second)
	if strings.TrimSpace(res.Body.String()) != "[]" {
		t.Fatal("connections leaked into another browser session")
	}
	res = request(handler, "DELETE", "/api/connections/"+connection.ID, "", second)
	if res.Code != 404 {
		t.Fatal("another browser can delete this connection")
	}
	res = request(handler, "DELETE", "/api/connections/"+connection.ID, "", first)
	if res.Code != 204 {
		t.Fatalf("delete failed: %d", res.Code)
	}
	res = request(handler, "GET", "/api/connections", "", first)
	if strings.TrimSpace(res.Body.String()) != "[]" {
		t.Fatal("deleted connection is still visible")
	}
}

func TestConnectionDefaultsAndTokenValidation(t *testing.T) {
	handler := New(&fakeGitHub{}, false)
	cookie := browserSession(t, handler)
	res := request(handler, "POST", "/api/connections", `{"token":"test-secret"}`, cookie)
	var connection github.Connection
	_ = json.Unmarshal(res.Body.Bytes(), &connection)
	if res.Code != 201 || connection.Label != "octocat" || len(connection.Accounts) != 1 || connection.Accounts[0] != "octocat" {
		t.Fatalf("authenticated account was not used by default: %+v", connection)
	}
	res = request(handler, "POST", "/api/connections", `{"token":"invalid"}`, cookie)
	if res.Code != 422 {
		t.Fatalf("invalid token should be rejected: status=%d", res.Code)
	}
	res = request(handler, "POST", "/api/connections", `{"token":"test-secret","organizations":["acme"]}`, cookie)
	_ = json.Unmarshal(res.Body.Bytes(), &connection)
	if res.Code != 201 || len(connection.Accounts) != 0 || len(connection.Organizations) != 1 {
		t.Fatal("organization-only connection unexpectedly includes a personal account")
	}
}

func TestInvalidConnectionsNeverReachGitHub(t *testing.T) {
	for _, body := range []string{
		`{}`, `{"token":"has\nnewline"}`, `{"token":"secret","accounts":["https://github.com/octo"]}`,
		`{"token":"secret","accounts":["../octo"]}`, `{"token":"secret","accounts":["acme"],"organizations":["ACME"]}`,
		`{"token":"secret","unexpected":true}`, `{"token":"secret"} {}`, `{"token":"` + strings.Repeat("x", 17000) + `"}`,
	} {
		t.Run(body[:min(50, len(body))], func(t *testing.T) {
			client := &fakeGitHub{}
			handler := New(client, false)
			res := request(handler, "POST", "/api/connections", body, browserSession(t, handler))
			if res.Code != 400 || client.loginCalls.Load() != 0 {
				t.Fatalf("invalid input reached GitHub: status=%d calls=%d", res.Code, client.loginCalls.Load())
			}
		})
	}
}

func TestCrossOriginWritesAreRejected(t *testing.T) {
	client := &fakeGitHub{}
	handler := New(client, false)
	req := httptest.NewRequest("POST", "http://localhost/api/connections", strings.NewReader(`{"token":"test-secret"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://evil.example")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != 403 || client.loginCalls.Load() != 0 {
		t.Fatal("cross-origin connection write was accepted")
	}
}

func TestAlertsOnlyUseCurrentBrowserCredentials(t *testing.T) {
	var fetched int
	client := &fakeGitHub{fetch: func(ctx context.Context, connections []github.Connection) github.Result {
		fetched = len(connections)
		if len(connections) > 0 && connections[0].Token != "test-secret" {
			t.Error("GitHub client did not receive the server-side credential")
		}
		return github.Result{Alerts: []github.Alert{}, Errors: []github.SourceError{}}
	}}
	handler := New(client, false)
	first, second := browserSession(t, handler), browserSession(t, handler)
	request(handler, "POST", "/api/connections", `{"token":"test-secret"}`, first)
	res := request(handler, "GET", "/api/alerts", "", first)
	if res.Code != 200 || fetched != 1 || strings.Contains(res.Body.String(), "test-secret") {
		t.Fatal("alert fetch did not preserve credential isolation")
	}
	request(handler, "GET", "/api/alerts", "", second)
	if fetched != 0 {
		t.Fatal("another browser's credentials were used")
	}
}

func TestConcurrentRefreshIsRejected(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	client := &fakeGitHub{fetch: func(ctx context.Context, connections []github.Connection) github.Result {
		close(started)
		<-release
		return github.Result{}
	}}
	handler := New(client, false)
	cookie := browserSession(t, handler)
	done := make(chan struct{})
	go func() { defer close(done); request(handler, "GET", "/api/alerts", "", cookie) }()
	<-started
	res := request(handler, "GET", "/api/alerts", "", cookie)
	close(release)
	<-done
	if res.Code != 409 {
		t.Fatalf("overlapping refresh was accepted: %d", res.Code)
	}
}

func TestExpiredSessionsAndSecureCookies(t *testing.T) {
	s := sessions{entries: make(map[string]sessionEntry), secure: true}
	res := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "http://localhost/api/connections", nil)
	first := s.get(res, req)
	cookie := res.Result().Cookies()[0]
	if !cookie.Secure {
		t.Fatal("HTTPS deployment cookie is not secure")
	}
	s.entries[cookie.Value] = sessionEntry{value: first, expires: time.Now().Add(-time.Minute)}
	req.AddCookie(cookie)
	second := s.get(httptest.NewRecorder(), req)
	if first == second {
		t.Fatal("expired browser session was reused")
	}
	if _, exists := s.entries[cookie.Value]; exists {
		t.Fatal("expired credentials were not evicted")
	}
}

func TestAPI404AndSecurityHeaders(t *testing.T) {
	res := request(New(&fakeGitHub{}, false), "GET", "/api/missing", "", nil)
	if res.Code != 404 || !strings.Contains(res.Header().Get("Content-Type"), "application/json") {
		t.Fatal("unknown API endpoint did not return a JSON error")
	}
	if res.Header().Get("Content-Security-Policy") == "" || res.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("missing browser security headers")
	}
}
