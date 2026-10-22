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
	login      func(context.Context, string) (string, error)
	fetch      func(context.Context, []github.Connection) github.Result
}

func (f *fakeGitHub) Login(ctx context.Context, token string) (string, error) {
	f.loginCalls.Add(1)
	if f.login != nil {
		return f.login(ctx, token)
	}
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

func newTestServer(t *testing.T, client GitHub) *Server {
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
	t.Cleanup(handler.Close)
	return handler
}

func waitForRefresh(t *testing.T, handler http.Handler) {
	t.Helper()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		res := request(handler, "GET", "/api/status", "", nil)
		var status syncStatus
		if err := json.Unmarshal(res.Body.Bytes(), &status); err != nil {
			t.Fatal(err)
		}
		if !status.Refreshing {
			return
		}
		select {
		case <-tick.C:
		case <-deadline.C:
			t.Fatal("background refresh did not finish")
		}
	}
}

func refreshAndWait(t *testing.T, handler http.Handler) {
	t.Helper()
	if res := request(handler, "POST", "/api/refresh", "", nil); res.Code != http.StatusAccepted {
		t.Fatalf("refresh was not queued: %d", res.Code)
	}
	waitForRefresh(t, handler)
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
	if connection.ID == "" || connection.Label != "Work" || len(connection.Accounts) != 1 || connection.Login != "" {
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
	if res.Code != 201 {
		t.Fatalf("connection was not saved: %d", res.Code)
	}
	waitForRefresh(t, handler)
	res = request(handler, "GET", "/api/connections", "", cookie)
	var connections []github.Connection
	_ = json.Unmarshal(res.Body.Bytes(), &connections)
	var connection github.Connection
	if len(connections) != 1 {
		t.Fatal("saved connection missing")
	}
	connection = connections[0]
	if connection.Label != "octocat" || len(connection.Accounts) != 1 || connection.Accounts[0] != "octocat" {
		t.Fatalf("authenticated account was not used by default: %+v", connection)
	}
	res = request(handler, "POST", "/api/connections", `{"token":"invalid"}`, cookie)
	if res.Code != 201 {
		t.Fatalf("token should be verified in the background: status=%d", res.Code)
	}
	waitForRefresh(t, handler)
	if res = request(handler, "GET", "/connections", "", nil); !strings.Contains(res.Body.String(), "GitHub rejected this token") {
		t.Fatal("background token failure was not displayed")
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
	waitForRefresh(t, handler)
	res := request(handler, "GET", "/api/alerts", "", first)
	if res.Code != 200 || fetched != 1 || strings.Contains(res.Body.String(), "test-secret") {
		t.Fatal("alert fetch did not use saved credentials or exposed a token")
	}
	request(handler, "GET", "/api/alerts", "", second)
	if fetched != 1 {
		t.Fatal("another browser could not refresh shared connections")
	}
}

func TestConcurrentRefreshRequestsShareBackgroundJob(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	var calls atomic.Int32
	client := &fakeGitHub{fetch: func(ctx context.Context, connections []github.Connection) github.Result {
		if calls.Add(1) == 1 {
			close(started)
		}
		select {
		case <-release:
		case <-ctx.Done():
		}
		return github.Result{}
	}}
	handler := newTestServer(t, client)
	cookie := browserCookie()
	first := request(handler, "POST", "/api/refresh", "", cookie)
	<-started
	res := request(handler, "POST", "/api/refresh", "", cookie)
	if first.Code != 202 || res.Code != 202 || calls.Load() != 1 {
		t.Fatalf("overlapping refreshes did not share a job: first=%d second=%d calls=%d", first.Code, res.Code, calls.Load())
	}
	if res = request(handler, "GET", "/api/alerts", "", cookie); res.Code != 200 {
		t.Fatal("cached alerts were unavailable during refresh")
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
