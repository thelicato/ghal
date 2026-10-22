package server

import (
	"context"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/thelicato/ghal/internal/github"
	"github.com/thelicato/ghal/internal/storage"
)

func TestAffectedRepositoriesCountsAcrossConnectionsFiltersAndPages(t *testing.T) {
	var result github.Result
	for i := range 25 {
		a := pageAlert("shared-package", "Acme/Project", "high", "open")
		a.Number = i + 1
		a.ConnectionIDs = []string{"work", "personal"}
		if i%2 == 0 {
			a.Repository.FullName = "acme/project"
		}
		result.Alerts = append(result.Alerts, a)
	}
	result.Alerts = append(result.Alerts,
		pageAlert("another-package", "acme/another", "low", "open"),
		pageAlert("other-owner", "other/project", "medium", "open"),
		pageAlert("accepted-package", "acme/accepted", "high", "open"),
		pageAlert("fixed-package", "acme/fixed", "critical", "fixed"),
		pageAlert("dismissed-package", "acme/dismissed", "high", "dismissed"),
		pageAlert("auto-dismissed-package", "acme/auto-dismissed", "low", "auto_dismissed"),
	)
	result.Errors = []github.SourceError{{Source: "restricted", Message: "Permission required"}}
	accepted := map[storage.AlertID]time.Time{
		storage.AlertKey("acme/project", 1): time.Now(), storage.AlertKey("acme/accepted", 1): time.Now(),
	}
	for _, path := range []string{
		"/", "/?page=2&sort=repository&direction=desc", "/?state=accepted", "/?state=all",
		"/?state=fixed&q=no-match&severity=critical&owner=missing&ecosystem=go",
	} {
		p := makePage(httptest.NewRequest("GET", path, nil), nil, result, true, false, accepted)
		if p.AffectedRepositories != 3 {
			t.Fatalf("%s: got %d affected repositories, want 3", path, p.AffectedRepositories)
		}
	}
	empty := makePage(httptest.NewRequest("GET", "/", nil), nil, github.Result{}, false, false, nil)
	if empty.AffectedRepositories != 0 {
		t.Fatal("empty results report affected repositories")
	}
}

func TestAffectedRepositoryStatUpdatesWithAcceptanceAndReopening(t *testing.T) {
	a := pageAlert("first-package", "acme/project", "high", "open")
	b := a
	b.Number = 2
	c := pageAlert("other-package", "acme/other", "low", "open")
	handler := newTestServer(t, &fakeGitHub{fetch: func(context.Context, []github.Connection) github.Result {
		return github.Result{Alerts: []github.Alert{a, b, c}}
	}})
	refreshAndWait(t, handler)
	assertCount := func(want int) {
		t.Helper()
		p := handler.currentPage(httptest.NewRequest("GET", "/", nil))
		if p.AffectedRepositories != want {
			t.Fatalf("got %d affected repositories, want %d", p.AffectedRepositories, want)
		}
	}
	assertCount(2)
	if page := request(handler, "GET", "/", "", nil).Body.String(); !strings.Contains(page, "Affected repositories") || !strings.Contains(page, "With unaccepted open alerts") {
		t.Fatal("repository stat or its scope is missing from the dashboard")
	}
	for _, step := range []struct {
		path   string
		values url.Values
		want   int
	}{
		{"/alerts/accept", acceptanceForm("acme/project", "1"), 2},
		{"/alerts/accept", acceptanceForm("acme/project", "2"), 1},
		{"/alerts/reopen", acceptanceForm("acme/project", "1"), 2},
		{"/alerts/accept-selected", url.Values{"alerts": {"acme/project#1", "acme/other#1"}}, 0},
	} {
		if res := formRequest(handler, step.path, step.values, nil); res.Code != 303 {
			t.Fatalf("acceptance action failed: %s status=%d", step.path, res.Code)
		}
		assertCount(step.want)
	}
}
