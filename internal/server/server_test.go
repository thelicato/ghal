package server

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thelicato/ghal/internal/github"
	"github.com/thelicato/ghal/internal/storage"
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

func newTestServer(t *testing.T, client GitHub) http.Handler {
	t.Helper()
	store, err := storage.Open(filepath.Join(t.TempDir(), "ghal.db"), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	handler, err := New(client, store)
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func browserCookie() *http.Cookie { return &http.Cookie{Name: "test-browser", Value: rand.Text()} }

func TestConnectionLifecycleAndDeploymentSharing(t *testing.T) {
	client := &fakeGitHub{}
	handler := newTestServer(t, client)
	first, second := browserCookie(), browserCookie()
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
	if !strings.Contains(res.Body.String(), "Work") {
		t.Fatal("saved connections are not shared across browsers")
	}
	res = request(handler, "DELETE", "/api/connections/"+connection.ID, "", second)
	if res.Code != 204 {
		t.Fatalf("delete failed: %d", res.Code)
	}
	res = request(handler, "GET", "/api/connections", "", first)
	if strings.TrimSpace(res.Body.String()) != "[]" {
		t.Fatal("deleted connection is still visible")
	}
}

func TestConnectionDefaultsAndTokenValidation(t *testing.T) {
	handler := newTestServer(t, &fakeGitHub{})
	cookie := browserCookie()
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
			handler := newTestServer(t, client)
			res := request(handler, "POST", "/api/connections", body, browserCookie())
			if res.Code != 400 || client.loginCalls.Load() != 0 {
				t.Fatalf("invalid input reached GitHub: status=%d calls=%d", res.Code, client.loginCalls.Load())
			}
		})
	}
}

func TestCrossOriginWritesAreRejected(t *testing.T) {
	client := &fakeGitHub{}
	handler := newTestServer(t, client)
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

func TestAlertsUseSharedCredentialsWithoutExposingTokens(t *testing.T) {
	var fetched int
	client := &fakeGitHub{fetch: func(ctx context.Context, connections []github.Connection) github.Result {
		fetched = len(connections)
		if len(connections) > 0 && connections[0].Token != "test-secret" {
			t.Error("GitHub client did not receive the server-side credential")
		}
		return github.Result{Alerts: []github.Alert{}, Errors: []github.SourceError{}}
	}}
	handler := newTestServer(t, client)
	first, second := browserCookie(), browserCookie()
	request(handler, "POST", "/api/connections", `{"token":"test-secret"}`, first)
	res := request(handler, "GET", "/api/alerts", "", first)
	if res.Code != 200 || fetched != 1 || strings.Contains(res.Body.String(), "test-secret") {
		t.Fatal("alert fetch did not use saved credentials or exposed a token")
	}
	request(handler, "GET", "/api/alerts", "", second)
	if fetched != 1 {
		t.Fatal("another browser could not refresh shared connections")
	}
}

func TestConcurrentRefreshIsRejected(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	client := &fakeGitHub{fetch: func(ctx context.Context, connections []github.Connection) github.Result {
		close(started)
		<-release
		return github.Result{}
	}}
	handler := newTestServer(t, client)
	cookie := browserCookie()
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

func TestAPI404AndSecurityHeaders(t *testing.T) {
	res := request(newTestServer(t, &fakeGitHub{}), "GET", "/api/missing", "", nil)
	if res.Code != 404 || !strings.Contains(res.Header().Get("Content-Type"), "application/json") {
		t.Fatal("unknown API endpoint did not return a JSON error")
	}
	if res.Header().Get("Content-Security-Policy") == "" || res.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("missing browser security headers")
	}
}
