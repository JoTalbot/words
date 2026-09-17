package main

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestStartPProfLoopbackOnly(t *testing.T) {
	for _, addr := range []string{"0.0.0.0:0", "192.168.1.10:6060", "[::]:6060", "example.org:6060"} {
		if ln, err := startPProf(addr); err == nil {
			ln.Close()
			t.Errorf("startPProf(%q) succeeded; non-loopback profiling listeners are refused by policy", addr)
		}
	}
}

func TestStartPProfServesHandlers(t *testing.T) {
	ln, err := startPProf("127.0.0.1:0")
	if err != nil {
		t.Fatalf("loopback start: %v", err)
	}
	defer ln.Close()
	base := "http://" + ln.Addr().String()
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp, err := http.Get(base + "/debug/pprof/cmdline")
		if err == nil {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("cmdline status %d", resp.StatusCode)
			}
			if len(body) == 0 {
				t.Fatal("cmdline body empty")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("listener never answered: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	resp, err := http.Get(base + "/debug/pprof/")
	if err != nil {
		t.Fatalf("index: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "goroutine") {
		t.Fatalf("index status %d, body %q", resp.StatusCode, body[:min(80, len(body))])
	}
}
