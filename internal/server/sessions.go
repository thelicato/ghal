package server

import (
	"crypto/rand"
	"net/http"
	"sync"
	"time"

	"dbaui/internal/github"
)

const sessionLifetime = 12 * time.Hour

type session struct {
	mu          sync.Mutex
	connections []github.Connection
	refreshing  bool
	result      github.Result
	loaded      bool
	revision    uint64
}

type sessionEntry struct {
	value   *session
	expires time.Time
}

type sessions struct {
	mu      sync.Mutex
	entries map[string]sessionEntry
	secure  bool
}

func (s *sessions) get(w http.ResponseWriter, r *http.Request) *session {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for id, entry := range s.entries {
		if now.After(entry.expires) {
			delete(s.entries, id)
		}
	}
	if cookie, err := r.Cookie("dbaui_session"); err == nil {
		if entry, ok := s.entries[cookie.Value]; ok {
			return entry.value
		}
	}
	if len(s.entries) >= 1000 {
		writeError(w, http.StatusServiceUnavailable, "Too many active sessions. Try again later.")
		return nil
	}
	id := rand.Text()
	value := &session{connections: make([]github.Connection, 0)}
	s.entries[id] = sessionEntry{value: value, expires: now.Add(sessionLifetime)}
	http.SetCookie(w, &http.Cookie{
		Name: "dbaui_session", Value: id, Path: "/", HttpOnly: true,
		Secure: s.secure || r.TLS != nil, SameSite: http.SameSiteStrictMode,
		MaxAge: int(sessionLifetime.Seconds()),
	})
	return value
}

func (s *session) snapshot() []github.Connection {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]github.Connection{}, s.connections...)
}

func (s *session) pageSnapshot() ([]github.Connection, github.Result, bool, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]github.Connection{}, s.connections...), s.result, s.loaded, s.refreshing
}

// Invalidate cached alerts when credentials change, including during a refresh.
// The caller holds s.mu.
func (s *session) invalidateLocked() {
	s.revision++
	s.result = github.Result{}
	s.loaded = false
}
