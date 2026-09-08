package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func TestHealthz(t *testing.T) {
	srv := httptest.NewServer(healthzHandler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	ct := resp.Header.Get("Content-Type")
	if ct != "application/json" {
		t.Fatalf("content-type = %q, want application/json", ct)
	}
}

func TestAddrDefault(t *testing.T) {
	os.Unsetenv("WORDARENA_ADDR")
	if got, want := addr(), ":8080"; got != want {
		t.Fatalf("addr() = %q, want %q", got, want)
	}
}

func TestAddrFromEnv(t *testing.T) {
	t.Setenv("WORDARENA_ADDR", "127.0.0.1:18080")
	if got, want := addr(), "127.0.0.1:18080"; got != want {
		t.Fatalf("addr() = %q, want %q", got, want)
	}
}
