package server

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/thelicato/ghal/internal/storage"
)

type settingsView struct {
	SyncInterval string
	Saved        bool
}

func formatSyncInterval(interval time.Duration) string {
	if interval%time.Hour == 0 {
		return fmt.Sprintf("%dh", interval/time.Hour)
	}
	if interval%time.Minute == 0 {
		return strings.TrimSuffix(interval.String(), "0s")
	}
	return interval.String()
}

func (s *Server) settingsPage(w http.ResponseWriter, r *http.Request) {
	s.state.mu.Lock()
	interval := s.state.syncInterval
	s.state.mu.Unlock()
	s.renderSettings(w, r, http.StatusOK, formatSyncInterval(interval), "", r.URL.Query().Get("saved") == "1")
}

func (s *Server) renderSettings(w http.ResponseWriter, r *http.Request, status int, value, message string, saved bool) {
	p := s.currentPage(r)
	p.Title, p.View, p.Error = "Settings", "settings", message
	p.Settings = settingsView{SyncInterval: value, Saved: saved}
	s.render(w, status, "settings", p)
}

func (s *Server) saveSettingsPage(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	defer r.Body.Close()
	if err := r.ParseForm(); err != nil {
		s.renderSettings(w, r, http.StatusBadRequest, "", "Enter a valid resync interval.", false)
		return
	}
	value := strings.TrimSpace(r.PostForm.Get("sync_interval"))
	interval, err := time.ParseDuration(value)
	if err != nil || len(value) > 64 || interval < storage.MinSyncInterval {
		s.renderSettings(w, r, http.StatusBadRequest, value, "Enter a duration of at least 1m, such as 30m, 1h, or 2h30m.", false)
		return
	}
	if failure := s.setSyncInterval(r.Context(), interval); failure != nil {
		s.renderSettings(w, r, failure.status, value, failure.message, false)
		return
	}
	http.Redirect(w, r, "/settings?saved=1", http.StatusSeeOther)
}

func (s *Server) setSyncInterval(ctx context.Context, interval time.Duration) *requestError {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	s.state.mu.Lock()
	defer s.state.mu.Unlock()
	if err := s.store.SetSyncInterval(ctx, interval); err != nil {
		return &requestError{http.StatusInternalServerError, "Could not save settings to SQLite."}
	}
	s.state.syncInterval = interval
	s.notifyAutoSync()
	return nil
}
