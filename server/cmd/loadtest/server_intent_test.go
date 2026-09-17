package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// Batch 39C: the harness must carry the server-side intent processing
// summary out of GET /metrics, so a leg's report can decompose the
// client-observed round trip into path latency and server work.

func TestServerSnapshotExtractsIntentProcess(t *testing.T) {
	body := `{
	  "active_matches": 2,
	  "intent_process_us": {
	    "count": 100,
	    "sum_us": 42000,
	    "overflow_over_10ms": 0,
	    "buckets": {
	      "le_50": 10, "le_100": 30, "le_250": 70, "le_500": 90,
	      "le_1000": 96, "le_2500": 99, "le_5000": 100,
	      "le_10000": 100, "le_inf": 100
	    }
	  }
	}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz":
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case "/metrics":
			_, _ = w.Write([]byte(body))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	_, _, active, ip := serverSnapshot(srv.URL)
	if active != 2 {
		t.Fatalf("active matches: want 2, got %d", active)
	}
	if ip.Count != 100 || ip.SumUS != 42000 {
		t.Fatalf("count/sum: want 100/42000, got %d/%d", ip.Count, ip.SumUS)
	}
	if ip.MeanUS != 420 {
		t.Fatalf("mean: want 420, got %.1f", ip.MeanUS)
	}
	// 95% of 100 needs cumulative >= 95: le_1000 covers 96 -> floor 1000 us.
	if ip.P95US != 1000 {
		t.Fatalf("p95 floor: want 1000, got %d", ip.P95US)
	}
}

func TestServerSnapshotZeroHistogramIsOmitted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"active_matches":0,"intent_process_us":{"count":0,"sum_us":0,"buckets":{}}}`))
	}))
	defer srv.Close()

	_, _, _, ip := serverSnapshot(srv.URL)
	if ip.Count != 0 || ip.MeanUS != 0 || ip.P95US != 0 {
		t.Fatalf("empty histogram must stay zeroed, got %+v", ip)
	}
}
