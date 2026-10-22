package server

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
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
	github   GitHub
	sessions sessions
}

func New(client GitHub, secureCookies bool) http.Handler {
	s := &Server{github: client, sessions: sessions{entries: make(map[string]sessionEntry), secure: secureCookies}}
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
	assets, _ := fs.Sub(web.Assets, "dist")
	files := http.FileServerFS(assets)
	r.Get("/*", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			if _, err := fs.Stat(assets, "index.html"); err != nil {
				http.Error(w, "Frontend not built. Run pnpm install and pnpm build, then restart dbaui.", http.StatusServiceUnavailable)
				return
			}
			w.Header().Set("Cache-Control", "no-cache")
		}
		files.ServeHTTP(w, r)
	})
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
	var input struct {
		Label         string   `json:"label"`
		Token         string   `json:"token"`
		Accounts      []string `json:"accounts"`
		Organizations []string `json:"organizations"`
	}
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
	input.Label = strings.TrimSpace(input.Label)
	input.Token = strings.TrimSpace(input.Token)
	if len(input.Label) > 80 || len(input.Token) < 1 || len(input.Token) > 512 || strings.ContainsAny(input.Token, " \r\n\t") || len(input.Accounts)+len(input.Organizations) > 20 {
		writeError(w, http.StatusBadRequest, "Provide a token, a label of at most 80 characters, and at most 20 accounts and organizations.")
		return
	}
	accounts, err := normalizeOwners(input.Accounts)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	orgs, err := normalizeOwners(input.Organizations)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	for _, account := range accounts {
		for _, org := range orgs {
			if strings.EqualFold(account, org) {
				writeError(w, http.StatusBadRequest, "A source must be either a personal account or an organization.")
				return
			}
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	login, err := s.github.Login(ctx, input.Token)
	if err != nil {
		status := http.StatusBadGateway
		var apiError *github.APIError
		if errors.As(err, &apiError) && (apiError.Status == 401 || apiError.Status == 403) {
			status = http.StatusUnprocessableEntity
		}
		writeError(w, status, err.Error())
		return
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
		writeError(w, http.StatusConflict, "A session supports up to 10 connections.")
		return
	}
	session.connections = append(session.connections, conn)
	writeJSON(w, http.StatusCreated, conn)
}

func (s *Server) deleteConnection(w http.ResponseWriter, r *http.Request) {
	session := s.sessions.get(w, r)
	if session == nil {
		return
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	for i, conn := range session.connections {
		if conn.ID == chi.URLParam(r, "id") {
			session.connections = slices.Delete(session.connections, i, i+1)
			w.WriteHeader(http.StatusNoContent)
			return
		}
	}
	writeError(w, http.StatusNotFound, "Connection not found.")
}

func (s *Server) alerts(w http.ResponseWriter, r *http.Request) {
	session := s.sessions.get(w, r)
	if session == nil {
		return
	}
	session.mu.Lock()
	if session.refreshing {
		session.mu.Unlock()
		writeError(w, http.StatusConflict, "A refresh is already running. Wait for it to finish.")
		return
	}
	session.refreshing = true
	connections := append([]github.Connection{}, session.connections...)
	session.mu.Unlock()
	defer func() { session.mu.Lock(); session.refreshing = false; session.mu.Unlock() }()
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	writeJSON(w, http.StatusOK, s.github.Fetch(ctx, connections))
}
