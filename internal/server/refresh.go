package server

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/thelicato/ghal/internal/github"
)

const authenticationSource = "Token verification"

type syncStatus struct {
	Refreshing bool   `json:"refreshing"`
	Loaded     bool   `json:"loaded"`
	Version    uint64 `json:"version"`
}

func (s *Server) status() syncStatus {
	s.state.mu.Lock()
	defer s.state.mu.Unlock()
	return syncStatus{s.state.refreshing, s.state.loaded, s.state.version}
}

func (s *Server) syncStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.status())
}

func (s *Server) refreshAPI(w http.ResponseWriter, r *http.Request) {
	s.requestRefresh()
	writeJSON(w, http.StatusAccepted, s.status())
}

func (s *Server) requestRefresh() {
	s.state.mu.Lock()
	defer s.state.mu.Unlock()
	s.startRefreshLocked()
}

// The caller holds state.mu. Repeated refresh requests share the running job.
func (s *Server) startRefreshLocked() {
	if s.closed || s.state.refreshing {
		return
	}
	s.state.refreshing = true
	s.jobs.Add(1)
	go s.refreshLoop()
}

func (s *Server) refreshLoop() {
	defer s.jobs.Done()
	state := s.state
	for {
		state.mu.Lock()
		if s.closed {
			state.refreshing = false
			state.mu.Unlock()
			return
		}
		connections := append([]github.Connection{}, state.connections...)
		revision := state.revision
		ctx, cancel := context.WithTimeout(s.ctx, 5*time.Minute)
		state.refreshCancel = cancel
		state.mu.Unlock()

		result := s.fetch(ctx, connections)
		cancel()

		state.mu.Lock()
		state.refreshCancel = nil
		if !s.closed && state.revision != revision {
			// A connection changed. Discard this snapshot and fetch the latest set.
			state.mu.Unlock()
			continue
		}
		if !s.closed {
			state.result = result
			state.loaded = true
			state.version++
		}
		state.refreshing = false
		state.mu.Unlock()
		return
	}
}

func (s *Server) fetch(ctx context.Context, connections []github.Connection) github.Result {
	ready := make([]github.Connection, 0, len(connections))
	var issues []github.SourceError
	for _, conn := range connections {
		if conn.Login == "" {
			verified, err := s.verifyConnection(ctx, conn)
			if err != nil {
				issues = append(issues, github.SourceError{ConnectionID: conn.ID, Source: authenticationSource, Message: err.Error()})
				continue
			}
			conn = verified
		}
		ready = append(ready, conn)
	}
	result := github.Result{Alerts: []github.Alert{}, Errors: []github.SourceError{}}
	if len(ready) > 0 || len(connections) == 0 {
		result = s.github.Fetch(ctx, ready)
	}
	result.Errors = append(result.Errors, issues...)
	if result.FetchedAt.IsZero() {
		result.FetchedAt = time.Now().UTC()
	}
	return result
}

func (s *Server) verifyConnection(ctx context.Context, conn github.Connection) (github.Connection, error) {
	loginCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	login, err := s.github.Login(loginCtx, conn.Token)
	if err != nil {
		// Only expose known, credential-safe GitHub errors.
		var apiError *github.APIError
		if errors.As(err, &apiError) {
			return conn, apiError
		}
		return conn, errors.New("Could not verify this token with GitHub. Try refreshing again.")
	}
	if login == "" {
		return conn, errors.New("GitHub did not return an account for this token. Try refreshing again.")
	}
	conn.Login = login
	if conn.Label == "" {
		conn.Label = login
	}
	if len(conn.Accounts) == 0 && len(conn.Organizations) == 0 {
		conn.Accounts = []string{login}
	}
	s.state.mu.Lock()
	defer s.state.mu.Unlock()
	for i, current := range s.state.connections {
		if current.ID == conn.ID {
			if err := s.store.UpdateVerifiedConnection(ctx, conn); err != nil {
				return conn, errors.New("Could not save verified connection to SQLite. Try refreshing again.")
			}
			s.state.connections[i] = conn
			return conn, nil
		}
	}
	return conn, errors.New("Connection was removed.")
}
