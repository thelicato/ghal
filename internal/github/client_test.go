package github

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func response(status int, body, link string) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{"Link": []string{link}}, Body: io.NopCloser(strings.NewReader(body))}
}

func testClient(handler transportFunc) *Client {
	c := NewClient()
	c.http.Transport = handler
	return c
}

func TestCollectFollowsCursorPagination(t *testing.T) {
	var calls int
	c := testClient(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Header.Get("Authorization") != "Bearer test-secret" || r.Header.Get("X-GitHub-Api-Version") != "2026-03-10" {
			t.Error("missing GitHub authentication or API version")
		}
		if r.URL.Query().Get("after") == "cursor-1" {
			return response(200, `[2]`, ""), nil
		}
		return response(200, `[1]`, `<https://api.github.com/items?after=cursor-1>; rel="next", <https://api.github.com/items?before=cursor-0>; rel="prev"`), nil
	})
	items, err := collect[int](context.Background(), c, "test-secret", "/items")
	if err != nil || len(items) != 2 || items[0] != 1 || items[1] != 2 || calls != 2 {
		t.Fatalf("cursor pagination failed: items=%v, calls=%d, err=%v", items, calls, err)
	}
}

func TestCollectPreservesPagesOnError(t *testing.T) {
	c := testClient(func(r *http.Request) (*http.Response, error) {
		if r.URL.Query().Get("after") != "" {
			return response(403, `{"message":"test-secret should never be returned"}`, ""), nil
		}
		return response(200, `[1]`, `<https://api.github.com/items?after=next>; rel="next"`), nil
	})
	items, err := collect[int](context.Background(), c, "test-secret", "/items")
	if len(items) != 1 || err == nil || strings.Contains(err.Error(), "test-secret") {
		t.Fatalf("expected a partial result and a sanitized error: items=%v err=%v", items, err)
	}
}

func TestCollectRejectsUnsafeAndRepeatedLinks(t *testing.T) {
	for _, next := range []string{"https://evil.example/steal", "http://api.github.com/items", "https://attacker@api.github.com/items", "https://api.github.com/items"} {
		t.Run(next, func(t *testing.T) {
			calls := 0
			c := testClient(func(r *http.Request) (*http.Response, error) {
				calls++
				return response(200, `[1]`, "<"+next+`>; rel="next"`), nil
			})
			_, err := collect[int](context.Background(), c, "test-secret", "/items")
			if err == nil || calls != 1 {
				t.Fatalf("unsafe or repeated link was followed: calls=%d err=%v", calls, err)
			}
		})
	}
}

func alertJSON(repo string, number int, state, severity, updated string) string {
	return fmt.Sprintf(`{"number":%d,"state":%q,"updated_at":%q,"repository":{"full_name":%q,"owner":{"login":"acme"}},"security_advisory":{"severity":%q}}`, number, state, updated, repo, severity)
}

func TestFetchAggregatesDeduplicatesAndReportsFailures(t *testing.T) {
	var repoCalls atomic.Int32
	c := testClient(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/orgs/acme/dependabot/alerts":
			if r.URL.Query().Get("after") == "" && r.URL.Query().Get("state") != "open,fixed,dismissed,auto_dismissed" {
				t.Error("not requesting all alert states")
			}
			if r.URL.Query().Get("after") != "" {
				return response(200, "["+alertJSON("acme/app", 2, "dismissed", "low", "2026-10-02")+"]", ""), nil
			}
			updated := "2026-10-01"
			if r.Header.Get("Authorization") == "Bearer second" {
				updated = "2026-10-03"
			}
			return response(200, "["+alertJSON("acme/app", 1, "open", "critical", updated)+"]", `<https://api.github.com/orgs/acme/dependabot/alerts?after=next>; rel="next"`), nil
		case "/orgs/restricted/dependabot/alerts":
			return response(403, `{"message":"first second"}`, ""), nil
		case "/user/repos":
			if r.URL.Query().Get("after") != "" {
				return response(200, `[{"full_name":"octo/private","owner":{"login":"octo"}}]`, ""), nil
			}
			return response(200, `[{"full_name":"octo/personal","owner":{"login":"octo"}},{"full_name":"someone/other","owner":{"login":"someone"}}]`, `<https://api.github.com/user/repos?after=repo-next>; rel="next"`), nil
		case "/repos/octo/personal/dependabot/alerts", "/repos/octo/private/dependabot/alerts":
			repoCalls.Add(1)
			return response(200, "["+alertJSON("", 1, "fixed", "high", "2026-10-01")+"]", ""), nil
		default:
			t.Errorf("unexpected GitHub endpoint: %s", r.URL.Path)
			return response(500, "", ""), nil
		}
	})
	result := c.Fetch(context.Background(), []Connection{
		{ID: "one", Token: "first", Accounts: []string{"OCTO"}, Organizations: []string{"acme"}},
		{ID: "two", Token: "second", Organizations: []string{"acme", "restricted"}},
	})
	if len(result.Alerts) != 4 || repoCalls.Load() != 2 {
		t.Fatalf("expected four unique alerts including both account repositories: alerts=%d repoCalls=%d", len(result.Alerts), repoCalls.Load())
	}
	first := result.Alerts[0]
	if first.SecurityAdvisory.Severity != "critical" || first.UpdatedAt != "2026-10-03" || len(first.ConnectionIDs) != 2 {
		t.Fatalf("deduplication did not retain newest alert and both connections: %+v", first)
	}
	if len(result.Errors) != 1 || result.Errors[0].Source != "restricted" {
		t.Fatalf("missing source failure: %+v", result.Errors)
	}
	if result.FetchedAt.IsZero() {
		t.Fatal("missing fetch timestamp")
	}
	encoded, _ := json.Marshal(result)
	if strings.Contains(string(encoded), `"token"`) || strings.Contains(string(encoded), "first second") {
		t.Fatal("response exposes credentials or the upstream error body")
	}
}

func TestFetchReportsUnmatchedAccounts(t *testing.T) {
	c := testClient(func(r *http.Request) (*http.Response, error) { return response(200, `[]`, ""), nil })
	result := c.Fetch(context.Background(), []Connection{{ID: "one", Accounts: []string{"missing"}}})
	if len(result.Errors) != 1 || result.Errors[0].Source != "missing" {
		t.Fatalf("missing account was silently omitted: %+v", result.Errors)
	}
}

func TestAPIErrorAndRedirectsDoNotLeakTokens(t *testing.T) {
	for _, status := range []int{401, 403, 404, 429, 500, 302} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			calls := 0
			c := testClient(func(r *http.Request) (*http.Response, error) {
				calls++
				resp := response(status, `{"message":"test-secret"}`, "")
				resp.Header.Set("Location", "https://evil.example/steal")
				return resp, nil
			})
			_, err := c.Login(context.Background(), "test-secret")
			if err == nil || calls != 1 || strings.Contains(err.Error(), "test-secret") {
				t.Fatalf("expected sanitized error without following redirects: calls=%d err=%v", calls, err)
			}
		})
	}
}

func TestPrimaryRateLimit(t *testing.T) {
	c := testClient(func(r *http.Request) (*http.Response, error) {
		resp := response(403, `{}`, "")
		resp.Header.Set("X-RateLimit-Remaining", "0")
		return resp, nil
	})
	_, err := c.Login(context.Background(), "test-secret")
	if err == nil || !strings.Contains(err.Error(), "rate limit") {
		t.Fatalf("rate limit was not explained: %v", err)
	}
}
