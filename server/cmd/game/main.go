package main

import (
	"log"
	"net/http"
	"os"
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
	api := NewAPI()

	server := &http.Server{
		Addr:              addr(),
		Handler:           api.Routes(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	log.Printf("wordarena game service listening on %s", server.Addr)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
