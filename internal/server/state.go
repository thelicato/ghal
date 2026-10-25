package server

import (
	"context"
	"maps"
	"sync"
	"time"

	"github.com/thelicato/ghal/internal/github"
	"github.com/thelicato/ghal/internal/storage"
	"github.com/thelicato/ghal/internal/telegram"
)

// All browsers use the same connections and alert cache for this deployment.
type state struct {
	mu                 sync.Mutex
	connections        []github.Connection
	accepted           map[storage.AlertID]time.Time
	refreshing         bool
	refreshCancel      context.CancelFunc
	result             github.Result
	loaded             bool
	revision           uint64
	version            uint64
	syncInterval       time.Duration
	lastSyncFinished   time.Time
	resultRevision     uint64
	telegram           telegram.Config
	telegramGeneration uint64
	telegramBusy       bool
	telegramError      string
	telegramLastSent   time.Time
	telegramCancel     context.CancelFunc
	messagePending     *notificationJob
	testPending        *notificationJob
}

func (s *state) snapshot() []github.Connection {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]github.Connection{}, s.connections...)
}

func (s *state) pageSnapshot() ([]github.Connection, github.Result, bool, bool, map[storage.AlertID]time.Time, uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]github.Connection{}, s.connections...), s.result, s.loaded, s.refreshing, maps.Clone(s.accepted), s.version
}

// The caller holds s.mu.
func (s *state) invalidateLocked() {
	s.revision++
	s.version++
	if s.refreshCancel != nil {
		s.refreshCancel()
	}
	if s.telegramCancel != nil {
		s.telegramCancel()
	}
	// Keep cached results usable, but drop data belonging only to removed connections.
	ids := make(map[string]bool, len(s.connections))
	for _, conn := range s.connections {
		ids[conn.ID] = true
	}
	result := github.Result{FetchedAt: s.result.FetchedAt, Alerts: []github.Alert{}, Errors: []github.SourceError{}}
	for _, alert := range s.result.Alerts {
		retained := make([]string, 0, len(alert.ConnectionIDs))
		for _, id := range alert.ConnectionIDs {
			if ids[id] {
				retained = append(retained, id)
			}
		}
		if len(retained) > 0 {
			alert.ConnectionIDs = retained
			result.Alerts = append(result.Alerts, alert)
		}
	}
	for _, issue := range s.result.Errors {
		if ids[issue.ConnectionID] {
			result.Errors = append(result.Errors, issue)
		}
	}
	s.result = result
}
