package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"dbaui/internal/github"
	"dbaui/internal/server"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8080", "HTTP listen address")
	secureCookies := flag.Bool("secure-cookies", false, "require HTTPS for session cookies when behind a TLS proxy")
	flag.Parse()
	httpServer := &http.Server{
		Addr: *addr, Handler: server.New(github.NewClient(), *secureCookies),
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
