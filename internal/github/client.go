// Package github reads Dependabot alerts without an SDK.
package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Connection struct {
	ID            string   `json:"id"`
	Label         string   `json:"label"`
	Login         string   `json:"login"`
	Accounts      []string `json:"accounts"`
	Organizations []string `json:"organizations"`
	Token         string   `json:"-"`
}

type Alert struct {
	Number     int        `json:"number"`
	State      string     `json:"state"`
	HTMLURL    string     `json:"html_url"`
	CreatedAt  string     `json:"created_at"`
	UpdatedAt  string     `json:"updated_at"`
	Repository Repository `json:"repository"`
	Dependency struct {
		Package struct {
			Ecosystem string `json:"ecosystem"`
			Name      string `json:"name"`
		} `json:"package"`
		ManifestPath string `json:"manifest_path"`
		Scope        string `json:"scope"`
	} `json:"dependency"`
	SecurityAdvisory struct {
		GHSAID   string `json:"ghsa_id"`
		CVEID    string `json:"cve_id"`
		Summary  string `json:"summary"`
		Severity string `json:"severity"`
	} `json:"security_advisory"`
	SecurityVulnerability struct {
		VulnerableVersionRange string `json:"vulnerable_version_range"`
		FirstPatchedVersion    *struct {
			Identifier string `json:"identifier"`
		} `json:"first_patched_version"`
	} `json:"security_vulnerability"`
	ConnectionIDs []string `json:"connection_ids"`
}

type Repository struct {
	FullName string `json:"full_name"`
	Owner    struct {
		Login string `json:"login"`
	} `json:"owner"`
}

type SourceError struct {
	ConnectionID string `json:"connection_id"`
	Source       string `json:"source"`
	Message      string `json:"message"`
}

type Result struct {
	Alerts    []Alert       `json:"alerts"`
	Errors    []SourceError `json:"errors"`
	FetchedAt time.Time     `json:"fetched_at"`
}

type Client struct {
	http    *http.Client
	baseURL string
}

func NewClient() *Client {
	return &Client{
		http: &http.Client{
			Timeout: 30 * time.Second,
			// Never forward a PAT through an upstream redirect.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		baseURL: "https://api.github.com",
	}
}

// APIError deliberately excludes GitHub response bodies and request headers.
type APIError struct {
	Status      int
	RateLimited bool
}

func (e *APIError) Error() string {
	if e.RateLimited {
		return "GitHub rate limit reached. Wait before refreshing."
	}
	switch e.Status {
	case http.StatusUnauthorized:
		return "GitHub rejected this token. Check whether it has expired or been revoked."
	case http.StatusForbidden:
		return "GitHub denied access. Check Dependabot permissions, organization role, and SSO authorization."
	case http.StatusNotFound:
		return "GitHub could not find this source, or the token cannot read its Dependabot alerts."
	default:
		return fmt.Sprintf("GitHub returned HTTP %d. Try refreshing later.", e.Status)
	}
}

func (c *Client) get(ctx context.Context, token, path string, dst any) (string, error) {
	u, err := url.Parse(c.baseURL + path)
	if err != nil {
		return "", errors.New("invalid GitHub API path")
	}
	return c.getURL(ctx, token, u, dst)
}

func (c *Client) getURL(ctx context.Context, token string, u *url.URL, dst any) (string, error) {
	base, _ := url.Parse(c.baseURL)
	if u.Scheme != base.Scheme || u.Host != base.Host || u.User != nil {
		return "", errors.New("GitHub returned an unsafe pagination URL")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", errors.New("could not create GitHub request")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2026-03-10")
	req.Header.Set("User-Agent", "dbaui")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", errors.New("could not reach GitHub, or the request timed out")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", &APIError{Status: resp.StatusCode, RateLimited: resp.StatusCode == 429 || (resp.StatusCode == 403 && (resp.Header.Get("X-RateLimit-Remaining") == "0" || resp.Header.Get("Retry-After") != ""))}
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(dst); err != nil {
		return "", errors.New("GitHub returned an unreadable response")
	}
	for _, link := range strings.Split(resp.Header.Get("Link"), ",") {
		parts := strings.Split(strings.TrimSpace(link), ";")
		if len(parts) < 2 {
			continue
		}
		for _, rel := range parts[1:] {
			if strings.TrimSpace(rel) == `rel="next"` {
				return strings.Trim(strings.TrimSpace(parts[0]), "<>"), nil
			}
		}
	}
	return "", nil
}

// collect follows Link URLs, including GitHub's cursor-based alert pagination.
// Successfully fetched pages remain available if a later page fails.
func collect[T any](ctx context.Context, c *Client, token, path string) ([]T, error) {
	items := make([]T, 0)
	next := c.baseURL + path
	visited := make(map[string]bool)
	for next != "" {
		if visited[next] {
			return items, errors.New("GitHub returned a repeated pagination URL")
		}
		visited[next] = true
		u, err := url.Parse(next)
		if err != nil {
			return items, errors.New("GitHub returned an invalid pagination URL")
		}
		var page []T
		next, err = c.getURL(ctx, token, u, &page)
		if err != nil {
			return items, err
		}
		items = append(items, page...)
	}
	return items, nil
}

func (c *Client) Login(ctx context.Context, token string) (string, error) {
	var user struct {
		Login string `json:"login"`
	}
	_, err := c.get(ctx, token, "/user", &user)
	if err == nil && user.Login == "" {
		err = errors.New("GitHub did not return an account for this token")
	}
	return user.Login, err
}

func (c *Client) Fetch(ctx context.Context, connections []Connection) Result {
	result := Result{Alerts: make([]Alert, 0), Errors: make([]SourceError, 0)}
	var mu sync.Mutex
	// Keep alert fetches bounded while the producer discovers account repositories.
	jobs := make(chan func())
	var workers sync.WaitGroup
	for range 4 {
		workers.Go(func() {
			for job := range jobs {
				job()
			}
		})
	}
	addError := func(conn Connection, source string, err error) {
		if err == nil {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		result.Errors = append(result.Errors, SourceError{ConnectionID: conn.ID, Source: source, Message: err.Error()})
	}
	addAlerts := func(conn Connection, alerts []Alert, repo *Repository) {
		mu.Lock()
		defer mu.Unlock()
		for _, alert := range alerts {
			if repo != nil {
				alert.Repository = *repo
			}
			alert.ConnectionIDs = []string{conn.ID}
			result.Alerts = append(result.Alerts, alert)
		}
	}
	// Discovery happens in the producer so workers never block on their own queue.
	for _, conn := range connections {
		for _, org := range conn.Organizations {
			jobs <- func() {
				alerts, err := collect[Alert](ctx, c, conn.Token, "/orgs/"+url.PathEscape(org)+"/dependabot/alerts?per_page=100&state=open,fixed,dismissed,auto_dismissed")
				addAlerts(conn, alerts, nil)
				addError(conn, org, err)
			}
		}
		if len(conn.Accounts) == 0 {
			continue
		}
		repos, err := collect[Repository](ctx, c, conn.Token, "/user/repos?affiliation=owner,collaborator&per_page=100&sort=full_name")
		addError(conn, strings.Join(conn.Accounts, ", ")+" repositories", err)
		owners := make(map[string]bool)
		for _, owner := range conn.Accounts {
			owners[strings.ToLower(owner)] = true
		}
		found := make(map[string]bool)
		for _, repo := range repos {
			if !owners[strings.ToLower(repo.Owner.Login)] {
				continue
			}
			found[strings.ToLower(repo.Owner.Login)] = true
			jobs <- func() {
				alerts, err := collect[Alert](ctx, c, conn.Token, "/repos/"+repo.FullName+"/dependabot/alerts?per_page=100&state=open,fixed,dismissed,auto_dismissed")
				addAlerts(conn, alerts, &repo)
				addError(conn, repo.FullName, err)
			}
		}
		if err == nil {
			for _, owner := range conn.Accounts {
				if !found[strings.ToLower(owner)] {
					addError(conn, owner, errors.New("no accessible repositories found for this account; check the account name and token repository access"))
				}
			}
		}
	}
	close(jobs)
	workers.Wait()
	// Multiple PATs may refer to the same repository and alert.
	unique := make(map[string]int)
	alerts := make([]Alert, 0, len(result.Alerts))
	for _, alert := range result.Alerts {
		key := strings.ToLower(alert.Repository.FullName) + "#" + strconv.Itoa(alert.Number)
		if i, ok := unique[key]; ok {
			prior := alerts[i]
			ids := prior.ConnectionIDs
			if !contains(ids, alert.ConnectionIDs[0]) {
				ids = append(ids, alert.ConnectionIDs[0])
			}
			if alert.UpdatedAt > prior.UpdatedAt {
				alerts[i] = alert
			}
			alerts[i].ConnectionIDs = ids
		} else {
			unique[key] = len(alerts)
			alerts = append(alerts, alert)
		}
	}
	severity := map[string]int{"critical": 0, "high": 1, "medium": 2, "low": 3}
	sort.Slice(alerts, func(i, j int) bool {
		a, b := alerts[i], alerts[j]
		if severity[a.SecurityAdvisory.Severity] != severity[b.SecurityAdvisory.Severity] {
			return severity[a.SecurityAdvisory.Severity] < severity[b.SecurityAdvisory.Severity]
		}
		if a.UpdatedAt != b.UpdatedAt {
			return a.UpdatedAt > b.UpdatedAt
		}
		return a.Repository.FullName+strconv.Itoa(a.Number) < b.Repository.FullName+strconv.Itoa(b.Number)
	})
	sort.Slice(result.Errors, func(i, j int) bool {
		return result.Errors[i].ConnectionID+result.Errors[i].Source < result.Errors[j].ConnectionID+result.Errors[j].Source
	})
	result.Alerts = alerts
	result.FetchedAt = time.Now().UTC()
	return result
}

func contains(items []string, value string) bool {
	for _, item := range items {
		if item == value {
			return true
		}
	}
	return false
}
