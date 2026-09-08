package main

import (
	"log"
	"net/http"
	"os"
	"time"
)

// addr returns the listen address for the game service.
//
// Resolution order:
//  1. WORDARENA_ADDR environment variable (used by orchestrators to place the
//     service on an isolated port, e.g. 127.0.0.1:18080);
//  2. default ":8080" for local development.
//
// The game service binds wherever it is told; competitive protocol
// contracts live in /proto and are transport-agnostic.
func addr() string {
	if v := os.Getenv("WORDARENA_ADDR"); v != "" {
		return v
	}
	return ":8080"
}

func healthzHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	return mux
}

func main() {
	server := &http.Server{
		Addr:              addr(),
		Handler:           healthzHandler(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	log.Printf("wordarena game service listening on %s", server.Addr)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
