package server

import (
	"bytes"
	"cmp"
	"encoding/csv"
	"html/template"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/thelicato/ghal/internal/github"
	"github.com/thelicato/ghal/internal/storage"

	"github.com/go-chi/chi/v5"
)

const pageSize = 20

type connectionView struct {
	ID, Label, Login        string
	Accounts, Organizations []string
	Error                   string
}

type filters struct {
	Query, State, Severity, Owner, Ecosystem string
	Sort, Direction                          string
	Page                                     int
}

type alertView struct {
	github.Alert
	Link, Updated, UpdatedTitle string
	AcceptedAt                  time.Time
	Selected                    bool
}

func (a alertView) Accepted() bool { return !a.AcceptedAt.IsZero() }

func (a alertView) CanAccept() bool { return a.State == "open" && !a.Accepted() }

func (a alertView) SelectionValue() string {
	return a.Repository.FullName + "#" + strconv.Itoa(a.Number)
}

type summaryView struct {
	Severity string
	Count    int
	Selected bool
	URL      string
}

type sortHeader struct {
	Label, URL, Order, Indicator string
}

func (p pageData) SortHeaders() []sortHeader {
	var headers []sortHeader
	for _, column := range []struct{ key, label string }{
		{"dependency", "Dependency / vulnerability"}, {"repository", "Repository"},
		{"severity", "Severity"}, {"state", "State"}, {"updated", "Updated"},
	} {
		order := p.SortOrder(column.key)
		indicator := "↕"
		if order == "ascending" {
			indicator = "↑"
		} else if order == "descending" {
			indicator = "↓"
		}
		headers = append(headers, sortHeader{column.label, p.URL("sort", column.key), order, indicator})
	}
	return headers
}

type formValues struct {
	Label, Accounts, Organizations string
}

type pageData struct {
	Title, View, BasePath, Error               string
	Demo, Loaded, Refreshing                   bool
	Connections                                []connectionView
	Errors                                     []github.SourceError
	SourceLabels                               map[string]string
	Filters                                    filters
	Owners, Ecosystems                         []string
	Summaries                                  []summaryView
	Counts                                     map[string]int
	Accepted                                   map[storage.AlertID]time.Time
	Matches                                    []github.Alert
	Rows                                       []alertView
	Total, MatchCount, Page, Pages, Start, End int
	FetchedAt                                  time.Time
	Form                                       formValues
	Version                                    uint64
}

func (p pageData) SelectableCount() int {
	if p.Demo {
		return 0
	}
	count := 0
	for _, row := range p.Rows {
		if row.CanAccept() {
			count++
		}
	}
	return count
}

func templateFunctions() template.FuncMap {
	return template.FuncMap{
		"label": func(value string) string {
			if value == "auto_dismissed" {
				return "Auto-dismissed"
			}
			if value == "" {
				return "Unknown"
			}
			return strings.ToUpper(value[:1]) + value[1:]
		},
		"add": func(a, b int) int { return a + b },
	}
}

func readFilters(r *http.Request) filters {
	q := r.URL.Query()
	state := q.Get("state")
	if !slices.Contains([]string{"open", "accepted", "fixed", "dismissed", "all"}, state) {
		state = "open"
	}
	severity := q.Get("severity")
	if !slices.Contains([]string{"critical", "high", "medium", "low"}, severity) {
		severity = "all"
	}
	page, _ := strconv.Atoi(q.Get("page"))
	column := q.Get("sort")
	if !slices.Contains([]string{"dependency", "repository", "severity", "state", "updated"}, column) {
		column = "severity"
	}
	direction := q.Get("direction")
	if direction != "asc" && direction != "desc" {
		direction = defaultDirection(column)
	}
	return filters{Query: q.Get("q"), State: state, Severity: severity, Owner: q.Get("owner"), Ecosystem: q.Get("ecosystem"), Sort: column, Direction: direction, Page: max(1, page)}
}

func defaultDirection(column string) string {
	if column == "severity" || column == "updated" {
		return "desc"
	}
	return "asc"
}

func (p pageData) URL(key, value string) string {
	q := url.Values{}
	for k, v := range map[string]string{"q": p.Filters.Query, "state": p.Filters.State, "severity": p.Filters.Severity, "owner": p.Filters.Owner, "ecosystem": p.Filters.Ecosystem, "sort": p.Filters.Sort, "direction": p.Filters.Direction} {
		if v != "" && v != "all" {
			q.Set(k, v)
		}
	}
	// "all" is significant for states, whereas other filters omit it.
	q.Set("state", p.Filters.State)
	switch key {
	case "page":
		q.Set(key, value)
	case "stat":
		q.Set("state", "open")
		if p.Filters.Severity == value {
			q.Del("severity")
		} else {
			q.Set("severity", value)
		}
	case "sort":
		q.Set("sort", value)
		direction := defaultDirection(value)
		if p.Filters.Sort == value && p.Filters.Direction == "asc" {
			direction = "desc"
		} else if p.Filters.Sort == value {
			direction = "asc"
		}
		q.Set("direction", direction)
	default:
		if key != "" {
			q.Set(key, value)
		}
	}
	return p.BasePath + "?" + q.Encode()
}

func (p pageData) PageURL(number int) string { return p.URL("page", strconv.Itoa(number)) }

func (p pageData) AlertActionURL(action string) string {
	u, _ := url.Parse(p.PageURL(p.Page))
	u.Path = "/alerts/" + action
	return u.String()
}

func (p pageData) SortOrder(column string) string {
	if p.Filters.Sort != column {
		return "none"
	}
	if p.Filters.Direction == "desc" {
		return "descending"
	}
	return "ascending"
}

func (p pageData) RefreshURL() string {
	u, _ := url.Parse(p.URL("", ""))
	return "/refresh?" + u.RawQuery
}

func (p pageData) ExportURL() string {
	u, _ := url.Parse(p.URL("page", "1"))
	u.Path = p.BasePath
	if u.Path == "/" {
		u.Path = ""
	}
	u.Path += "/alerts.csv"
	return u.String()
}

func githubLink(value string) string {
	u, err := url.Parse(value)
	if err != nil || u.Scheme != "https" || u.Host != "github.com" || u.User != nil {
		return ""
	}
	return u.String()
}

func makePage(r *http.Request, connections []github.Connection, result github.Result, loaded, refreshing bool, accepted map[storage.AlertID]time.Time) pageData {
	p := pageData{
		Title: "Dependabot alerts", View: "alerts", BasePath: "/", Filters: readFilters(r),
		Loaded: loaded, Refreshing: refreshing, FetchedAt: result.FetchedAt,
		Errors: result.Errors, SourceLabels: make(map[string]string), Counts: make(map[string]int),
		Total: len(result.Alerts), Accepted: accepted,
	}
	for _, conn := range connections {
		view := connectionView{ID: conn.ID, Label: conn.Label, Login: conn.Login, Accounts: conn.Accounts, Organizations: conn.Organizations}
		if view.Label == "" {
			view.Label = "GitHub connection"
		}
		for _, issue := range result.Errors {
			if issue.ConnectionID == conn.ID && issue.Source == authenticationSource {
				view.Error = issue.Message
				break
			}
		}
		p.Connections = append(p.Connections, view)
		p.SourceLabels[conn.ID] = view.Label
	}
	owners, ecosystems := make(map[string]bool), make(map[string]bool)
	severityCounts := make(map[string]int)
	search := strings.ToLower(strings.TrimSpace(p.Filters.Query))
	for _, alert := range result.Alerts {
		_, isAccepted := accepted[storage.AlertKey(alert.Repository.FullName, alert.Number)]
		state := alert.State
		if state == "auto_dismissed" {
			state = "dismissed"
		}
		if isAccepted {
			p.Counts["accepted"]++
		}
		if state != "open" || !isAccepted {
			p.Counts[state]++
		}
		if alert.State == "open" && !isAccepted {
			severityCounts[alert.SecurityAdvisory.Severity]++
		}
		owners[alert.Repository.Owner.Login] = true
		ecosystems[alert.Dependency.Package.Ecosystem] = true
		if p.Filters.State == "accepted" && !isAccepted {
			continue
		}
		if p.Filters.State == "open" && isAccepted {
			continue
		}
		if p.Filters.State != "all" && p.Filters.State != "accepted" && p.Filters.State != state {
			continue
		}
		if p.Filters.Severity != "all" && p.Filters.Severity != alert.SecurityAdvisory.Severity {
			continue
		}
		if p.Filters.Owner != "" && p.Filters.Owner != alert.Repository.Owner.Login {
			continue
		}
		if p.Filters.Ecosystem != "" && p.Filters.Ecosystem != alert.Dependency.Package.Ecosystem {
			continue
		}
		haystack := strings.Join([]string{alert.Repository.FullName, alert.Dependency.Package.Name, alert.SecurityAdvisory.Summary, alert.SecurityAdvisory.CVEID, alert.SecurityAdvisory.GHSAID}, " ")
		if search != "" && !strings.Contains(strings.ToLower(haystack), search) {
			continue
		}
		p.Matches = append(p.Matches, alert)
	}
	for owner := range owners {
		p.Owners = append(p.Owners, owner)
	}
	for ecosystem := range ecosystems {
		p.Ecosystems = append(p.Ecosystems, ecosystem)
	}
	sort.Strings(p.Owners)
	sort.Strings(p.Ecosystems)
	sortAlerts(p.Matches, p.Filters, accepted)
	for _, severity := range []string{"critical", "high", "medium", "low"} {
		p.Summaries = append(p.Summaries, summaryView{severity, severityCounts[severity], p.Filters.Severity == severity, p.URL("stat", severity)})
	}
	p.MatchCount = len(p.Matches)
	p.Pages = max(1, (p.MatchCount+pageSize-1)/pageSize)
	p.Page = min(p.Filters.Page, p.Pages)
	p.End = min(p.Page*pageSize, p.MatchCount)
	if p.MatchCount > 0 {
		p.Start = (p.Page-1)*pageSize + 1
		for _, alert := range p.Matches[p.Start-1 : p.End] {
			view := alertView{Alert: alert, Link: githubLink(alert.HTMLURL), Updated: "Unknown", AcceptedAt: accepted[storage.AlertKey(alert.Repository.FullName, alert.Number)]}
			if updated, err := time.Parse(time.RFC3339, alert.UpdatedAt); err == nil {
				view.Updated = updated.Format("02 Jan 2006")
				view.UpdatedTitle = updated.UTC().Format("02 Jan 2006 15:04 UTC")
			}
			p.Rows = append(p.Rows, view)
		}
	}
	return p
}

func sortAlerts(alerts []github.Alert, f filters, accepted map[storage.AlertID]time.Time) {
	severity := map[string]int{"critical": 4, "high": 3, "medium": 2, "low": 1}
	textCompare := func(a, b string) int { return strings.Compare(strings.ToLower(a), strings.ToLower(b)) }
	updated := func(value string) time.Time {
		t, _ := time.Parse(time.RFC3339, value)
		return t
	}
	state := func(a github.Alert) string {
		if _, ok := accepted[storage.AlertKey(a.Repository.FullName, a.Number)]; ok {
			return "accepted"
		}
		if a.State == "auto_dismissed" {
			return "auto-dismissed"
		}
		return a.State
	}
	slices.SortStableFunc(alerts, func(a, b github.Alert) int {
		var order int
		switch f.Sort {
		case "dependency":
			order = textCompare(a.Dependency.Package.Name, b.Dependency.Package.Name)
		case "repository":
			order = textCompare(a.Repository.FullName, b.Repository.FullName)
		case "severity":
			order = cmp.Compare(severity[a.SecurityAdvisory.Severity], severity[b.SecurityAdvisory.Severity])
		case "state":
			order = textCompare(state(a), state(b))
		case "updated":
			order = updated(a.UpdatedAt).Compare(updated(b.UpdatedAt))
		}
		if f.Direction == "desc" {
			order = -order
		}
		if order != 0 {
			return order
		}
		// Stable ties keep alerts in the same position across refreshes and pages.
		if f.Sort == "severity" {
			if order = updated(b.UpdatedAt).Compare(updated(a.UpdatedAt)); order != 0 {
				return order
			}
		}
		if order = textCompare(a.Repository.FullName, b.Repository.FullName); order != 0 {
			return order
		}
		return cmp.Compare(a.Number, b.Number)
	})
}

func (s *Server) render(w http.ResponseWriter, status int, name string, data pageData) {
	var buffer bytes.Buffer
	if err := s.templates.ExecuteTemplate(&buffer, name, data); err != nil {
		http.Error(w, "Could not render the page.", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = buffer.WriteTo(w)
}

func (s *Server) currentPage(r *http.Request) pageData {
	connections, result, loaded, refreshing, accepted, version := s.state.pageSnapshot()
	p := makePage(r, connections, result, loaded, refreshing, accepted)
	p.Version = version
	return p
}

func (s *Server) overviewPage(w http.ResponseWriter, r *http.Request) {
	s.render(w, http.StatusOK, "overview", s.currentPage(r))
}

func (s *Server) connectionsPage(w http.ResponseWriter, r *http.Request) {
	s.renderConnections(w, r, http.StatusOK, "", formValues{})
}

func (s *Server) renderConnections(w http.ResponseWriter, r *http.Request, status int, message string, values formValues) {
	p := s.currentPage(r)
	p.Title, p.View, p.Error, p.Form = "Connections", "connections", message, values
	s.render(w, status, "connections", p)
}

func (s *Server) createConnectionPage(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	defer r.Body.Close()
	if err := r.ParseForm(); err != nil {
		s.renderConnections(w, r, http.StatusBadRequest, "Enter a valid connection.", formValues{})
		return
	}
	split := func(value string) []string {
		return strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' || r == '\r' || r == '\t' })
	}
	values := formValues{r.PostForm.Get("label"), r.PostForm.Get("accounts"), r.PostForm.Get("organizations")}
	_, failure := s.createConnection(r.Context(), connectionInput{
		Label: values.Label, Token: r.PostForm.Get("token"), Accounts: split(values.Accounts), Organizations: split(values.Organizations),
	})
	if failure != nil {
		s.renderConnections(w, r, failure.status, failure.message, values)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) removeConnectionPage(w http.ResponseWriter, r *http.Request) {
	if failure := s.removeConnection(r.Context(), chi.URLParam(r, "id")); failure != nil {
		s.renderConnections(w, r, failure.status, failure.message, formValues{})
		return
	}
	http.Redirect(w, r, "/connections", http.StatusSeeOther)
}

func (s *Server) refreshPage(w http.ResponseWriter, r *http.Request) {
	s.requestRefresh()
	// Filters are query parameters on the form action, never a redirect target.
	target := "/"
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

func (s *Server) demoPage(w http.ResponseWriter, r *http.Request) {
	s.render(w, http.StatusOK, "overview", demoPageData(r))
}

func (s *Server) exportCSV(w http.ResponseWriter, r *http.Request) {
	writeCSV(w, s.currentPage(r))
}

func (s *Server) exportDemoCSV(w http.ResponseWriter, r *http.Request) { writeCSV(w, demoPageData(r)) }

func writeCSV(w http.ResponseWriter, p pageData) {
	// Stage the complete file before writing headers, and escape spreadsheet formulas.
	var buffer bytes.Buffer
	writer := csv.NewWriter(&buffer)
	_ = writer.Write([]string{"Repository", "Package", "Ecosystem", "Severity", "State", "Summary", "Advisory", "Fixed version", "URL", "Risk accepted", "Accepted at"})
	for _, alert := range p.Matches {
		advisory := alert.SecurityAdvisory.CVEID
		if advisory == "" {
			advisory = alert.SecurityAdvisory.GHSAID
		}
		patch := ""
		if alert.SecurityVulnerability.FirstPatchedVersion != nil {
			patch = alert.SecurityVulnerability.FirstPatchedVersion.Identifier
		}
		accepted, at := "No", ""
		if timestamp, ok := p.Accepted[storage.AlertKey(alert.Repository.FullName, alert.Number)]; ok {
			accepted, at = "Yes", timestamp.UTC().Format(time.RFC3339)
		}
		row := []string{alert.Repository.FullName, alert.Dependency.Package.Name, alert.Dependency.Package.Ecosystem, alert.SecurityAdvisory.Severity, alert.State, alert.SecurityAdvisory.Summary, advisory, patch, githubLink(alert.HTMLURL), accepted, at}
		for i, value := range row {
			trimmed := strings.TrimSpace(value)
			if strings.HasPrefix(value, "\t") || strings.HasPrefix(value, "\r") || strings.HasPrefix(value, "\n") || (trimmed != "" && strings.ContainsRune("=+-@", rune(trimmed[0]))) {
				row[i] = "'" + value
			}
		}
		_ = writer.Write(row)
	}
	writer.Flush()
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="ghal-alerts.csv"`)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = buffer.WriteTo(w)
}
