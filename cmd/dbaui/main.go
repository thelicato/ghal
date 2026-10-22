package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"dbaui/internal/github"
	"dbaui/internal/server"
	"dbaui/internal/storage"
)

func main() {
	addr := flag.String("addr", envOr("DBAUI_ADDR", "127.0.0.1:8080"), "HTTP listen address")
	database := flag.String("db", envOr("DBAUI_DATABASE", "data/dbaui.db"), "SQLite database path")
	keyFile := flag.String("key-file", os.Getenv("DBAUI_KEY_FILE"), "encryption key file (default: database path + .key)")
	healthcheck := flag.Bool("healthcheck", false, "check the running server and exit")
	flag.Parse()
	if *healthcheck {
		if err := checkHealth(*addr); err != nil {
			log.Fatal(err)
		}
		return
	}
	store, err := storage.Open(*database, *keyFile)
	if err != nil {
		log.Fatal(err)
	}
	defer store.Close()
	handler, err := server.New(github.NewClient(), store)
	if err != nil {
		log.Fatal(err)
	}
	httpServer := &http.Server{
		Addr: *addr, Handler: handler,
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second,
		WriteTimeout: 6 * time.Minute, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	done := make(chan struct{})
	go func() {
		defer close(done)
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			log.Printf("Shutdown: %v", err)
		}
	}()
	log.Printf("dbaui (DependaBot Alerts UI) listening on %s", *addr)
	if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
	stop()
	<-done
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func checkHealth(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return errors.New("invalid healthcheck address")
	}
	if host == "" || host == "0.0.0.0" {
		host = "127.0.0.1"
	}
	if host == "::" {
		host = "::1"
	}
	u := url.URL{Scheme: "http", Host: net.JoinHostPort(host, port), Path: "/api/health"}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(u.String())
	if err != nil {
		return errors.New("dbaui healthcheck failed: server unreachable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return errors.New("dbaui healthcheck failed: server or SQLite unavailable")
	}
	return nil
}
