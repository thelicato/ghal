package server

import (
	"maps"
	"sync"
	"time"

	"github.com/thelicato/ghal/internal/github"
	"github.com/thelicato/ghal/internal/storage"
)

// All browsers use the same connections and alert cache for this deployment.
type state struct {
	mu          sync.Mutex
	connections []github.Connection
	accepted    map[storage.AlertID]time.Time
	refreshing  bool
	result      github.Result
	loaded      bool
	revision    uint64
}

func (s *state) snapshot() []github.Connection {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]github.Connection{}, s.connections...)
}

func (s *state) pageSnapshot() ([]github.Connection, github.Result, bool, bool, map[storage.AlertID]time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]github.Connection{}, s.connections...), s.result, s.loaded, s.refreshing, maps.Clone(s.accepted)
}

// The caller holds s.mu.
func (s *state) invalidateLocked() {
	s.revision++
	s.result = github.Result{}
	s.loaded = false
}
