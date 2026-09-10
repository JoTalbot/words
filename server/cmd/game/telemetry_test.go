package main

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	wordarenav1 "github.com/JoTalbot/words/server/gen/wordarena/net/v1"
)

func TestPrometheusMetricsEndpoint(t *testing.T) {
	api := NewAPI()
	defer api.Stop()
	srv := httptest.NewServer(api.Routes())
	defer srv.Close()

	seed := uint64(e2eSeed)
	id, tokens, _ := createMatch(t, srv, "en", &seed)
	c0 := dial(t, srv, id, tokens[0])
	defer c0.close()
	_ = c0.readSnapshot(t)

	c0.submit(t, id, []uint32{9, 1, 0}, 17)
	if ev := c0.readWordEvent(t); ev.Result != wordarenav1.WordResult_ACCEPTED {
		t.Fatalf("cat result = %v", ev.Result)
	}
	c0.submit(t, id, []uint32{0, 1, 2}, 18)
	if ev := c0.readWordEvent(t); ev.Result == wordarenav1.WordResult_ACCEPTED {
		t.Fatalf("taa unexpectedly accepted")
	}

	resp, err := http.Get(srv.URL + "/metrics/prometheus")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("prometheus status = %d", resp.StatusCode)
	}
	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(ct, "text/plain") {
		t.Fatalf("content-type = %q, want text/plain", ct)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, want := range []string{
		"# TYPE wordarena_matches_created_total counter",
		"wordarena_matches_created_total 1",
		"wordarena_intents_received_total 2",
		"wordarena_words_accepted_total 1",
		"wordarena_words_rejected_total 1",
		"# TYPE wordarena_active_matches gauge",
		"wordarena_telemetry_events_dropped_total 0",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("prometheus body missing %q:\n%s", want, text)
		}
	}
}

func TestTelemetryJSONLExport(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	t.Setenv("WORDARENA_TELEMETRY_JSONL", path)
	t.Setenv("WORDARENA_TELEMETRY_BUFFER", "64")

	api := NewAPI()
	srv := httptest.NewServer(api.Routes())
	defer srv.Close()

	seed := uint64(e2eSeed)
	id, tokens, _ := createMatch(t, srv, "en", &seed)
	c0 := dial(t, srv, id, tokens[0])
	_ = c0.readSnapshot(t)
	c0.submit(t, id, []uint32{9, 1, 0}, 7)
	if ev := c0.readWordEvent(t); ev.Result != wordarenav1.WordResult_ACCEPTED {
		t.Fatalf("cat result = %v", ev.Result)
	}
	c0.close()

	waitForTelemetryWrites(t, api, 3)
	api.Stop()

	events := readTelemetryJSONL(t, path)
	if len(events) < 3 {
		t.Fatalf("telemetry events = %d, want >= 3: %#v", len(events), events)
	}

	created := findTelemetryEvent(events, "match_created")
	if created == nil {
		t.Fatalf("missing match_created in %#v", events)
	}
	if got := uint64(created["match_id"].(float64)); got != id {
		t.Fatalf("match_created id = %d, want %d", got, id)
	}
	if got := created["language"]; got != "en" {
		t.Fatalf("match_created language = %v", got)
	}

	validated := findTelemetryEvent(events, "word_validated")
	if validated == nil {
		t.Fatalf("missing word_validated in %#v", events)
	}
	if got := uint64(validated["match_id"].(float64)); got != id {
		t.Fatalf("word_validated id = %d, want %d", got, id)
	}
	if got := int(validated["seat"].(float64)); got != 0 {
		t.Fatalf("word_validated seat = %d, want 0", got)
	}
	if got := int(validated["client_sequence"].(float64)); got != 7 {
		t.Fatalf("word_validated client_sequence = %d, want 7", got)
	}
	if got := validated["word"]; got != "cat" {
		t.Fatalf("word_validated word = %v", got)
	}
	if got := validated["result"]; got != "accepted" {
		t.Fatalf("word_validated result = %v", got)
	}
	if got := int64(validated["score_added"].(float64)); got != 5 {
		t.Fatalf("word_validated score_added = %d, want 5", got)
	}

	stats := api.metricsSnapshot()
	if stats.TelemetryEventsDropped != 0 || stats.TelemetryExportErrors != 0 {
		t.Fatalf("telemetry stats dropped/errors = %d/%d", stats.TelemetryEventsDropped, stats.TelemetryExportErrors)
	}
}

func waitForTelemetryWrites(t *testing.T, api *API, want uint64) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if api.metricsSnapshot().TelemetryEventsWritten >= want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("telemetry writes = %d, want >= %d", api.metricsSnapshot().TelemetryEventsWritten, want)
}

func readTelemetryJSONL(t *testing.T, path string) []map[string]any {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var events []map[string]any
	s := bufio.NewScanner(f)
	for s.Scan() {
		var ev map[string]any
		if err := json.Unmarshal(s.Bytes(), &ev); err != nil {
			t.Fatalf("bad telemetry json %q: %v", s.Text(), err)
		}
		events = append(events, ev)
	}
	if err := s.Err(); err != nil {
		t.Fatal(err)
	}
	return events
}

func findTelemetryEvent(events []map[string]any, typ string) map[string]any {
	for _, ev := range events {
		if ev["type"] == typ {
			return ev
		}
	}
	return nil
}
