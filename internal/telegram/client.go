// Package telegram sends plain-text notifications through the Telegram Bot API.
package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

type Config struct {
	Enabled bool
	ChatID  string
	Token   string `json:"-"`
}

var tokenPattern = regexp.MustCompile(`^[0-9]+:[A-Za-z0-9_-]+$`)
var channelPattern = regexp.MustCompile(`^@[A-Za-z0-9_]{1,64}$`)

func (c Config) Validate() error {
	if !c.Enabled && c.ChatID == "" && c.Token == "" {
		return nil
	}
	if len(c.Token) > 512 || !tokenPattern.MatchString(c.Token) {
		return errors.New("Enter a valid Telegram bot token.")
	}
	if !channelPattern.MatchString(c.ChatID) {
		id, err := strconv.ParseInt(c.ChatID, 10, 64)
		if err != nil || id == 0 {
			return errors.New("Enter a numeric Telegram chat ID or an @channel username.")
		}
	}
	return nil
}

type Client struct {
	http    *http.Client
	baseURL string
}

func NewClient() *Client {
	return &Client{
		http:    &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		baseURL: "https://api.telegram.org",
	}
}

// APIError excludes response descriptions and URLs, which may contain the bot token.
type APIError struct{ Status int }

func (e *APIError) Error() string {
	switch e.Status {
	case 401, 404:
		return "Telegram rejected the bot token. Check it in Settings."
	case 400, 403:
		return "Telegram could not send to this chat. Check the chat ID, start the bot, and check its chat permissions."
	case 429:
		return "Telegram rate limit reached. Try again later."
	default:
		return "Telegram is unavailable. Try again later."
	}
}

func (c *Client) Send(ctx context.Context, token, chatID, message string) error {
	if err := (Config{Token: token, ChatID: chatID, Enabled: true}).Validate(); err != nil {
		return err
	}
	if message == "" || utf8.RuneCountInString(message) > 4096 {
		return errors.New("Telegram message must contain between 1 and 4096 characters.")
	}
	form := url.Values{"chat_id": {chatID}, "text": {message}, "link_preview_options": {`{"is_disabled":true}`}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/bot"+token+"/sendMessage", strings.NewReader(form.Encode()))
	if err != nil {
		return errors.New("Could not create the Telegram request.")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "ghal")
	resp, err := c.http.Do(req)
	if err != nil {
		return errors.New("Could not reach Telegram.")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return &APIError{Status: resp.StatusCode}
	}
	var result struct {
		OK        bool `json:"ok"`
		ErrorCode int  `json:"error_code"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&result); err != nil {
		return errors.New("Telegram returned an unreadable response.")
	}
	if !result.OK {
		return &APIError{Status: result.ErrorCode}
	}
	return nil
}
