package server

import (
	"bytes"
	"encoding/csv"
	"html/template"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"dbaui/internal/github"

	"github.com/go-chi/chi/v5"
)

const pageSize = 20

type connectionView struct {
	ID, Label, Login        string
	Accounts, Organizations []string
}

type filters struct {
	Query, State, Severity, Owner, Ecosystem string
	Page                                     int
}

type alertView struct {
	github.Alert
	Link, Updated, UpdatedTitle string
}

type summaryView struct {
	Severity string
	Count    int
	Selected bool
	URL      string
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
	Matches                                    []github.Alert
	Rows                                       []alertView
	Total, MatchCount, Page, Pages, Start, End int
	FetchedAt                                  time.Time
	Form                                       formValues
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
	if !slices.Contains([]string{"open", "fixed", "dismissed", "all"}, state) {
		state = "open"
	}
	severity := q.Get("severity")
	if !slices.Contains([]string{"critical", "high", "medium", "low"}, severity) {
		severity = "all"
	}
	page, _ := strconv.Atoi(q.Get("page"))
	return filters{Query: q.Get("q"), State: state, Severity: severity, Owner: q.Get("owner"), Ecosystem: q.Get("ecosystem"), Page: max(1, page)}
}

func (p pageData) URL(key, value string) string {
	q := url.Values{}
	for k, v := range map[string]string{"q": p.Filters.Query, "state": p.Filters.State, "severity": p.Filters.Severity, "owner": p.Filters.Owner, "ecosystem": p.Filters.Ecosystem} {
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
	default:
		if key != "" {
			q.Set(key, value)
		}
	}
	return p.BasePath + "?" + q.Encode()
}

func (p pageData) PageURL(number int) string { return p.URL("page", strconv.Itoa(number)) }

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

func makePage(r *http.Request, connections []github.Connection, result github.Result, loaded, refreshing bool) pageData {
	p := pageData{
		Title: "Dependabot alerts", View: "alerts", BasePath: "/", Filters: readFilters(r),
		Loaded: loaded, Refreshing: refreshing, FetchedAt: result.FetchedAt,
		Errors: result.Errors, SourceLabels: make(map[string]string), Counts: make(map[string]int),
		Total: len(result.Alerts),
	}
	for _, conn := range connections {
		p.Connections = append(p.Connections, connectionView{conn.ID, conn.Label, conn.Login, conn.Accounts, conn.Organizations})
		p.SourceLabels[conn.ID] = conn.Label
	}
	owners, ecosystems := make(map[string]bool), make(map[string]bool)
	severityCounts := make(map[string]int)
	search := strings.ToLower(strings.TrimSpace(p.Filters.Query))
	for _, alert := range result.Alerts {
		state := alert.State
		if state == "auto_dismissed" {
			state = "dismissed"
		}
		p.Counts[state]++
		if alert.State == "open" {
			severityCounts[alert.SecurityAdvisory.Severity]++
		}
		owners[alert.Repository.Owner.Login] = true
		ecosystems[alert.Dependency.Package.Ecosystem] = true
		if p.Filters.State != "all" && p.Filters.State != state {
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
			view := alertView{Alert: alert, Link: githubLink(alert.HTMLURL), Updated: "Unknown"}
			if updated, err := time.Parse(time.RFC3339, alert.UpdatedAt); err == nil {
				view.Updated = updated.Format("02 Jan 2006")
				view.UpdatedTitle = updated.UTC().Format("02 Jan 2006 15:04 UTC")
			}
			p.Rows = append(p.Rows, view)
		}
	}
	return p
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

func (s *Server) sessionPage(w http.ResponseWriter, r *http.Request) (pageData, *session) {
	session := s.sessions.get(w, r)
	if session == nil {
		return pageData{}, nil
	}
	connections, result, loaded, refreshing := session.pageSnapshot()
	return makePage(r, connections, result, loaded, refreshing), session
}

func (s *Server) overviewPage(w http.ResponseWriter, r *http.Request) {
	p, session := s.sessionPage(w, r)
	if session != nil {
		s.render(w, http.StatusOK, "overview", p)
	}
}

func (s *Server) connectionsPage(w http.ResponseWriter, r *http.Request) {
	s.renderConnections(w, r, http.StatusOK, "", formValues{})
}

func (s *Server) renderConnections(w http.ResponseWriter, r *http.Request, status int, message string, values formValues) {
	p, session := s.sessionPage(w, r)
	if session == nil {
		return
	}
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
	session := s.sessions.get(w, r)
	if session == nil {
		return
	}
	split := func(value string) []string {
		return strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' || r == '\r' || r == '\t' })
	}
	values := formValues{r.PostForm.Get("label"), r.PostForm.Get("accounts"), r.PostForm.Get("organizations")}
	_, failure := s.createConnection(r.Context(), session, connectionInput{
		Label: values.Label, Token: r.PostForm.Get("token"), Accounts: split(values.Accounts), Organizations: split(values.Organizations),
	})
	if failure != nil {
		s.renderConnections(w, r, failure.status, failure.message, values)
		return
	}
	if _, failure = s.refreshSession(r.Context(), session); failure != nil {
		s.renderConnections(w, r, failure.status, "Connection added. "+failure.message, formValues{})
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) removeConnectionPage(w http.ResponseWriter, r *http.Request) {
	session := s.sessions.get(w, r)
	if session == nil {
		return
	}
	if !removeConnection(session, chi.URLParam(r, "id")) {
		s.renderConnections(w, r, http.StatusNotFound, "Connection not found.", formValues{})
		return
	}
	if _, failure := s.refreshSession(r.Context(), session); failure != nil {
		s.renderConnections(w, r, failure.status, "Connection removed. "+failure.message, formValues{})
		return
	}
	http.Redirect(w, r, "/connections", http.StatusSeeOther)
}

func (s *Server) refreshPage(w http.ResponseWriter, r *http.Request) {
	session := s.sessions.get(w, r)
	if session == nil {
		return
	}
	if _, failure := s.refreshSession(r.Context(), session); failure != nil {
		connections, result, loaded, refreshing := session.pageSnapshot()
		p := makePage(r, connections, result, loaded, refreshing)
		p.Error = failure.message
		s.render(w, failure.status, "overview", p)
		return
	}
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
	p, session := s.sessionPage(w, r)
	if session != nil {
		writeCSV(w, p)
	}
}

func (s *Server) exportDemoCSV(w http.ResponseWriter, r *http.Request) { writeCSV(w, demoPageData(r)) }

func writeCSV(w http.ResponseWriter, p pageData) {
	// Stage the complete file before writing headers, and escape spreadsheet formulas.
	var buffer bytes.Buffer
	writer := csv.NewWriter(&buffer)
	_ = writer.Write([]string{"Repository", "Package", "Ecosystem", "Severity", "State", "Summary", "Advisory", "Fixed version", "URL"})
	for _, alert := range p.Matches {
		advisory := alert.SecurityAdvisory.CVEID
		if advisory == "" {
			advisory = alert.SecurityAdvisory.GHSAID
		}
		patch := ""
		if alert.SecurityVulnerability.FirstPatchedVersion != nil {
			patch = alert.SecurityVulnerability.FirstPatchedVersion.Identifier
		}
		row := []string{alert.Repository.FullName, alert.Dependency.Package.Name, alert.Dependency.Package.Ecosystem, alert.SecurityAdvisory.Severity, alert.State, alert.SecurityAdvisory.Summary, advisory, patch, githubLink(alert.HTMLURL)}
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
	w.Header().Set("Content-Disposition", `attachment; filename="dbaui-alerts.csv"`)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = buffer.WriteTo(w)
}
