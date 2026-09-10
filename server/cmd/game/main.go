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

	server := &http.Server{
		Addr:              addr(),
		Handler:           api.Routes(),
		ReadHeaderTimeout: 5 * time.Second,
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
