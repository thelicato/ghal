package server

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
	"unicode/utf16"

	"github.com/thelicato/ghal/internal/github"
)

func TestScheduledResyncSendsTelegramWithoutBrowserRequests(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls, messages atomic.Int32
		handler := newMessagingServer(t, &fakeGitHub{fetch: func(context.Context, []github.Connection) github.Result {
			n := calls.Add(1)
			a := pageAlert("package", "octocat/project", "high", "open")
			if n > 1 {
				a.State = "fixed"
			}
			return github.Result{Alerts: []github.Alert{a}}
		}}, fakeMessenger(func(context.Context, string, string, string) error { messages.Add(1); return nil }))
		request(handler, "POST", "/api/connections", `{"token":"test-secret"}`, nil)
		synctest.Wait()
		connectTelegram(t, handler)
		time.Sleep(time.Hour)
		synctest.Wait()
		if calls.Load() != 2 || messages.Load() != 1 {
			t.Fatal("scheduled resync did not send its changes in the background")
		}
		time.Sleep(time.Hour)
		synctest.Wait()
		if messages.Load() != 1 {
			t.Fatal("unchanged scheduled resync repeated the notification")
		}
	})
}

func TestNotificationSummaryFitsTelegramLimitForLargeUnicodeChanges(t *testing.T) {
	var alerts []github.Alert
	for i := range 100 {
		a := pageAlert(strings.Repeat("🦊", 300), fmt.Sprintf("octocat/project-%03d", i), "critical", "open")
		alerts = append(alerts, a)
	}
	before := notificationSnapshot("scope", nil)
	after := notificationSnapshot("scope", alerts)
	message := notificationMessage(before, after, alerts)
	if len(utf16.Encode([]rune(message))) > 4096 || !strings.Contains(message, "New: 100") || !strings.Contains(message, "88 more changes.") {
		t.Fatal("large summary exceeds Telegram's limit or loses the full change count")
	}
}
