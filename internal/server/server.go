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
	"time"

	"dbaui/internal/github"
	"dbaui/web"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

type GitHub interface {
	Login(context.Context, string) (string, error)
	Fetch(context.Context, []github.Connection) github.Result
}

type Server struct {
	github    GitHub
	sessions  sessions
	templates *template.Template
}

func New(client GitHub, secureCookies bool) http.Handler {
	s := &Server{
		github:    client,
		sessions:  sessions{entries: make(map[string]sessionEntry), secure: secureCookies},
		templates: template.Must(template.New("pages").Funcs(templateFunctions()).ParseFS(web.Assets, "templates/*.html")),
	}
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
		r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		})
		r.Get("/connections", s.listConnections)
		r.Post("/connections", s.addConnection)
		r.Delete("/connections/{id}", s.deleteConnection)
		r.Get("/alerts", s.alerts)
		r.NotFound(func(w http.ResponseWriter, r *http.Request) {
			writeError(w, http.StatusNotFound, "API endpoint not found.")
		})
	})
	assets, _ := fs.Sub(web.Assets, "static")
	r.Handle("/static/*", http.StripPrefix("/static/", http.FileServerFS(assets)))
	r.Get("/", s.overviewPage)
	r.Get("/connections", s.connectionsPage)
	r.Post("/connections", s.createConnectionPage)
	r.Post("/connections/{id}/delete", s.removeConnectionPage)
	r.Post("/refresh", s.refreshPage)
	r.Get("/alerts.csv", s.exportCSV)
	r.Get("/demo", s.demoPage)
	r.Get("/demo/alerts.csv", s.exportDemoCSV)
	return r
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
	session := s.sessions.get(w, r)
	if session == nil {
		return
	}
	writeJSON(w, http.StatusOK, session.snapshot())
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
	session := s.sessions.get(w, r)
	if session == nil {
		return
	}
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
	conn, failure := s.createConnection(r.Context(), session, input)
	if failure != nil {
		writeError(w, failure.status, failure.message)
		return
	}
	writeJSON(w, http.StatusCreated, conn)
}

func (s *Server) createConnection(ctx context.Context, session *session, input connectionInput) (github.Connection, *requestError) {
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
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	login, err := s.github.Login(ctx, input.Token)
	if err != nil {
		status := http.StatusBadGateway
		var apiError *github.APIError
		if errors.As(err, &apiError) && (apiError.Status == 401 || apiError.Status == 403) {
			status = http.StatusUnprocessableEntity
		}
		return fail(status, err.Error())
	}
	if input.Label == "" {
		input.Label = login
	}
	if len(accounts) == 0 && len(orgs) == 0 {
		accounts = append(accounts, login)
	}
	conn := github.Connection{ID: rand.Text(), Label: input.Label, Login: login, Accounts: accounts, Organizations: orgs, Token: input.Token}
	session.mu.Lock()
	defer session.mu.Unlock()
	if len(session.connections) >= 10 {
		return fail(http.StatusConflict, "A session supports up to 10 connections.")
	}
	session.connections = append(session.connections, conn)
	session.invalidateLocked()
	return conn, nil
}

func (s *Server) deleteConnection(w http.ResponseWriter, r *http.Request) {
	session := s.sessions.get(w, r)
	if session == nil {
		return
	}
	if !removeConnection(session, chi.URLParam(r, "id")) {
		writeError(w, http.StatusNotFound, "Connection not found.")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func removeConnection(session *session, id string) bool {
	session.mu.Lock()
	defer session.mu.Unlock()
	for i, conn := range session.connections {
		if conn.ID == id {
			session.connections = slices.Delete(session.connections, i, i+1)
			session.invalidateLocked()
			return true
		}
	}
	return false
}

func (s *Server) alerts(w http.ResponseWriter, r *http.Request) {
	session := s.sessions.get(w, r)
	if session == nil {
		return
	}
	result, failure := s.refreshSession(r.Context(), session)
	if failure != nil {
		writeError(w, failure.status, failure.message)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) refreshSession(ctx context.Context, session *session) (github.Result, *requestError) {
	session.mu.Lock()
	if session.refreshing {
		session.mu.Unlock()
		return github.Result{}, &requestError{http.StatusConflict, "A refresh is already running. Wait for it to finish."}
	}
	session.refreshing = true
	connections := append([]github.Connection{}, session.connections...)
	revision := session.revision
	session.mu.Unlock()
	defer func() { session.mu.Lock(); session.refreshing = false; session.mu.Unlock() }()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	result := s.github.Fetch(ctx, connections)
	if result.FetchedAt.IsZero() {
		result.FetchedAt = time.Now().UTC()
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.revision != revision {
		return github.Result{}, &requestError{http.StatusConflict, "Connections changed during the refresh. Refresh alerts again."}
	}
	session.result = result
	session.loaded = true
	return result, nil
}
