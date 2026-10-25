package server

import (
	"context"
	"fmt"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/thelicato/ghal/internal/github"
)

func TestAutomaticResyncUsesHourlyDefaultAndStopsOnClose(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		handler := newTestServer(t, &fakeGitHub{fetch: func(context.Context, []github.Connection) github.Result {
			n := calls.Add(1)
			return github.Result{Alerts: []github.Alert{pageAlert(fmt.Sprintf("package-%d", n), "octocat/project", "high", "open")}}
		}})
		request(handler, "POST", "/api/connections", `{"token":"test-secret"}`, nil)
		synctest.Wait()
		if calls.Load() != 1 {
			t.Fatal("adding a connection did not perform the initial refresh")
		}
		time.Sleep(time.Hour - time.Nanosecond)
		synctest.Wait()
		if calls.Load() != 1 {
			t.Fatal("automatic resync ran before the configured interval")
		}
		time.Sleep(time.Nanosecond)
		synctest.Wait()
		if calls.Load() != 2 || handler.status().Refreshing {
			t.Fatal("automatic resync did not complete after one hour")
		}
		if res := request(handler, "GET", "/api/alerts", "", nil); !strings.Contains(res.Body.String(), "package-2") {
			t.Fatal("automatic resync did not publish the latest alerts")
		}
		handler.Close()
		time.Sleep(2 * time.Hour)
		synctest.Wait()
		if calls.Load() != 2 {
			t.Fatal("automatic resync continued after shutdown")
		}
	})
}

func TestAutomaticResyncRestoresSavedIntervalAfterRestart(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		client := &fakeGitHub{fetch: func(context.Context, []github.Connection) github.Result {
			calls.Add(1)
			return github.Result{}
		}}
		handler := newTestServer(t, client)
		formRequest(handler, "/settings", url.Values{"sync_interval": {"2h"}}, nil)
		request(handler, "POST", "/api/connections", `{"token":"test-secret"}`, nil)
		synctest.Wait()
		handler.Close()
		restarted, err := New(client, handler.store)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(restarted.Close)
		synctest.Wait()
		if calls.Load() != 2 {
			t.Fatal("saved connection did not refresh on restart")
		}
		time.Sleep(2*time.Hour - time.Nanosecond)
		synctest.Wait()
		if calls.Load() != 2 {
			t.Fatal("restart used the default interval instead of the saved interval")
		}
		time.Sleep(time.Nanosecond)
		synctest.Wait()
		if calls.Load() != 3 {
			t.Fatal("saved interval did not trigger automatic resync after restart")
		}
	})
}

func TestChangingResyncIntervalReschedulesWithoutRestart(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		handler := newTestServer(t, &fakeGitHub{fetch: func(context.Context, []github.Connection) github.Result {
			calls.Add(1)
			return github.Result{}
		}})
		request(handler, "POST", "/api/connections", `{"token":"test-secret"}`, nil)
		synctest.Wait()
		time.Sleep(10 * time.Minute)
		res := formRequest(handler, "/settings", url.Values{"sync_interval": {"5m"}}, nil)
		synctest.Wait()
		if res.Code != 303 || calls.Load() != 2 {
			t.Fatal("shorter interval did not immediately refresh an overdue cache")
		}
		formRequest(handler, "/settings", url.Values{"sync_interval": {"2h"}}, nil)
		synctest.Wait()
		time.Sleep(5 * time.Minute)
		synctest.Wait()
		if calls.Load() != 2 {
			t.Fatal("old timer still fired after increasing the interval")
		}
		formRequest(handler, "/refresh", nil, nil)
		synctest.Wait()
		if calls.Load() != 3 {
			t.Fatal("manual refresh did not run")
		}
		time.Sleep(2*time.Hour - time.Nanosecond)
		synctest.Wait()
		if calls.Load() != 3 {
			t.Fatal("manual refresh did not restart the interval")
		}
		time.Sleep(time.Nanosecond)
		synctest.Wait()
		if calls.Load() != 4 {
			t.Fatal("new interval did not schedule another refresh")
		}
	})
}

func TestAutomaticResyncDoesNotOverlapAndWaitsAfterCompletion(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		release := make(chan struct{})
		var calls atomic.Int32
		handler := newTestServer(t, &fakeGitHub{fetch: func(ctx context.Context, connections []github.Connection) github.Result {
			if calls.Add(1) == 2 {
				select {
				case <-release:
				case <-ctx.Done():
				}
			}
			return github.Result{}
		}})
		request(handler, "POST", "/api/connections", `{"token":"test-secret"}`, nil)
		synctest.Wait()
		formRequest(handler, "/settings", url.Values{"sync_interval": {"1m"}}, nil)
		synctest.Wait()
		time.Sleep(time.Minute)
		synctest.Wait()
		if calls.Load() != 2 || !handler.status().Refreshing {
			t.Fatal("scheduled background refresh did not start")
		}
		res := promptly(t, func() *httptest.ResponseRecorder {
			return formRequest(handler, "/settings", url.Values{"sync_interval": {"1m"}}, nil)
		})
		if res.Code != 303 {
			t.Fatal("settings could not be saved while GitHub was busy")
		}
		request(handler, "POST", "/api/refresh", "", nil)
		time.Sleep(2 * time.Minute)
		synctest.Wait()
		if calls.Load() != 2 {
			t.Fatal("manual or automatic refresh overlapped the running job")
		}
		close(release)
		synctest.Wait()
		time.Sleep(time.Minute - time.Nanosecond)
		synctest.Wait()
		if calls.Load() != 2 {
			t.Fatal("interval was measured from the start of the previous refresh")
		}
		time.Sleep(time.Nanosecond)
		synctest.Wait()
		if calls.Load() != 3 {
			t.Fatal("resync did not resume after the running job completed")
		}
	})
}

func TestAutomaticResyncSkipsEmptyConnections(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		handler := newTestServer(t, &fakeGitHub{fetch: func(context.Context, []github.Connection) github.Result {
			calls.Add(1)
			return github.Result{}
		}})
		formRequest(handler, "/settings", url.Values{"sync_interval": {"1m"}}, nil)
		synctest.Wait()
		time.Sleep(5 * time.Minute)
		synctest.Wait()
		if calls.Load() != 0 {
			t.Fatal("automatic resync fetched without configured connections")
		}
		request(handler, "POST", "/api/connections", `{"token":"test-secret"}`, nil)
		synctest.Wait()
		connections := handler.state.snapshot()
		request(handler, "DELETE", "/api/connections/"+connections[0].ID, "", nil)
		synctest.Wait()
		before := calls.Load()
		time.Sleep(5 * time.Minute)
		synctest.Wait()
		if calls.Load() != before {
			t.Fatal("automatic resync continued after removing the last connection")
		}
	})
}
