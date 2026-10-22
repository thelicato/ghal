package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thelicato/ghal/internal/github"
	"github.com/thelicato/ghal/internal/storage"
)

func promptly(t *testing.T, action func() *httptest.ResponseRecorder) *httptest.ResponseRecorder {
	t.Helper()
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- action() }()
	select {
	case res := <-done:
		return res
	case <-time.After(time.Second):
		t.Fatal("request waited for background GitHub work")
		return nil
	}
}

func TestConnectionCreationDoesNotWaitForTokenVerification(t *testing.T) {
	for _, api := range []bool{false, true} {
		name := "form"
		if api {
			name = "api"
		}
		t.Run(name, func(t *testing.T) {
			started, release := make(chan struct{}), make(chan struct{})
			client := &fakeGitHub{login: func(ctx context.Context, token string) (string, error) {
				close(started)
				select {
				case <-release:
					return "octocat", nil
				case <-ctx.Done():
					return "", ctx.Err()
				}
			}}
			handler := newTestServer(t, client)
			path, body, media, code := "/connections", "token=background-secret", "application/x-www-form-urlencoded", 303
			if api {
				path, body, media, code = "/api/connections", `{"token":"background-secret"}`, "application/json", 201
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			req := httptest.NewRequest("POST", "http://localhost"+path, strings.NewReader(body)).WithContext(ctx)
			req.Header.Set("Content-Type", media)
			res := promptly(t, func() *httptest.ResponseRecorder {
				res := httptest.NewRecorder()
				handler.ServeHTTP(res, req)
				return res
			})
			cancel() // Finishing or leaving the page must not cancel the job.
			<-started
			if res.Code != code || strings.Contains(res.Body.String(), "background-secret") {
				t.Fatalf("connection was not saved safely: status=%d", res.Code)
			}
			if !handler.status().Refreshing {
				t.Fatal("token verification was tied to the request context")
			}
			connections, err := handler.store.List(context.Background())
			if err != nil || len(connections) != 1 || connections[0].Login != "" {
				t.Fatal("pending connection was not persisted before verification")
			}
			page := request(handler, "GET", "/connections", "", nil).Body.String()
			if !strings.Contains(page, "Token verification pending") || strings.Contains(page, "background-secret") {
				t.Fatal("pending connection is missing or exposes its PAT")
			}
			close(release)
			waitForRefresh(t, handler)
			connections, err = handler.store.List(context.Background())
			if err != nil || connections[0].Login != "octocat" || connections[0].Label != "octocat" || len(connections[0].Accounts) != 1 || connections[0].Accounts[0] != "octocat" {
				t.Fatal("verified defaults were not persisted")
			}
		})
	}
}

func TestRefreshReturnsPromptlyAndServesCachedAlerts(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	client := &fakeGitHub{fetch: func(ctx context.Context, connections []github.Connection) github.Result {
		if calls.Add(1) == 1 {
			return github.Result{Alerts: []github.Alert{pageAlert("cached-package", "octocat/project", "high", "open")}}
		}
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
			return github.Result{}
		}
		return github.Result{
			Alerts: []github.Alert{pageAlert("updated-package", "octocat/project", "high", "open")},
			Errors: []github.SourceError{{Source: "restricted", Message: "Permission required"}},
		}
	}}
	handler := newTestServer(t, client)
	refreshAndWait(t, handler)
	version := handler.status().Version
	query := "?state=all&q=package&sort=dependency&direction=desc&page=2"
	res := promptly(t, func() *httptest.ResponseRecorder { return formRequest(handler, "/refresh"+query, nil, nil) })
	if res.Code != 303 || res.Header().Get("Location") != "/"+query {
		t.Fatal("background refresh lost the current view")
	}
	<-started
	for _, path := range []string{"/", "/api/alerts", "/alerts.csv"} {
		res := promptly(t, func() *httptest.ResponseRecorder { return request(handler, "GET", path, "", nil) })
		if res.Code != 200 || !strings.Contains(res.Body.String(), "cached-package") {
			t.Fatalf("cached alerts unavailable during refresh: %s", path)
		}
	}
	res = promptly(t, func() *httptest.ResponseRecorder { return request(handler, "POST", "/api/refresh", "", nil) })
	if res.Code != 202 || calls.Load() != 2 || !handler.status().Refreshing {
		t.Fatal("a second refresh did not share the active background job")
	}
	close(release)
	waitForRefresh(t, handler)
	page := request(handler, "GET", "/", "", nil).Body.String()
	if !strings.Contains(page, "updated-package") || strings.Contains(page, "cached-package") || !strings.Contains(page, "Permission required") || handler.status().Version <= version {
		t.Fatal("completed background result or partial failure was not published")
	}
}

func TestNewConnectionDuringRefreshKeepsCacheAndFetchesLatestConfiguration(t *testing.T) {
	started, latest, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	client := &fakeGitHub{fetch: func(ctx context.Context, connections []github.Connection) github.Result {
		switch calls.Add(1) {
		case 1:
			a := pageAlert("cached-package", "octocat/project", "high", "open")
			a.ConnectionIDs = []string{connections[0].ID}
			return github.Result{Alerts: []github.Alert{a}}
		case 2:
			close(started)
			<-ctx.Done()
			return github.Result{Alerts: []github.Alert{pageAlert("obsolete-package", "octocat/project", "high", "open")}}
		default:
			if len(connections) != 2 {
				t.Error("follow-up refresh did not include the new connection")
			}
			close(latest)
			select {
			case <-release:
			case <-ctx.Done():
			}
			return github.Result{Alerts: []github.Alert{pageAlert("latest-package", "octocat/project", "high", "open")}}
		}
	}}
	handler := newTestServer(t, client)
	request(handler, "POST", "/api/connections", `{"token":"first-secret"}`, nil)
	waitForRefresh(t, handler)
	request(handler, "POST", "/api/refresh", "", nil)
	<-started
	res := promptly(t, func() *httptest.ResponseRecorder {
		return formRequest(handler, "/connections", url.Values{"token": {"second-secret"}}, nil)
	})
	if res.Code != 303 {
		t.Fatal("connection could not be added during refresh")
	}
	<-latest
	page := request(handler, "GET", "/", "", nil).Body.String()
	if !strings.Contains(page, "cached-package") || strings.Contains(page, "obsolete-package") {
		t.Fatal("adding a connection discarded the cache or published an obsolete result")
	}
	close(release)
	waitForRefresh(t, handler)
	if page = request(handler, "GET", "/", "", nil).Body.String(); !strings.Contains(page, "latest-package") {
		t.Fatal("latest connection configuration was not refreshed automatically")
	}
}

func TestPendingConnectionRetriesAfterRestart(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "ghal.db"), "")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	started := make(chan struct{})
	client := &fakeGitHub{login: func(ctx context.Context, token string) (string, error) {
		close(started)
		<-ctx.Done()
		return "", ctx.Err()
	}}
	handler, err := New(client, store)
	if err != nil {
		t.Fatal(err)
	}
	defer handler.Close()
	request(handler, "POST", "/api/connections", `{"token":"pending-secret"}`, nil)
	<-started
	closed := make(chan struct{})
	go func() { handler.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not cancel and finish token verification")
	}
	restarted, err := New(&fakeGitHub{}, store)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	waitForRefresh(t, restarted)
	connections, err := store.List(context.Background())
	if err != nil || len(connections) != 1 || connections[0].Login != "octocat" {
		t.Fatal("pending connection was not verified after restart")
	}
}

func TestBackgroundVerificationFailureIsSafeAndRetryable(t *testing.T) {
	var calls atomic.Int32
	client := &fakeGitHub{login: func(context.Context, string) (string, error) {
		if calls.Add(1) == 1 {
			return "", errors.New("upstream error containing retry-secret")
		}
		return "octocat", nil
	}}
	handler := newTestServer(t, client)
	request(handler, "POST", "/api/connections", `{"token":"retry-secret"}`, nil)
	waitForRefresh(t, handler)
	for _, path := range []string{"/", "/connections", "/api/alerts", "/api/connections", "/api/status"} {
		res := request(handler, "GET", path, "", nil)
		if strings.Contains(res.Body.String(), "retry-secret") {
			t.Fatalf("background error exposed a PAT: %s", path)
		}
	}
	refreshAndWait(t, handler)
	res := request(handler, "GET", "/api/connections", "", nil)
	var connections []github.Connection
	if err := json.Unmarshal(res.Body.Bytes(), &connections); err != nil || connections[0].Login != "octocat" {
		t.Fatal("failed verification could not be retried")
	}
	if page := request(handler, "GET", "/connections", "", nil).Body.String(); strings.Contains(page, "Could not verify this token") {
		t.Fatal("successful retry left the previous failure visible")
	}
}
