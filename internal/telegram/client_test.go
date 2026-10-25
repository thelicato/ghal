package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

type transport func(*http.Request) (*http.Response, error)

func (f transport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestSendPlainTextWithChatAndNoPreview(t *testing.T) {
	client := NewClient()
	client.http.Transport = transport(func(r *http.Request) (*http.Response, error) {
		if r.Method != "POST" || r.URL.Scheme != "https" || r.URL.Host != "api.telegram.org" || r.URL.Path != "/bot123:test-secret/sendMessage" {
			t.Fatal("unexpected Telegram request destination or method")
		}
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if r.Form.Get("chat_id") != "-100123456" || r.Form.Get("text") != "ghal: <plain> & unchanged" || r.Form.Get("parse_mode") != "" || r.Form.Get("link_preview_options") != `{"is_disabled":true}` {
			t.Fatal("Telegram request lost its chat, plain text, or preview settings")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":{"message_id":1}}`))}, nil
	})
	if err := client.Send(context.Background(), "123:test-secret", "-100123456", "ghal: <plain> & unchanged"); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(Config{Token: "123:test-secret", ChatID: "-100123456"})
	if strings.Contains(string(data), "test-secret") {
		t.Fatal("serializing Telegram settings exposes the bot token")
	}
}

func TestSendFailuresDoNotExposeTokenOrResponseDescription(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
		body   string
		fail   bool
	}{
		{"unauthorized", 401, "test-secret", false}, {"forbidden", 403, "test-secret", false},
		{"rate limit", 429, "test-secret", false}, {"unavailable", 503, "test-secret", false},
		{"api failure", 200, `{"ok":false,"error_code":400,"description":"test-secret"}`, false},
		{"unreadable", 200, "test-secret", false}, {"transport", 0, "", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := NewClient()
			client.http.Transport = transport(func(r *http.Request) (*http.Response, error) {
				if test.fail {
					return nil, &url.Error{Op: "Post", URL: r.URL.String(), Err: errors.New("test-secret")}
				}
				return &http.Response{StatusCode: test.status, Body: io.NopCloser(strings.NewReader(test.body))}, nil
			})
			err := client.Send(context.Background(), "123:test-secret", "42", "message")
			if err == nil || strings.Contains(err.Error(), "test-secret") || strings.Contains(err.Error(), "api.telegram.org/bot") {
				t.Fatal("Telegram failure was hidden or exposed credentials")
			}
		})
	}
}

func TestSendDoesNotForwardTokenThroughRedirect(t *testing.T) {
	client := NewClient()
	calls := 0
	client.http.Transport = transport(func(r *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 302, Header: http.Header{"Location": {"https://elsewhere.example/"}}, Body: io.NopCloser(strings.NewReader(""))}, nil
	})
	if err := client.Send(context.Background(), "123:test-secret", "@channel", "message"); err == nil || calls != 1 {
		t.Fatal("Telegram redirect was followed or accepted")
	}
}

func TestSendRejectsUnsafeTokenPathsAndInvalidMessages(t *testing.T) {
	client := NewClient()
	client.http.Transport = transport(func(*http.Request) (*http.Response, error) {
		t.Fatal("invalid configuration reached Telegram")
		return nil, errors.New("unexpected request")
	})
	for _, token := range []string{"", "123:../secret", "123:secret/path", "123:secret?query", "123:secret\n"} {
		if err := client.Send(context.Background(), token, "42", "message"); err == nil {
			t.Fatal("unsafe bot token was accepted")
		}
	}
	for _, message := range []string{"", strings.Repeat("a", 4097)} {
		if err := client.Send(context.Background(), "123:test-secret", "42", message); err == nil {
			t.Fatal("invalid message length was accepted")
		}
	}
}
