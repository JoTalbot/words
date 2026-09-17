package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// addr returns the listen address (WORDARENA_ADDR env override).
func addr() string {
	if v := os.Getenv("WORDARENA_ADDR"); v != "" {
		return v
	}
	return ":8080"
}

func main() {
	var (
		api *API
		err error
	)
	if dsn := os.Getenv("WORDARENA_POSTGRES_DSN"); dsn != "" {
		api, err = NewAPIWithPostgres(dsn)
		if err != nil {
			log.Fatalf("postgres init failed: %v", err)
		}
		log.Printf("durable storage: postgres enabled")
	} else {
		api = NewAPI()
	}

	// Timeouts bound the slow-loris style exposure of the listener. A
	// WebSocket upgrade hijacks its connection, so the write deadline does
	// not apply to a live match stream; it only bounds ordinary responses.
	server := &http.Server{
		Addr:              addr(),
		Handler:           api.Routes(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	// Opt-in profiling (batch 37D): only when WORDARENA_PPROF_ADDR is set,
	// loopback-only, on its own listener and mux - never on the public one.
	// A startup failure here must not take the game down with it.
	if pp := os.Getenv("WORDARENA_PPROF_ADDR"); pp != "" {
		if _, err := startPProf(pp); err != nil {
			log.Printf("pprof disabled: %v", err)
		}
	}

	serveErr := make(chan error, 1)
	go func() {
		log.Printf("wordarena game service listening on %s", server.Addr)
		serveErr <- server.ListenAndServe()
	}()

	// Graceful shutdown: stop accepting work, stop room tickers, drain
	// in-flight requests within a bounded window, then exit.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	select {
	case err := <-serveErr:
		if err != nil && err != http.ErrServerClosed {
			log.Fatalf("serve: %v", err)
		}
	case s := <-sig:
		log.Printf("signal %s received: shutting down", s)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		api.Stop()
		if err := server.Shutdown(ctx); err != nil {
			log.Printf("shutdown: %v", err)
		}
	}
	log.Print("wordarena game service stopped")
}
