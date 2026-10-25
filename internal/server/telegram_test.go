package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thelicato/ghal/internal/github"
	"github.com/thelicato/ghal/internal/storage"
)

type fakeMessenger func(context.Context, string, string, string) error

func (f fakeMessenger) Send(ctx context.Context, token, chatID, message string) error {
	return f(ctx, token, chatID, message)
}

func newMessagingServer(t *testing.T, client GitHub, messenger Messenger) *Server {
	t.Helper()
	store, err := storage.Open(filepath.Join(t.TempDir(), "ghal.db"), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	handler, err := newServer(client, store, messenger)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(handler.Close)
	return handler
}

func connectTelegram(t *testing.T, handler http.Handler) {
	t.Helper()
	res := formRequest(handler, "/settings/telegram", url.Values{"enabled": {"on"}, "bot_token": {"123:telegram-secret"}, "chat_id": {"42"}}, nil)
	if res.Code != 303 {
		t.Fatalf("Telegram configuration was not saved: %d", res.Code)
	}
}

func waitForTelegram(t *testing.T, handler http.Handler) {
	t.Helper()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		res := request(handler, "GET", "/api/telegram/status", "", nil)
		var status telegramView
		if err := json.Unmarshal(res.Body.Bytes(), &status); err != nil {
			t.Fatal(err)
		}
		if !status.Pending {
			return
		}
		select {
		case <-tick.C:
		case <-deadline.C:
			t.Fatal("background Telegram task did not finish")
		}
	}
}

func TestTelegramSettingsKeepTokenSecretAndAllowBlankToRetainIt(t *testing.T) {
	handler := newMessagingServer(t, &fakeGitHub{}, fakeMessenger(func(context.Context, string, string, string) error { return nil }))
	connectTelegram(t, handler)
	for _, path := range []string{"/settings", "/api/telegram/status", "/api/status", "/api/connections", "/api/alerts"} {
		res := request(handler, "GET", path, "", browserCookie())
		if strings.Contains(res.Body.String(), "telegram-secret") || strings.Contains(res.Body.String(), `"Token"`) {
			t.Fatalf("saved bot token exposed in %s", path)
		}
	}
	res := formRequest(handler, "/settings/telegram", url.Values{"bot_token": {""}, "chat_id": {"@mychannel"}}, nil)
	config, err := handler.store.TelegramConfig(context.Background())
	if res.Code != 303 || err != nil || config.Token != "123:telegram-secret" || config.ChatID != "@mychannel" || config.Enabled {
		t.Fatal("blank token was not retained or notifications could not be disabled")
	}
	for _, values := range []url.Values{
		{"enabled": {"on"}, "bot_token": {"123:telegram-secret/unsafe"}, "chat_id": {"42"}},
		{"enabled": {"on"}, "bot_token": {"123:telegram-secret"}, "chat_id": {"0"}},
		{"enabled": {"on"}, "bot_token": {"123:telegram-secret"}, "chat_id": {"invalid/chat"}},
	} {
		res := formRequest(handler, "/settings/telegram", values, nil)
		if res.Code != 400 || strings.Contains(res.Body.String(), "telegram-secret") {
			t.Fatal("invalid Telegram configuration was accepted or exposed a token")
		}
	}
	if err := handler.store.Close(); err != nil {
		t.Fatal(err)
	}
	res = formRequest(handler, "/settings/telegram", url.Values{"enabled": {"on"}, "chat_id": {"43"}}, nil)
	if res.Code != 500 || handler.telegramView().Enabled || handler.telegramView().ChatID != "@mychannel" {
		t.Fatal("failed SQLite write changed live Telegram settings")
	}
}

func TestTelegramTestMessageRunsInBackgroundAndDisconnectCancelsIt(t *testing.T) {
	started, cancelled := make(chan struct{}), make(chan struct{})
	handler := newMessagingServer(t, &fakeGitHub{}, fakeMessenger(func(ctx context.Context, token, chatID, message string) error {
		if token != "123:telegram-secret" || chatID != "42" || !strings.Contains(message, "Telegram test message") {
			t.Error("test delivery did not use saved credentials or test content")
		}
		close(started)
		<-ctx.Done()
		close(cancelled)
		return errors.New("Telegram URL containing 123:telegram-secret")
	}))
	if res := formRequest(handler, "/settings/telegram/test", nil, nil); res.Code != 400 {
		t.Fatal("test was queued without a saved destination")
	}
	connectTelegram(t, handler)
	res := promptly(t, func() *httptest.ResponseRecorder { return formRequest(handler, "/settings/telegram/test", nil, nil) })
	<-started
	if res.Code != 303 || !handler.telegramView().Pending {
		t.Fatal("test message did not return promptly with background delivery status")
	}
	res = promptly(t, func() *httptest.ResponseRecorder {
		return formRequest(handler, "/settings", url.Values{"sync_interval": {"2h"}}, nil)
	})
	if res.Code != 303 {
		t.Fatal("Telegram delivery blocked other settings")
	}
	res = promptly(t, func() *httptest.ResponseRecorder {
		return formRequest(handler, "/settings/telegram/disconnect", nil, nil)
	})
	if res.Code != 303 {
		t.Fatal("disconnect waited for Telegram")
	}
	<-cancelled
	waitForTelegram(t, handler)
	config, err := handler.store.TelegramConfig(context.Background())
	if err != nil || config.Token != "" || config.Enabled || handler.telegramView().LastError != "" {
		t.Fatal("disconnect did not clear the secret or retained a stale delivery error")
	}
}

func TestTelegramNotificationsOnlySendMaterialResyncChanges(t *testing.T) {
	a := pageAlert("first-package", "octocat/project", "high", "open")
	b := pageAlert("removed-package", "octocat/removed", "low", "open")
	current := github.Result{Alerts: []github.Alert{a, b}}
	var messages []string
	handler := newMessagingServer(t, &fakeGitHub{fetch: func(context.Context, []github.Connection) github.Result { return current }}, fakeMessenger(func(ctx context.Context, token, chatID, message string) error {
		messages = append(messages, message)
		return nil
	}))
	refreshAndWait(t, handler)
	connectTelegram(t, handler)
	a.UpdatedAt, a.ConnectionIDs = "2030-01-01T00:00:00Z", []string{"second", "first"}
	current = github.Result{Alerts: []github.Alert{b, a}}
	refreshAndWait(t, handler)
	waitForTelegram(t, handler)
	if len(messages) != 0 {
		t.Fatal("unchanged data or reordered sources caused a notification")
	}
	a.State = "fixed"
	c := pageAlert("new-package", "octocat/new", "critical", "open")
	current = github.Result{Alerts: []github.Alert{a, c}}
	refreshAndWait(t, handler)
	waitForTelegram(t, handler)
	if len(messages) != 1 || !strings.Contains(messages[0], "New: 1 | Updated: 1 | Removed: 1") || !strings.Contains(messages[0], "octocat/project#1 [high, fixed]") || !strings.Contains(messages[0], "Removed: octocat/removed#1") {
		t.Fatal("resync changes were missing or classified incorrectly")
	}
	refreshAndWait(t, handler)
	waitForTelegram(t, handler)
	formRequest(handler, "/alerts/accept", acceptanceForm("octocat/new", "1"), nil)
	if len(messages) != 1 {
		t.Fatal("unchanged refresh or local acceptance produced another notification")
	}
	// Changing monitored connections establishes a fresh scope instead of reporting removals.
	request(handler, "POST", "/api/connections", `{"token":"test-secret"}`, nil)
	waitForRefresh(t, handler)
	waitForTelegram(t, handler)
	if len(messages) != 1 {
		t.Fatal("connection configuration change produced a change notification")
	}
}

func TestPartialResyncDoesNotNotifyOrReplaceCompleteHistory(t *testing.T) {
	a := pageAlert("package", "octocat/project", "high", "open")
	current := github.Result{Alerts: []github.Alert{a}}
	messages := 0
	handler := newMessagingServer(t, &fakeGitHub{fetch: func(context.Context, []github.Connection) github.Result { return current }}, fakeMessenger(func(context.Context, string, string, string) error { messages++; return nil }))
	connectTelegram(t, handler)
	refreshAndWait(t, handler)
	waitForTelegram(t, handler)
	if messages != 0 {
		t.Fatal("initial complete resync did not quietly establish change history")
	}
	current = github.Result{Errors: []github.SourceError{{Source: "restricted", Message: "permission required"}}}
	refreshAndWait(t, handler)
	waitForTelegram(t, handler)
	history, err := handler.store.TelegramSnapshot(context.Background())
	if err != nil || len(history.Alerts) != 1 || messages != 0 {
		t.Fatal("partial resync reported removals or replaced complete history")
	}
	current = github.Result{Alerts: []github.Alert{a}}
	refreshAndWait(t, handler)
	waitForTelegram(t, handler)
	if messages != 0 {
		t.Fatal("recovery from partial results repeated an existing alert")
	}
	current = github.Result{}
	refreshAndWait(t, handler)
	waitForTelegram(t, handler)
	if messages != 1 {
		t.Fatal("complete removal was not reported")
	}
}

func TestTelegramDeliveryFailureRetriesAfterRestartWithoutLeakingSecrets(t *testing.T) {
	a := pageAlert("package", "octocat/project", "high", "open")
	current := github.Result{Alerts: []github.Alert{a}}
	client := &fakeGitHub{fetch: func(context.Context, []github.Connection) github.Result { return current }}
	attempts := 0
	messenger := fakeMessenger(func(context.Context, string, string, string) error {
		attempts++
		if attempts == 1 {
			return errors.New("transport URL contains 123:telegram-secret")
		}
		return nil
	})
	handler := newMessagingServer(t, client, messenger)
	request(handler, "POST", "/api/connections", `{"token":"test-secret"}`, nil)
	waitForRefresh(t, handler)
	connectTelegram(t, handler)
	a.State = "fixed"
	current = github.Result{Alerts: []github.Alert{a}}
	refreshAndWait(t, handler)
	waitForTelegram(t, handler)
	if attempts != 1 || handler.telegramView().LastError == "" {
		t.Fatal("failed delivery did not expose a safe status")
	}
	for _, path := range []string{"/settings", "/api/telegram/status", "/api/alerts"} {
		if res := request(handler, "GET", path, "", nil); strings.Contains(res.Body.String(), "telegram-secret") {
			t.Fatal("delivery failure exposed the bot token")
		}
	}
	handler.Close()
	restarted, err := newServer(client, handler.store, messenger)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(restarted.Close)
	waitForRefresh(t, restarted)
	waitForTelegram(t, restarted)
	if attempts != 2 || restarted.telegramView().LastError != "" || restarted.telegramView().LastSent == "" {
		t.Fatal("failed delivery was not retried from persisted history after restart")
	}
	refreshAndWait(t, restarted)
	waitForTelegram(t, restarted)
	if attempts != 2 {
		t.Fatal("successful delivery did not advance the persisted history")
	}
}

func TestSlowTelegramDeliveryDoesNotHoldRefreshAndStopsOnShutdown(t *testing.T) {
	a := pageAlert("package", "octocat/project", "high", "open")
	current := github.Result{Alerts: []github.Alert{a}}
	started, cancelled := make(chan struct{}), make(chan struct{})
	handler := newMessagingServer(t, &fakeGitHub{fetch: func(context.Context, []github.Connection) github.Result { return current }}, fakeMessenger(func(ctx context.Context, token, chatID, message string) error {
		close(started)
		<-ctx.Done()
		close(cancelled)
		return ctx.Err()
	}))
	refreshAndWait(t, handler)
	connectTelegram(t, handler)
	a.State = "fixed"
	current = github.Result{Alerts: []github.Alert{a}}
	refreshAndWait(t, handler)
	<-started
	if handler.status().Refreshing {
		t.Fatal("Telegram delivery kept the alert refresh running")
	}
	res := promptly(t, func() *httptest.ResponseRecorder { return request(handler, "GET", "/api/alerts", "", nil) })
	if res.Code != 200 || !strings.Contains(res.Body.String(), `"state":"fixed"`) {
		t.Fatal("slow Telegram delivery blocked the updated alert cache")
	}
	// Queue a newer result while the notification worker is still busy.
	refreshAndWait(t, handler)
	closed := make(chan struct{})
	go func() { handler.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not cancel Telegram delivery")
	}
	<-cancelled
	if handler.telegramView().Pending {
		t.Fatal("shutdown left queued Telegram jobs behind")
	}
}

func TestDisabledTelegramSkipsNotificationsButAllowsExplicitTest(t *testing.T) {
	a := pageAlert("package", "octocat/project", "high", "open")
	current := github.Result{Alerts: []github.Alert{a}}
	messages := 0
	handler := newMessagingServer(t, &fakeGitHub{fetch: func(context.Context, []github.Connection) github.Result { return current }}, fakeMessenger(func(context.Context, string, string, string) error { messages++; return nil }))
	refreshAndWait(t, handler)
	connectTelegram(t, handler)
	formRequest(handler, "/settings/telegram", url.Values{"chat_id": {"42"}}, nil)
	a.State = "fixed"
	current = github.Result{Alerts: []github.Alert{a}}
	refreshAndWait(t, handler)
	waitForTelegram(t, handler)
	if messages != 0 {
		t.Fatal("disabled Telegram configuration sent a resync notification")
	}
	if res := formRequest(handler, "/settings/telegram/test", nil, nil); res.Code != 303 {
		t.Fatal("saved disabled configuration could not be tested")
	}
	waitForTelegram(t, handler)
	if messages != 1 {
		t.Fatal("explicit Telegram test was not delivered")
	}
}
