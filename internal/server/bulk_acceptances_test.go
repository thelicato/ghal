package server

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/thelicato/ghal/internal/github"
	"github.com/thelicato/ghal/internal/storage"
)

func TestBulkAcceptancePreservesViewAndOnlyChangesSelectedAlerts(t *testing.T) {
	var result github.Result
	for i := 1; i <= 25; i++ {
		a := pageAlert(fmt.Sprintf("package-%02d", i), "octocat/project", "high", "open")
		a.Number = i
		if i == 22 {
			a.State = "fixed"
		}
		if i == 24 {
			a.State = "dismissed"
		}
		result.Alerts = append(result.Alerts, a)
	}
	var fetches atomic.Int32
	handler := newTestServer(t, &fakeGitHub{fetch: func(context.Context, []github.Connection) github.Result {
		fetches.Add(1)
		return result
	}})
	refreshAndWait(t, handler)
	formRequest(handler, "/alerts/accept", acceptanceForm("octocat/project", "23"), nil)
	query := "?state=all&q=package&severity=high&owner=octocat&ecosystem=npm&sort=dependency&direction=asc&page=2"
	page := request(handler, "GET", "/"+query, "", nil).Body.String()
	if !strings.Contains(page, `id="bulk-accept-form"`) || !strings.Contains(page, `data-select-all`) || strings.Count(page, `form="bulk-accept-form"`) != 2 {
		t.Fatal("selection controls do not match eligible rows on the current page")
	}
	for _, value := range []string{"octocat/project#1", "octocat/project#22", "octocat/project#23", "octocat/project#24"} {
		if strings.Contains(page, `value="`+value+`"`) {
			t.Fatalf("unavailable alert is selectable: %s", value)
		}
	}
	values := url.Values{"alerts": {"OCTOCAT/Project#21", "octocat/project#25", "octocat/project#21"}}
	res := formRequest(handler, "/alerts/accept-selected"+query, values, nil)
	if res.Code != 303 || res.Header().Get("Location") != "/"+query {
		t.Fatalf("bulk acceptance lost the current filters, sorting, or page: %d %s", res.Code, res.Header().Get("Location"))
	}
	accepted, err := handler.store.ListAcceptedAlerts(context.Background())
	if err != nil || len(accepted) != 3 || accepted[storage.AlertKey("octocat/project", 21)].IsZero() || accepted[storage.AlertKey("octocat/project", 25)].IsZero() {
		t.Fatal("bulk acceptance did not persist exactly the selected alerts", err)
	}
	if len(handler.state.accepted) != 3 || fetches.Load() != 1 {
		t.Fatal("bulk acceptance changed the live state incorrectly or fetched GitHub")
	}
	page = request(handler, "GET", "/?state=accepted&sort=dependency&direction=asc", "", browserCookie()).Body.String()
	for _, pkg := range []string{"package-21", "package-23", "package-25"} {
		if !strings.Contains(page, pkg) {
			t.Fatalf("selected alert missing from shared Accepted view: %s", pkg)
		}
	}
	if strings.Contains(page, `id="bulk-accept-form"`) || strings.Contains(page, "package-20") {
		t.Fatal("Accepted view offers selection or includes an unselected alert")
	}
	res = formRequest(handler, "/alerts/accept-selected", values, nil)
	again, err := handler.store.ListAcceptedAlerts(context.Background())
	if res.Code != 303 || err != nil || !again[storage.AlertKey("octocat/project", 21)].Equal(accepted[storage.AlertKey("octocat/project", 21)]) {
		t.Fatal("repeated bulk submission changed an existing decision")
	}
	formRequest(handler, "/alerts/reopen", acceptanceForm("octocat/project", "21"), nil)
	again, err = handler.store.ListAcceptedAlerts(context.Background())
	if err != nil || len(again) != 2 || again[storage.AlertKey("octocat/project", 25)].IsZero() {
		t.Fatal("individual reopening changed another alert from the batch")
	}
}

func TestBulkAcceptanceRejectsInvalidSelectionsWithoutPartialChanges(t *testing.T) {
	for _, test := range []struct {
		name   string
		values url.Values
		status int
	}{
		{"empty", nil, 400},
		{"malformed", url.Values{"alerts": {"octocat/project#1", "invalid"}}, 400},
		{"missing repository", url.Values{"alerts": {"#1"}}, 400},
		{"invalid number", url.Values{"alerts": {"octocat/project#zero"}}, 400},
		{"zero number", url.Values{"alerts": {"octocat/project#0"}}, 400},
		{"missing alert", url.Values{"alerts": {"octocat/project#1", "missing/project#1"}}, 404},
		{"fixed alert", url.Values{"alerts": {"octocat/project#1", "octocat/fixed#1"}}, 409},
		{"dismissed alert", url.Values{"alerts": {"octocat/project#1", "octocat/dismissed#1"}}, 409},
		{"too many", url.Values{"alerts": strings.Split(strings.Repeat("octocat/project#1,", pageSize)+"octocat/project#1", ",")}, 400},
	} {
		t.Run(test.name, func(t *testing.T) {
			handler := newTestServer(t, &fakeGitHub{fetch: func(context.Context, []github.Connection) github.Result {
				return github.Result{Alerts: []github.Alert{
					pageAlert("open-package", "octocat/project", "high", "open"),
					pageAlert("fixed-package", "octocat/fixed", "high", "fixed"),
					pageAlert("dismissed-package", "octocat/dismissed", "high", "auto_dismissed"),
				}}
			}})
			refreshAndWait(t, handler)
			res := formRequest(handler, "/alerts/accept-selected", test.values, nil)
			if res.Code != test.status {
				t.Fatalf("unexpected validation result: %d %s", res.Code, res.Body.String())
			}
			accepted, err := handler.store.ListAcceptedAlerts(context.Background())
			if err != nil || len(accepted) != 0 || len(handler.state.accepted) != 0 {
				t.Fatal("invalid selection partially changed persisted or live decisions", err)
			}
		})
	}
}

func TestBulkAcceptanceFailedWritePreservesDecisionsAndSelection(t *testing.T) {
	handler := newTestServer(t, &fakeGitHub{fetch: func(context.Context, []github.Connection) github.Result {
		a := pageAlert("first-package", "octocat/project", "high", "open")
		b := pageAlert("second-package", "octocat/project", "high", "open")
		b.Number = 2
		return github.Result{Alerts: []github.Alert{a, b}}
	}})
	refreshAndWait(t, handler)
	if err := handler.store.Close(); err != nil {
		t.Fatal(err)
	}
	res := formRequest(handler, "/alerts/accept-selected", url.Values{"alerts": {"octocat/project#1", "octocat/project#2"}}, nil)
	if res.Code != 500 || !strings.Contains(res.Body.String(), "Could not save accepted alert selection") || len(handler.state.accepted) != 0 {
		t.Fatal("failed write was not reported or changed live acceptance")
	}
	if strings.Count(res.Body.String(), `checked`) != 2 {
		t.Fatal("failed write lost the user's row selection")
	}
}

func TestDemoAndClosedAlertsCannotBeSelected(t *testing.T) {
	handler := newTestServer(t, &fakeGitHub{fetch: func(context.Context, []github.Connection) github.Result {
		return github.Result{Alerts: []github.Alert{pageAlert("fixed-package", "octocat/project", "high", "fixed")}}
	}})
	refreshAndWait(t, handler)
	for _, path := range []string{"/demo", "/?state=fixed"} {
		page := request(handler, "GET", path, "", nil).Body.String()
		if strings.Contains(page, `id="bulk-accept-form"`) || strings.Contains(page, "data-select-all") || strings.Contains(page, `form="bulk-accept-form"`) {
			t.Fatalf("selection offered for unavailable alerts: %s", path)
		}
	}
}
