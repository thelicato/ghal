package server

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/thelicato/ghal/internal/storage"
	"github.com/thelicato/ghal/internal/telegram"
)

type telegramView struct {
	Enabled    bool   `json:"enabled"`
	Configured bool   `json:"configured"`
	ChatID     string `json:"chat_id"`
	Pending    bool   `json:"pending"`
	LastError  string `json:"last_error"`
	LastSent   string `json:"last_sent"`
	Status     string `json:"status"`
}

func (s *Server) telegramView() telegramView {
	s.state.mu.Lock()
	defer s.state.mu.Unlock()
	state := s.state
	v := telegramView{
		Enabled: state.telegram.Enabled, Configured: state.telegram.Token != "", ChatID: state.telegram.ChatID,
		Pending:   state.telegramBusy || state.messagePending != nil || state.testPending != nil,
		LastError: state.telegramError,
	}
	if !state.telegramLastSent.IsZero() {
		v.LastSent = state.telegramLastSent.UTC().Format("02 Jan 2006 15:04:05 UTC")
	}
	switch {
	case v.Pending:
		v.Status = "Telegram message queued or being sent."
	case v.LastError != "":
		v.Status = v.LastError
	case v.LastSent != "":
		v.Status = "Last message sent: " + v.LastSent
	case v.Configured:
		v.Status = "Bot token saved. Send a test message to check delivery."
	default:
		v.Status = "Telegram is not configured."
	}
	return v
}

func (s *Server) telegramStatusAPI(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.telegramView())
}

func (s *Server) telegramPageError(w http.ResponseWriter, r *http.Request, status int, message string) {
	s.state.mu.Lock()
	interval := s.state.syncInterval
	s.state.mu.Unlock()
	s.renderSettings(w, r, status, formatSyncInterval(interval), message, false)
}

func (s *Server) saveTelegramPage(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	defer r.Body.Close()
	if err := r.ParseForm(); err != nil {
		s.telegramPageError(w, r, http.StatusBadRequest, "Enter valid Telegram settings.")
		return
	}
	config := telegram.Config{Enabled: r.PostForm.Get("enabled") == "on", ChatID: strings.TrimSpace(r.PostForm.Get("chat_id")), Token: strings.TrimSpace(r.PostForm.Get("bot_token"))}
	if failure := s.setTelegramConfig(r.Context(), config, true); failure != nil {
		s.telegramPageError(w, r, failure.status, failure.message)
		return
	}
	http.Redirect(w, r, "/settings?telegram=saved", http.StatusSeeOther)
}

func (s *Server) setTelegramConfig(ctx context.Context, config telegram.Config, keepToken bool) *requestError {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	s.state.mu.Lock()
	defer s.state.mu.Unlock()
	// Resolve an unchanged password under the same lock as token rotation and saving.
	if keepToken && config.Token == "" {
		config.Token = s.state.telegram.Token
	}
	if err := config.Validate(); err != nil {
		return &requestError{http.StatusBadRequest, err.Error()}
	}
	var snapshot *storage.NotificationSnapshot
	if config.Enabled && s.state.loaded && s.state.resultRevision == s.state.revision && len(s.state.result.Errors) == 0 {
		snapshot = notificationSnapshot(notificationScope(s.state.connections), s.state.result.Alerts)
	}
	if err := s.store.SetTelegramConfig(ctx, config, snapshot); err != nil {
		return &requestError{http.StatusInternalServerError, "Could not save Telegram settings to SQLite."}
	}
	if s.state.telegramCancel != nil {
		s.state.telegramCancel()
	}
	s.state.telegram = config
	s.state.telegramGeneration++
	s.state.telegramError, s.state.telegramLastSent = "", time.Time{}
	s.state.messagePending, s.state.testPending = nil, nil
	return nil
}

func (s *Server) testTelegramPage(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	s.state.mu.Lock()
	if s.state.telegram.Token == "" || s.closed {
		s.state.mu.Unlock()
		s.telegramPageError(w, r, http.StatusBadRequest, "Save a Telegram bot token and chat ID first.")
		return
	}
	s.state.testPending = &notificationJob{generation: s.state.telegramGeneration, test: true}
	s.wakeMessages()
	s.state.mu.Unlock()
	http.Redirect(w, r, "/settings?telegram=test", http.StatusSeeOther)
}

func (s *Server) disconnectTelegramPage(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	if failure := s.setTelegramConfig(r.Context(), telegram.Config{}, false); failure != nil {
		s.telegramPageError(w, r, failure.status, failure.message)
		return
	}
	http.Redirect(w, r, "/settings?telegram=disconnected", http.StatusSeeOther)
}
