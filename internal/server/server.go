package server

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"html/template"
	"io"
	"io/fs"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/thelicato/ghal/internal/github"
	"github.com/thelicato/ghal/internal/storage"
	"github.com/thelicato/ghal/internal/telegram"
	"github.com/thelicato/ghal/web"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

type GitHub interface {
	Login(context.Context, string) (string, error)
	Fetch(context.Context, []github.Connection) github.Result
}

type Messenger interface {
	Send(context.Context, string, string, string) error
}

type Server struct {
	github       GitHub
	state        *state
	store        *storage.Store
	templates    *template.Template
	router       http.Handler
	ctx          context.Context
	cancel       context.CancelFunc
	jobs         sync.WaitGroup
	closed       bool // Protected by state.mu.
	autoSyncWake chan struct{}
	telegram     Messenger
	messageWake  chan struct{}
}

func New(client GitHub, store *storage.Store) (*Server, error) {
	return newServer(client, store, telegram.NewClient())
}

func newServer(client GitHub, store *storage.Store, messenger Messenger) (*Server, error) {
	connections, err := store.List(context.Background())
	if err != nil {
		return nil, err
	}
	accepted, err := store.ListAcceptedAlerts(context.Background())
	if err != nil {
		return nil, err
	}
	interval, err := store.SyncInterval(context.Background())
	if err != nil {
		return nil, err
	}
	telegramConfig, err := store.TelegramConfig(context.Background())
	if err != nil {
		return nil, err
	}
	s := &Server{
		github:       client,
		state:        &state{connections: connections, accepted: accepted, syncInterval: interval, lastSyncFinished: time.Now().UTC(), telegram: telegramConfig},
		store:        store,
		autoSyncWake: make(chan struct{}, 1),
		telegram:     messenger,
		messageWake:  make(chan struct{}, 1),
		templates:    template.Must(template.New("pages").Funcs(templateFunctions()).ParseFS(web.Assets, "templates/*.html")),
	}
	s.ctx, s.cancel = context.WithCancel(context.Background())
	r := chi.NewRouter()
	r.Use(middleware.Recoverer)
	r.Use(securityHeaders)
	r.Use(http.NewCrossOriginProtection().Handler)
	r.Route("/api", func(r chi.Router) {
		r.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Cache-Control", "no-store")
				next.ServeHTTP(w, r)
			})
		})
		r.Get("/health", s.health)
		r.Get("/connections", s.listConnections)
		r.Post("/connections", s.addConnection)
		r.Delete("/connections/{id}", s.deleteConnection)
		r.Get("/alerts", s.alerts)
		r.Get("/status", s.syncStatus)
		r.Get("/telegram/status", s.telegramStatusAPI)
		r.Post("/refresh", s.refreshAPI)
		r.NotFound(func(w http.ResponseWriter, r *http.Request) {
			writeError(w, http.StatusNotFound, "API endpoint not found.")
		})
	})
	assets, _ := fs.Sub(web.Assets, "static")
	r.Handle("/static/*", http.StripPrefix("/static/", http.FileServerFS(assets)))
	r.Get("/", s.overviewPage)
	r.Get("/connections", s.connectionsPage)
	r.Get("/settings", s.settingsPage)
	r.Post("/settings", s.saveSettingsPage)
	r.Post("/settings/telegram", s.saveTelegramPage)
	r.Post("/settings/telegram/test", s.testTelegramPage)
	r.Post("/settings/telegram/disconnect", s.disconnectTelegramPage)
	r.Post("/connections", s.createConnectionPage)
	r.Post("/connections/{id}/delete", s.removeConnectionPage)
	r.Post("/refresh", s.refreshPage)
	r.Post("/alerts/accept", s.acceptAlertPage)
	r.Post("/alerts/accept-selected", s.acceptSelectedAlertsPage)
	r.Post("/alerts/reopen", s.reopenAlertPage)
	r.Get("/alerts.csv", s.exportCSV)
	r.Get("/demo", s.demoPage)
	r.Get("/demo/alerts.csv", s.exportDemoCSV)
	s.router = r
	if len(connections) > 0 {
		s.requestRefresh()
	}
	s.jobs.Add(1)
	go s.autoSyncLoop()
	s.jobs.Add(1)
	go s.messageLoop()
	return s, nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.router.ServeHTTP(w, r) }

// Close cancels background jobs and waits before SQLite can be closed.
func (s *Server) Close() {
	s.state.mu.Lock()
	s.closed = true
	s.state.messagePending, s.state.testPending = nil, nil
	s.cancel()
	s.state.mu.Unlock()
	s.jobs.Wait()
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func (s *Server) listConnections(w http.ResponseWriter, r *http.Request) {
	state := s.state
	writeJSON(w, http.StatusOK, state.snapshot())
}

var ownerPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9-]{0,38}$`)

func normalizeOwners(values []string) ([]string, error) {
	result := make([]string, 0, len(values))
	seen := make(map[string]bool)
	for _, value := range values {
		value = strings.TrimSpace(value)
		if !ownerPattern.MatchString(value) {
			return nil, errors.New("Use GitHub account or organization names, without URLs or @ prefixes.")
		}
		key := strings.ToLower(value)
		if !seen[key] {
			result = append(result, value)
			seen[key] = true
		}
	}
	return result, nil
}

type connectionInput struct {
	Label         string   `json:"label"`
	Token         string   `json:"token"`
	Accounts      []string `json:"accounts"`
	Organizations []string `json:"organizations"`
}

type requestError struct {
	status  int
	message string
}

func (s *Server) addConnection(w http.ResponseWriter, r *http.Request) {
	if media := strings.Split(r.Header.Get("Content-Type"), ";")[0]; media != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, "Send the connection as JSON.")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	defer r.Body.Close()
	var input connectionInput
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "Enter a valid connection.")
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeError(w, http.StatusBadRequest, "Send a single JSON object.")
		return
	}
	conn, failure := s.createConnection(r.Context(), input)
	if failure != nil {
		writeError(w, failure.status, failure.message)
		return
	}
	writeJSON(w, http.StatusCreated, conn)
}

func (s *Server) createConnection(ctx context.Context, input connectionInput) (github.Connection, *requestError) {
	fail := func(status int, message string) (github.Connection, *requestError) {
		return github.Connection{}, &requestError{status, message}
	}
	input.Label = strings.TrimSpace(input.Label)
	input.Token = strings.TrimSpace(input.Token)
	if len(input.Label) > 80 || len(input.Token) < 1 || len(input.Token) > 512 || strings.ContainsAny(input.Token, " \r\n\t") || len(input.Accounts)+len(input.Organizations) > 20 {
		return fail(http.StatusBadRequest, "Provide a token, a label of at most 80 characters, and at most 20 accounts and organizations.")
	}
	accounts, err := normalizeOwners(input.Accounts)
	if err != nil {
		return fail(http.StatusBadRequest, err.Error())
	}
	orgs, err := normalizeOwners(input.Organizations)
	if err != nil {
		return fail(http.StatusBadRequest, err.Error())
	}
	for _, account := range accounts {
		for _, org := range orgs {
			if strings.EqualFold(account, org) {
				return fail(http.StatusBadRequest, "A source must be either a personal account or an organization.")
			}
		}
	}
	// Save first. Token verification and source discovery belong to the background job.
	conn := github.Connection{ID: rand.Text(), Label: input.Label, Accounts: accounts, Organizations: orgs, Token: input.Token}
	state := s.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if len(state.connections) >= 10 {
		return fail(http.StatusConflict, "A deployment supports up to 10 connections.")
	}
	if err := s.store.Add(ctx, conn); err != nil {
		return fail(http.StatusInternalServerError, "Could not save connection to SQLite.")
	}
	state.connections = append(state.connections, conn)
	state.invalidateLocked()
	s.startRefreshLocked()
	return conn, nil
}

func (s *Server) deleteConnection(w http.ResponseWriter, r *http.Request) {
	if failure := s.removeConnection(r.Context(), chi.URLParam(r, "id")); failure != nil {
		writeError(w, failure.status, failure.message)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) removeConnection(ctx context.Context, id string) *requestError {
	state := s.state
	state.mu.Lock()
	defer state.mu.Unlock()
	for i, conn := range state.connections {
		if conn.ID == id {
			if err := s.store.Delete(ctx, id); err != nil {
				return &requestError{http.StatusInternalServerError, "Could not delete connection from SQLite."}
			}
			state.connections = slices.Delete(state.connections, i, i+1)
			state.invalidateLocked()
			s.startRefreshLocked()
			return nil
		}
	}
	return &requestError{http.StatusNotFound, "Connection not found."}
}

func (s *Server) alerts(w http.ResponseWriter, r *http.Request) {
	s.state.mu.Lock()
	result := s.state.result
	s.state.mu.Unlock()
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.store.Ping(ctx); err != nil {
		writeError(w, http.StatusServiceUnavailable, "SQLite is unavailable.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
