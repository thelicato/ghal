package server

import (
	"encoding/csv"
	"fmt"
	"net/http/httptest"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/thelicato/ghal/internal/github"
	"github.com/thelicato/ghal/internal/storage"
)

func TestSortColumnsInBothDirections(t *testing.T) {
	a := pageAlert("zebra", "alpha/repo", "critical", "fixed")
	b := pageAlert("Alpha", "Zeta/repo", "medium", "open")
	c := pageAlert("middle", "middle/repo", "high", "dismissed")
	a.Number, b.Number, c.Number = 1, 2, 3
	a.ConnectionIDs, b.ConnectionIDs, c.ConnectionIDs = []string{"first"}, []string{"second"}, []string{"third"}
	connections := []github.Connection{{ID: "first", Label: "Zeta"}, {ID: "second", Label: "middle"}, {ID: "third", Label: "Alpha"}}
	a.UpdatedAt, b.UpdatedAt, c.UpdatedAt = "2026-10-22T12:00:00Z", "2026-10-22T11:30:00-02:00", "2026-10-21T10:00:00Z"
	for _, test := range []struct {
		column string
		want   []int
	}{
		{"dependency", []int{2, 3, 1}}, {"repository", []int{1, 3, 2}},
		{"connection", []int{3, 2, 1}},
		{"severity", []int{2, 3, 1}}, {"state", []int{3, 1, 2}}, {"updated", []int{3, 1, 2}},
	} {
		for _, direction := range []string{"asc", "desc"} {
			t.Run(test.column+"/"+direction, func(t *testing.T) {
				result := github.Result{Alerts: []github.Alert{a, b, c}}
				r := httptest.NewRequest("GET", "/?state=all&sort="+test.column+"&direction="+direction, nil)
				p := makePage(r, connections, result, true, false, nil)
				var got []int
				for _, row := range p.Rows {
					got = append(got, row.Number)
				}
				want := slices.Clone(test.want)
				if direction == "desc" {
					slices.Reverse(want)
				}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("got %v, want %v", got, want)
				}
				if result.Alerts[0].Number != 1 {
					t.Fatal("sorting modified the shared GitHub cache")
				}
			})
		}
	}
	accepted := map[storage.AlertID]time.Time{storage.AlertKey(b.Repository.FullName, b.Number): time.Now()}
	r := httptest.NewRequest("GET", "/?state=all&sort=state&direction=asc", nil)
	p := makePage(r, nil, github.Result{Alerts: []github.Alert{a, b, c}}, true, false, accepted)
	if p.Rows[0].Number != 2 || !p.Rows[0].Accepted() {
		t.Fatal("state sorting ignored the displayed acceptance status")
	}
}

func TestDefaultSeveritySortingAndStableTies(t *testing.T) {
	a := pageAlert("a", "acme/repo", "high", "open")
	b, c := a, a
	a.Number, b.Number, c.Number = 3, 2, 1
	c.UpdatedAt = "2026-10-23T10:00:00Z"
	low := pageAlert("low", "acme/repo", "low", "open")
	r := httptest.NewRequest("GET", "/?sort=invalid&direction=invalid", nil)
	p := makePage(r, nil, github.Result{Alerts: []github.Alert{low, a, b, c}}, true, false, nil)
	if p.Filters.Sort != "severity" || p.Filters.Direction != "desc" || p.Rows[0].Number != 1 || p.Rows[1].Number != 2 || p.Rows[2].Number != 3 || p.Rows[3].Dependency.Package.Name != "low" {
		t.Fatal("default sorting or deterministic ties failed")
	}
}

func TestSortingPrecedesPaginationAndSurvivesNavigation(t *testing.T) {
	var result github.Result
	for i := 44; i >= 0; i-- {
		a := pageAlert(fmt.Sprintf("package-%02d", i), "octocat/project", "high", "open")
		a.Number = i + 1
		result.Alerts = append(result.Alerts, a)
	}
	r := httptest.NewRequest("GET", "/?state=open&q=package&severity=high&owner=octocat&ecosystem=npm&sort=dependency&direction=asc&page=2", nil)
	p := makePage(r, nil, result, true, false, nil)
	if p.Rows[0].Dependency.Package.Name != "package-20" || p.Rows[19].Dependency.Package.Name != "package-39" {
		t.Fatal("sorting was applied only to the current page")
	}
	res := httptest.NewRecorder()
	writeCSV(res, p)
	records, err := csv.NewReader(strings.NewReader(res.Body.String())).ReadAll()
	if err != nil || len(records) != 46 || records[1][1] != "package-00" || records[45][1] != "package-44" {
		t.Fatal("CSV did not export every match in the selected order")
	}
	for _, link := range []string{p.PageURL(3), p.RefreshURL(), p.ExportURL(), p.URL("state", "all"), p.URL("stat", "critical"), p.AlertActionURL("accept")} {
		u, err := url.Parse(link)
		if err != nil {
			t.Fatal(err)
		}
		q := u.Query()
		if q.Get("sort") != "dependency" || q.Get("direction") != "asc" || q.Get("q") != "package" || q.Get("owner") != "octocat" || q.Get("ecosystem") != "npm" {
			t.Fatalf("navigation lost filters or sorting: %s", link)
		}
	}
	for _, test := range []struct{ column, direction string }{{"dependency", "desc"}, {"updated", "desc"}, {"repository", "asc"}} {
		u, _ := url.Parse(p.URL("sort", test.column))
		if u.Query().Get("direction") != test.direction || u.Query().Has("page") {
			t.Fatalf("sort toggling did not reset the page or choose the expected direction: %s", u)
		}
	}
}
