package server

import (
	"encoding/csv"
	"fmt"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/thelicato/ghal/internal/github"
)

func TestConnectionColumnShowsAllSourcesAndEscapesNames(t *testing.T) {
	const work = `Work <script>alert(1)</script>`
	const formula = `=HYPERLINK("https://example.com")`
	connections := []github.Connection{
		{ID: "work", Label: work, Token: "never-render-this-pat"},
		{ID: "personal", Label: "Personal", Token: "another-secret-pat"},
		{ID: "formula", Label: formula},
	}
	shared := pageAlert("shared-package", "octocat/shared", "high", "open")
	shared.ConnectionIDs = []string{"work", "personal", "work"}
	single := pageAlert("single-package", "octocat/single", "high", "open")
	single.ConnectionIDs = []string{"work"}
	unknown := pageAlert("unknown-package", "octocat/unknown", "high", "open")
	formulaAlert := pageAlert("formula-package", "octocat/formula", "high", "open")
	formulaAlert.ConnectionIDs = []string{"formula"}
	r := httptest.NewRequest("GET", "/?sort=connection&direction=asc", nil)
	p := makePage(r, connections, github.Result{Alerts: []github.Alert{shared, single, unknown, formulaAlert}}, true, false, nil)
	handler := newTestServer(t, &fakeGitHub{})
	res := httptest.NewRecorder()
	handler.render(res, 200, "overview", p)
	body := res.Body.String()
	if res.Code != 200 || !strings.Contains(body, `aria-label="Sort by Connection"`) || !strings.Contains(body, `<div class="alert-connections"><span>Personal</span><span>Work &lt;script&gt;alert(1)&lt;/script&gt;</span></div>`) {
		t.Fatal("connection column is missing, fails to show all sources, or fails to escape names")
	}
	if strings.Contains(body, "never-render-this-pat") || strings.Contains(body, "another-secret-pat") || strings.Contains(body, "<script>alert(1)</script>") {
		t.Fatal("connection column exposed credentials or unsafe HTML")
	}
	if !reflect.DeepEqual(shared.ConnectionIDs, []string{"work", "personal", "work"}) {
		t.Fatal("displaying connection names mutated the cached alert")
	}
	res = httptest.NewRecorder()
	writeCSV(res, p)
	records, err := csv.NewReader(strings.NewReader(res.Body.String())).ReadAll()
	if err != nil || len(records) != 5 || records[0][len(records[0])-1] != "Connection" {
		t.Fatal("CSV connection column is missing", err)
	}
	got := make(map[string]string)
	for _, row := range records[1:] {
		got[row[1]] = row[len(row)-1]
	}
	want := map[string]string{
		"shared-package": "Personal; " + work, "single-package": work,
		"unknown-package": "Unknown connection", "formula-package": "'" + formula,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("CSV lost source names or formula protection: got %v", got)
	}
}

func TestConnectionSortingAcrossPagesAndNavigation(t *testing.T) {
	connections := []github.Connection{{ID: "a", Label: "Alpha"}, {ID: "b", Label: "beta"}, {ID: "c", Label: "Zeta"}}
	var result github.Result
	for i := 1; i <= 45; i++ {
		a := pageAlert(fmt.Sprintf("package-%02d", i), "octocat/project", "high", "open")
		a.Number = i
		a.ConnectionIDs = []string{connections[(i-1)%3].ID}
		result.Alerts = append(result.Alerts, a)
	}
	for _, direction := range []string{"asc", "desc"} {
		t.Run(direction, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/?state=open&q=package&severity=high&owner=octocat&ecosystem=npm&sort=connection&direction="+direction+"&page=2", nil)
			p := makePage(r, connections, result, true, false, nil)
			last := "Zeta"
			if direction == "desc" {
				last = "Alpha"
			}
			if p.Page != 2 || len(p.Rows) != 20 || p.Rows[0].ConnectionLabels[0] != "beta" || p.Rows[19].ConnectionLabels[0] != last {
				t.Fatal("connection sorting was applied only to the current page")
			}
			res := httptest.NewRecorder()
			writeCSV(res, p)
			records, err := csv.NewReader(strings.NewReader(res.Body.String())).ReadAll()
			if err != nil || len(records) != 46 || records[45][11] != last {
				t.Fatal("CSV did not retain connection sorting across all matching alerts", err)
			}
			for _, link := range []string{p.PageURL(1), p.PageURL(p.Pages), p.RefreshURL(), p.ExportURL(), p.URL("state", "accepted"), p.URL("stat", "critical"), p.AlertActionURL("accept"), p.AlertActionURL("accept-selected")} {
				u, err := url.Parse(link)
				if err != nil {
					t.Fatal(err)
				}
				q := u.Query()
				if q.Get("sort") != "connection" || q.Get("direction") != direction || q.Get("q") != "package" || q.Get("owner") != "octocat" || q.Get("ecosystem") != "npm" {
					t.Fatalf("navigation lost connection sorting or filters: %s", link)
				}
			}
		})
	}
}

func TestSharedConnectionSortingIsIndependentOfFetchOrder(t *testing.T) {
	connections := []github.Connection{{ID: "a", Label: "Alpha"}, {ID: "b", Label: "beta"}, {ID: "c", Label: "Zeta"}}
	a := pageAlert("later-name", "octocat/first", "high", "open")
	a.ConnectionIDs = []string{"a", "c"}
	b := pageAlert("earlier-name", "octocat/second", "high", "open")
	b.ConnectionIDs = []string{"b", "a"}
	r := httptest.NewRequest("GET", "/?sort=connection&direction=asc", nil)
	p := makePage(r, connections, github.Result{Alerts: []github.Alert{a, b}}, true, false, nil)
	if p.Rows[0].Dependency.Package.Name != "earlier-name" || !reflect.DeepEqual(p.Rows[0].ConnectionLabels, []string{"Alpha", "beta"}) {
		t.Fatal("shared connections were sorted by fetch order instead of their names")
	}
}
