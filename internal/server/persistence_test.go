package server

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"dbaui/internal/github"
	"dbaui/internal/storage"
)

func TestSavedConnectionsAndPATsWorkAfterRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dbaui.db")
	store, err := storage.Open(path, "")
	if err != nil {
		t.Fatal(err)
	}
	client := &fakeGitHub{fetch: func(ctx context.Context, connections []github.Connection) github.Result {
		if len(connections) != 1 || connections[0].Token != "persistent-test-secret" {
			t.Error("restored PAT did not reach the GitHub client")
		}
		return github.Result{Alerts: []github.Alert{pageAlert("restored-package", "octocat/project", "high", "open")}}
	}}
	handler, err := New(client, store)
	if err != nil {
		t.Fatal(err)
	}
	created := request(handler, "POST", "/api/connections", `{"label":"Saved work","token":"persistent-test-secret","accounts":["octocat"],"organizations":["acme"]}`, nil)
	if created.Code != 201 {
		t.Fatalf("connection was not saved: %d", created.Code)
	}
	var conn github.Connection
	_ = json.Unmarshal(created.Body.Bytes(), &conn)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = storage.Open(path, "")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	handler, err = New(client, store)
	if err != nil {
		t.Fatal(err)
	}
	listing := request(handler, "GET", "/api/connections", "", nil)
	if !strings.Contains(listing.Body.String(), "Saved work") || strings.Contains(listing.Body.String(), "persistent-test-secret") {
		t.Fatal("connection was not restored or listing exposes its PAT")
	}
	if len(listing.Result().Cookies()) != 0 {
		t.Fatal("shared connections still depend on a browser session cookie")
	}
	res := request(handler, "GET", "/api/alerts", "", browserCookie())
	if res.Code != 200 || !strings.Contains(res.Body.String(), "restored-package") || strings.Contains(res.Body.String(), "persistent-test-secret") {
		t.Fatal("restored connection could not fetch alerts safely")
	}
	res = request(handler, "DELETE", "/api/connections/"+conn.ID, "", nil)
	if res.Code != 204 {
		t.Fatal("restored connection could not be deleted")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = storage.Open(path, "")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	handler, err = New(client, store)
	if err != nil {
		t.Fatal(err)
	}
	listing = request(handler, "GET", "/api/connections", "", nil)
	if strings.TrimSpace(listing.Body.String()) != "[]" {
		t.Fatal("deleted connection reappeared after restart")
	}
}

func TestDatabaseFailuresDoNotChangeLiveConfiguration(t *testing.T) {
	for _, operation := range []string{"create", "delete"} {
		t.Run(operation, func(t *testing.T) {
			store, err := storage.Open(filepath.Join(t.TempDir(), "dbaui.db"), "")
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			handler, err := New(&fakeGitHub{}, store)
			if err != nil {
				t.Fatal(err)
			}
			var conn github.Connection
			if operation == "delete" {
				res := request(handler, "POST", "/api/connections", `{"label":"Saved","token":"test-secret"}`, nil)
				_ = json.Unmarshal(res.Body.Bytes(), &conn)
			}
			_ = store.Close()
			var status int
			if operation == "create" {
				status = request(handler, "POST", "/api/connections", `{"label":"Unsaved","token":"test-secret"}`, nil).Code
			} else {
				status = request(handler, "DELETE", "/api/connections/"+conn.ID, "", nil).Code
			}
			if status != 500 {
				t.Fatalf("database failure was not reported: %d", status)
			}
			listing := request(handler, "GET", "/api/connections", "", nil).Body.String()
			if operation == "create" && listing != "[]\n" {
				t.Fatal("failed write changed the live configuration")
			}
			if operation == "delete" && !strings.Contains(listing, "Saved") {
				t.Fatal("failed delete changed the live configuration")
			}
			if res := request(handler, "GET", "/api/health", "", nil); res.Code != http.StatusServiceUnavailable {
				t.Fatal("healthcheck accepted an unavailable database")
			}
		})
	}
}
