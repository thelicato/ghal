package server

import (
	"context"
	"encoding/csv"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/thelicato/ghal/internal/github"
	"github.com/thelicato/ghal/internal/storage"
)

func acceptanceForm(repo, number string) url.Values {
	return url.Values{"repository": {repo}, "number": {number}}
}

func TestAcceptIndividualAlertRefreshRestartAndReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ghal.db")
	store, err := storage.Open(path, "")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	a := pageAlert("accepted-package", "octocat/project", "critical", "open")
	a.ConnectionIDs = []string{"one", "two"}
	b := pageAlert("same-repository", "octocat/project", "high", "open")
	b.Number = 2
	c := pageAlert("same-number", "acme/project", "low", "open")
	var fetches atomic.Int32
	client := &fakeGitHub{fetch: func(context.Context, []github.Connection) github.Result {
		fetches.Add(1)
		return github.Result{Alerts: []github.Alert{a, b, c}}
	}}
	handler, err := New(client, store)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(handler.Close)
	formRequest(handler, "/connections", url.Values{"token": {"test-secret"}}, nil)
	waitForRefresh(t, handler)
	query := "?state=all&sort=dependency&direction=desc&q=package&page=2"
	res := formRequest(handler, "/alerts/accept"+query, acceptanceForm("OCTOCAT/Project", "1"), nil)
	if res.Code != 303 || res.Header().Get("Location") != "/"+query {
		t.Fatalf("accept did not preserve the view: %d %s", res.Code, res.Body.String())
	}
	accepted, err := store.ListAcceptedAlerts(context.Background())
	id := storage.AlertKey("octocat/project", 1)
	if err != nil || len(accepted) != 1 || accepted[id].IsZero() {
		t.Fatal("acceptance was not persisted for precisely one repository alert")
	}
	res = formRequest(handler, "/alerts/accept", acceptanceForm("octocat/project", "1"), browserCookie())
	again, err := store.ListAcceptedAlerts(context.Background())
	if res.Code != 303 || err != nil || !again[id].Equal(accepted[id]) {
		t.Fatal("repeated acceptance changed its timestamp")
	}
	res = request(handler, "GET", "/", "", browserCookie())
	body := res.Body.String()
	if strings.Contains(body, "accepted-package") || !strings.Contains(body, "same-repository") || !strings.Contains(body, "same-number") || !strings.Contains(body, "<strong>0</strong>") {
		t.Fatal("acceptance hid another alert or left the accepted alert in Open")
	}
	res = request(handler, "GET", "/?state=accepted", "", browserCookie())
	body = res.Body.String()
	if !strings.Contains(body, "accepted-package") || !strings.Contains(body, "GitHub: Open") || !strings.Contains(body, "Reopen") || strings.Contains(body, "same-repository") {
		t.Fatal("the shared Accepted view lost the alert, its GitHub state, or its reopen action")
	}
	res = request(handler, "GET", "/alerts.csv?state=all", "", nil)
	records, err := csv.NewReader(strings.NewReader(res.Body.String())).ReadAll()
	if err != nil || len(records) != 4 || records[1][4] != "open" || records[1][9] != "Yes" || records[1][10] == "" || records[2][9] != "No" {
		t.Fatal("CSV did not retain GitHub state and local acceptance independently")
	}
	if fetches.Load() != 1 {
		t.Fatal("accepting or viewing alerts repeated a GitHub fetch")
	}
	refreshAndWait(t, handler)
	res = request(handler, "GET", "/api/alerts", "", nil)
	if res.Code != 200 || !strings.Contains(res.Body.String(), "accepted-package") || strings.Contains(res.Body.String(), "accepted_at") {
		t.Fatal("local acceptance changed the raw GitHub response")
	}
	if res = request(handler, "GET", "/?state=accepted", "", nil); !strings.Contains(res.Body.String(), "accepted-package") {
		t.Fatal("refresh lost local acceptance")
	}
	handler.Close()
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = storage.Open(path, "")
	if err != nil {
		t.Fatal(err)
	}
	handler, err = New(client, store)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(handler.Close)
	waitForRefresh(t, handler)
	if res = request(handler, "GET", "/?state=accepted", "", nil); !strings.Contains(res.Body.String(), "accepted-package") {
		t.Fatal("restart lost local acceptance")
	}
	res = formRequest(handler, "/alerts/reopen?state=accepted&sort=updated&direction=desc", acceptanceForm("octocat/project", "1"), nil)
	if res.Code != 303 {
		t.Fatalf("reopen failed: %d", res.Code)
	}
	if res = request(handler, "GET", "/", "", nil); !strings.Contains(res.Body.String(), "accepted-package") {
		t.Fatal("reopening did not restore the open alert")
	}
	accepted, err = store.ListAcceptedAlerts(context.Background())
	if err != nil || len(accepted) != 0 {
		t.Fatal("reopen was not persisted")
	}
}

func TestAcceptanceValidationAndFailedWrites(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "ghal.db"), "")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	client := &fakeGitHub{fetch: func(context.Context, []github.Connection) github.Result {
		return github.Result{Alerts: []github.Alert{
			pageAlert("open-package", "octocat/project", "high", "open"),
			pageAlert("fixed-package", "octocat/fixed", "high", "fixed"),
		}}
	}}
	handler, err := New(client, store)
	if err != nil {
		t.Fatal(err)
	}
	defer handler.Close()
	refreshAndWait(t, handler)
	for _, test := range []struct {
		values url.Values
		status int
	}{
		{acceptanceForm("octocat/project", "0"), 400}, {acceptanceForm("octocat/project", "bad"), 400},
		{acceptanceForm("", "1"), 400}, {acceptanceForm("octocat/missing", "1"), 404},
		{acceptanceForm("octocat/project", "2"), 404}, {acceptanceForm("octocat/fixed", "1"), 409},
	} {
		if res := formRequest(handler, "/alerts/accept", test.values, nil); res.Code != test.status {
			t.Fatalf("unexpected validation result: %v status=%d", test.values, res.Code)
		}
	}
	// A failed accept must not change the shared in-memory view.
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	res := formRequest(handler, "/alerts/accept", acceptanceForm("octocat/project", "1"), nil)
	if res.Code != http.StatusInternalServerError || !strings.Contains(res.Body.String(), "Could not save accepted alert") {
		t.Fatal("SQLite write failure was not reported")
	}
	if res = request(handler, "GET", "/?state=accepted", "", nil); strings.Contains(res.Body.String(), "open-package") {
		t.Fatal("failed accept changed the live state")
	}
}

func TestFailedReopenPreservesAcceptance(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "ghal.db"), "")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.AcceptAlert(context.Background(), storage.AlertKey("octocat/project", 1)); err != nil {
		t.Fatal(err)
	}
	client := &fakeGitHub{fetch: func(context.Context, []github.Connection) github.Result {
		return github.Result{Alerts: []github.Alert{pageAlert("accepted-package", "octocat/project", "high", "open")}}
	}}
	handler, err := New(client, store)
	if err != nil {
		t.Fatal(err)
	}
	defer handler.Close()
	refreshAndWait(t, handler)
	_ = store.Close()
	res := formRequest(handler, "/alerts/reopen", acceptanceForm("octocat/project", "1"), nil)
	if res.Code != 500 {
		t.Fatal("failed reopen was not reported")
	}
	if res = request(handler, "GET", "/?state=accepted", "", nil); !strings.Contains(res.Body.String(), "accepted-package") {
		t.Fatal("failed reopen removed the live acceptance")
	}
}

func TestAcceptanceDuringRefreshSurvivesNewResults(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	client := &fakeGitHub{fetch: func(context.Context, []github.Connection) github.Result {
		if calls.Add(1) == 2 {
			close(started)
			<-release
		}
		return github.Result{Alerts: []github.Alert{pageAlert("accepted-package", "octocat/project", "high", "open")}}
	}}
	handler := newTestServer(t, client)
	refreshAndWait(t, handler)
	refreshed := request(handler, "POST", "/api/refresh", "", nil)
	<-started
	res := formRequest(handler, "/alerts/accept", acceptanceForm("octocat/project", "1"), nil)
	close(release)
	waitForRefresh(t, handler)
	if res.Code != 303 || refreshed.Code != 202 {
		t.Fatalf("acceptance conflicted with refresh: accept=%d refresh=%d", res.Code, refreshed.Code)
	}
	if res = request(handler, "GET", "/?state=accepted", "", nil); !strings.Contains(res.Body.String(), "accepted-package") {
		t.Fatal("in-flight refresh overwrote a newer local decision")
	}
}
