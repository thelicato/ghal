package server

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/thelicato/ghal/internal/storage"
)

func (s *Server) acceptAlertPage(w http.ResponseWriter, r *http.Request) {
	s.updateAcceptancePage(w, r, true)
}

func (s *Server) reopenAlertPage(w http.ResponseWriter, r *http.Request) {
	s.updateAcceptancePage(w, r, false)
}

func (s *Server) acceptSelectedAlertsPage(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	defer r.Body.Close()
	var ids []storage.AlertID
	failure := &requestError{http.StatusBadRequest, fmt.Sprintf("Select between 1 and %d open alerts.", pageSize)}
	if err := r.ParseForm(); err == nil {
		values := r.PostForm["alerts"]
		if len(values) > 0 && len(values) <= pageSize {
			failure = nil
			for _, value := range values {
				repository, rawNumber, ok := strings.Cut(value, "#")
				number, err := strconv.Atoi(rawNumber)
				if !ok || repository == "" || err != nil || number <= 0 {
					failure = &requestError{http.StatusBadRequest, "Choose valid alerts."}
					break
				}
				id := storage.AlertKey(repository, number)
				if !slices.Contains(ids, id) {
					ids = append(ids, id)
				}
			}
			if failure == nil {
				failure = s.setAlertsAccepted(r.Context(), ids)
			}
		}
	}
	s.acceptanceResponse(w, r, failure, ids)
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
	s.acceptanceResponse(w, r, failure, nil)
}

func (s *Server) acceptanceResponse(w http.ResponseWriter, r *http.Request, failure *requestError, selected []storage.AlertID) {
	if failure != nil {
		p := s.currentPage(r)
		p.Error = failure.message
		for i := range p.Rows {
			row := &p.Rows[i]
			row.Selected = row.CanAccept() && slices.Contains(selected, storage.AlertKey(row.Repository.FullName, row.Number))
		}
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
	if accept {
		return s.setAlertsAccepted(ctx, []storage.AlertID{id})
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	state := s.state
	state.mu.Lock()
	defer state.mu.Unlock()
	found := false
	for i := range state.result.Alerts {
		alert := &state.result.Alerts[i]
		if storage.AlertKey(alert.Repository.FullName, alert.Number) == id {
			found = true
			break
		}
	}
	if !found {
		return &requestError{http.StatusNotFound, "Alert not found in the current results. Refresh alerts and try again."}
	}
	if err := s.store.ReopenAlert(ctx, id); err != nil {
		return &requestError{http.StatusInternalServerError, "Could not reopen accepted alert in SQLite."}
	}
	delete(state.accepted, id)
	return nil
}

func (s *Server) setAlertsAccepted(ctx context.Context, ids []storage.AlertID) *requestError {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	state := s.state
	state.mu.Lock()
	defer state.mu.Unlock()
	states := make(map[storage.AlertID]string, len(state.result.Alerts))
	for _, alert := range state.result.Alerts {
		states[storage.AlertKey(alert.Repository.FullName, alert.Number)] = alert.State
	}
	// Validate the entire selection before writing any decisions.
	for _, id := range ids {
		status, found := states[id]
		if !found {
			return &requestError{http.StatusNotFound, "Alert not found in the current results. Refresh alerts and try again."}
		}
		if status != "open" {
			return &requestError{http.StatusConflict, "Only open alerts can be accepted."}
		}
	}
	accepted, err := s.store.AcceptAlerts(ctx, ids)
	if err != nil {
		return &requestError{http.StatusInternalServerError, "Could not save accepted alert selection to SQLite."}
	}
	for id, at := range accepted {
		state.accepted[id] = at
	}
	return nil
}
