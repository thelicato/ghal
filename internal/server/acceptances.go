package server

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/thelicato/ghal/internal/github"
	"github.com/thelicato/ghal/internal/storage"
)

func (s *Server) acceptAlertPage(w http.ResponseWriter, r *http.Request) {
	s.updateAcceptancePage(w, r, true)
}

func (s *Server) reopenAlertPage(w http.ResponseWriter, r *http.Request) {
	s.updateAcceptancePage(w, r, false)
}

func (s *Server) updateAcceptancePage(w http.ResponseWriter, r *http.Request, accept bool) {
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	defer r.Body.Close()
	failure := &requestError{http.StatusBadRequest, "Choose a valid alert."}
	if err := r.ParseForm(); err == nil {
		number, err := strconv.Atoi(r.PostForm.Get("number"))
		if err == nil && number > 0 && r.PostForm.Get("repository") != "" {
			failure = s.setAlertAcceptance(r.Context(), storage.AlertKey(r.PostForm.Get("repository"), number), accept)
		}
	}
	if failure != nil {
		p := s.currentPage(r)
		p.Error = failure.message
		s.render(w, failure.status, "overview", p)
		return
	}
	// Only the current view's query is retained; the redirect stays on this app.
	target := "/"
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

func (s *Server) setAlertAcceptance(ctx context.Context, id storage.AlertID, accept bool) *requestError {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	state := s.state
	state.mu.Lock()
	defer state.mu.Unlock()
	var found *github.Alert
	for i := range state.result.Alerts {
		alert := &state.result.Alerts[i]
		if storage.AlertKey(alert.Repository.FullName, alert.Number) == id {
			found = alert
			break
		}
	}
	if found == nil {
		return &requestError{http.StatusNotFound, "Alert not found in the current results. Refresh alerts and try again."}
	}
	if accept {
		if found.State != "open" {
			return &requestError{http.StatusConflict, "Only open alerts can be accepted."}
		}
		at, err := s.store.AcceptAlert(ctx, id)
		if err != nil {
			return &requestError{http.StatusInternalServerError, "Could not save accepted alert to SQLite."}
		}
		state.accepted[id] = at
	} else {
		if err := s.store.ReopenAlert(ctx, id); err != nil {
			return &requestError{http.StatusInternalServerError, "Could not reopen accepted alert in SQLite."}
		}
		delete(state.accepted, id)
	}
	return nil
}
