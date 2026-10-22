package server

import (
	"sync"

	"dbaui/internal/github"
)

// All browsers use the same connections and alert cache for this deployment.
type state struct {
	mu          sync.Mutex
	connections []github.Connection
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

func (s *state) pageSnapshot() ([]github.Connection, github.Result, bool, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]github.Connection{}, s.connections...), s.result, s.loaded, s.refreshing
}

// The caller holds s.mu.
func (s *state) invalidateLocked() {
	s.revision++
	s.result = github.Result{}
	s.loaded = false
}
