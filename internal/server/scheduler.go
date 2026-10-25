package server

import "time"

// notifyAutoSync coalesces changes without making requests wait for the scheduler.
func (s *Server) notifyAutoSync() {
	select {
	case s.autoSyncWake <- struct{}{}:
	default:
	}
}

func (s *Server) autoSyncLoop() {
	defer s.jobs.Done()
	for {
		s.state.mu.Lock()
		active := !s.closed && !s.state.refreshing && len(s.state.connections) > 0
		delay := time.Until(s.state.lastSyncFinished.Add(s.state.syncInterval))
		s.state.mu.Unlock()

		var timer *time.Timer
		var due <-chan time.Time
		if active {
			timer = time.NewTimer(delay)
			due = timer.C
		}
		select {
		case <-s.ctx.Done():
			if timer != nil {
				timer.Stop()
			}
			return
		case <-s.autoSyncWake:
		case <-due:
			s.state.mu.Lock()
			// Recheck under the lock in case settings or connections just changed.
			if !s.closed && !s.state.refreshing && len(s.state.connections) > 0 && !time.Now().Before(s.state.lastSyncFinished.Add(s.state.syncInterval)) {
				s.startRefreshLocked()
			}
			s.state.mu.Unlock()
		}
		if timer != nil {
			timer.Stop()
		}
	}
}
