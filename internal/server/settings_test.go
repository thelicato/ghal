package server

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestSettingsPageDefaultsAndSavedIntervalSurviveRestart(t *testing.T) {
	handler := newTestServer(t, &fakeGitHub{})
	res := request(handler, "GET", "/settings", "", nil)
	body := res.Body.String()
	if res.Code != 200 || !strings.Contains(body, `value="1h"`) || !strings.Contains(body, `href="/settings" aria-current="page"`) || !strings.Contains(body, `name="sync_interval"`) {
		t.Fatal("settings page is missing the default, navigation, or native form")
	}
	if res.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("settings page can be cached")
	}
	res = formRequest(handler, "/settings", url.Values{"sync_interval": {" 1h30m "}}, nil)
	if res.Code != 303 || res.Header().Get("Location") != "/settings?saved=1" {
		t.Fatal("settings form did not save and redirect", res.Code)
	}
	res = request(handler, "GET", "/settings?saved=1", "", browserCookie())
	if !strings.Contains(res.Body.String(), `value="1h30m"`) || !strings.Contains(res.Body.String(), "Settings saved.") {
		t.Fatal("saved settings are not shared across browsers")
	}
	handler.Close()
	restarted, err := New(&fakeGitHub{}, handler.store)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(restarted.Close)
	if res = request(restarted, "GET", "/settings", "", nil); !strings.Contains(res.Body.String(), `value="1h30m"`) {
		t.Fatal("server restart lost the configured interval")
	}
}

func TestInvalidSettingsDoNotChangeSavedOrLiveInterval(t *testing.T) {
	handler := newTestServer(t, &fakeGitHub{})
	for _, value := range []string{"", "invalid", "0", "0s", "59s", "-1h", "9999999999999999999h", `<script>alert(1)</script>`, strings.Repeat("x", 5000)} {
		res := formRequest(handler, "/settings", url.Values{"sync_interval": {value}}, nil)
		if res.Code != 400 || strings.Contains(res.Body.String(), "<script>alert(1)</script>") {
			t.Fatal("invalid settings were accepted or unsafe input was rendered", res.Code)
		}
		interval, err := handler.store.SyncInterval(context.Background())
		if err != nil || interval != time.Hour || handler.state.syncInterval != time.Hour {
			t.Fatal("invalid input changed the persisted or live interval")
		}
	}
}

func TestFailedSettingsWriteDoesNotChangeLiveInterval(t *testing.T) {
	handler := newTestServer(t, &fakeGitHub{})
	if err := handler.store.Close(); err != nil {
		t.Fatal(err)
	}
	res := formRequest(handler, "/settings", url.Values{"sync_interval": {"2h"}}, nil)
	if res.Code != 500 || !strings.Contains(res.Body.String(), "Could not save settings to SQLite.") || handler.state.syncInterval != time.Hour {
		t.Fatal("failed settings write changed live configuration or was not reported")
	}
}
