package main

// Hardening tests: resource limits, result persistence endpoint, and
// oversized WebSocket frame rejection (DoS protection). These cover the
// production-shape additions to cmd/game without touching match rules.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	wordarenav1 "github.com/JoTalbot/words/server/gen/wordarena/net/v1"
)

func TestRoomCapEnforced(t *testing.T) {
	api := NewAPI()
	api.maxRooms = 1
	srv := httptest.NewServer(api.Routes())
	defer srv.Close()

	seed := uint64(1)
	createMatch(t, srv, "en", &seed) // occupies the only room slot

	resp, err := http.Post(srv.URL+"/v1/matches", "application/json",
		strings.NewReader(`{"language":"en"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("second create status = %d, want 429", resp.StatusCode)
	}
}

func TestResultEndpointAfterMatch(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping result-persistence transport test in -short mode")
	}
	api := NewAPI()
	srv := httptest.NewServer(api.Routes())
	defer srv.Close()

	seed := uint64(1512)
	moves, want, solvable := egSolve(t, "en", seed)
	if !solvable {
		t.Fatalf("seed %d not fully claimable", seed)
	}

	id, tokens, userIDs := createMatch(t, srv, "en", &seed)
	c0 := dial(t, srv, id, tokens[0])
	defer c0.close()
	c1 := dial(t, srv, id, tokens[1])
	defer c1.close()
	c0.seat, c0.user = 0, userIDs[0]
	c1.seat, c1.user = 1, userIDs[1]

	// Drain the immediate canonical snapshots.
	_ = c0.readSnapshot(t)
	_ = c1.readSnapshot(t)

	bots := []*testClient{c0, c1}
	users := [2]uint64{userIDs[0], userIDs[1]}
	lastWave := -1
	for _, mv := range moves {
		if mv.wave != lastWave {
			egReadUntilWave(t, c0, uint32(mv.wave))
			lastWave = mv.wave
		}
		b := bots[mv.seat]
		u := users[mv.seat]
		b.submit(t, id, mv.ids, mv.seq)
		_ = egReadEventFor(t, c0, u, mv.seq)
		_ = egReadEventFor(t, c1, u, mv.seq)
	}

	// Terminal over snapshot proves the match finished.
	egReadUntilOver(t, c0)

	// The result must be served (recorded by the ticker on match end).
	var res matchResult
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp, err := http.Get(fmt.Sprintf("%s/v1/matches/%d/result", srv.URL, id))
		if err == nil {
			if resp.StatusCode == http.StatusOK {
				_ = json.NewDecoder(resp.Body).Decode(&res)
				resp.Body.Close()
				break
			}
			resp.Body.Close()
		}
		if time.Now().After(deadline) {
			t.Fatal("result not available within 5 s of match end")
		}
		time.Sleep(50 * time.Millisecond)
	}

	if !res.Over {
		t.Fatalf("result.Over = false, want true")
	}
	if res.Scores != want {
		t.Fatalf("result scores = %d:%d, offline replay wants %d:%d",
			res.Scores[0], res.Scores[1], want[0], want[1])
	}
	if res.MatchID != id || res.Seed != seed || res.Language != "en" {
		t.Fatalf("result metadata mismatch: %+v", res)
	}
	if res.StateVer == 0 || res.ServerTick == 0 {
		t.Fatalf("result missing version/tick: %+v", res)
	}
	t.Logf("result endpoint ok: winner=%d tie=%v scores=%d:%d ticks=%d",
		res.WinnerSeat, res.IsTie, res.Scores[0], res.Scores[1], res.ServerTick)
}

func TestResultEndpointUnknownMatch(t *testing.T) {
	api := NewAPI()
	srv := httptest.NewServer(api.Routes())
	defer srv.Close()

	resp, err := http.Get(fmt.Sprintf("%s/v1/matches/424242/result", srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown result status = %d, want 404", resp.StatusCode)
	}
}

func TestMetricsCounters(t *testing.T) {
	api := NewAPI()
	srv := httptest.NewServer(api.Routes())
	defer srv.Close()

	seed := uint64(e2eSeed)
	id, tokens, _ := createMatch(t, srv, "en", &seed)
	c0 := dial(t, srv, id, tokens[0])
	defer c0.close()
	_ = c0.readSnapshot(t)

	// Accepted word "cat" (board seed 1512: cells 9,1,0).
	c0.submit(t, id, []uint32{9, 1, 0}, 1)
	if ev := c0.readWordEvent(t); ev.Result != wordarenav1.WordResult_ACCEPTED {
		t.Fatalf("cat result = %v", ev.Result)
	}
	// Rejected nonsense "taa".
	c0.submit(t, id, []uint32{0, 1, 2}, 2)
	if ev := c0.readWordEvent(t); ev.Result == wordarenav1.WordResult_ACCEPTED {
		t.Fatalf("taa unexpectedly accepted")
	}

	resp, err := http.Get(srv.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("metrics status = %d", resp.StatusCode)
	}
	var m map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		t.Fatal(err)
	}
	check := func(k string, wantMin float64) {
		v, ok := m[k].(float64)
		if !ok || v < wantMin {
			t.Fatalf("metric %s = %v, want >= %v", k, m[k], wantMin)
		}
	}
	check("matches_created", 1)
	check("intents_received", 2)
	// The batch 35C intent histogram must appear on the JSON surface with the
	// same observation count as intents_received (the prometheus surface is
	// asserted separately in telemetry_test.go).
	if h, ok := m["intent_process_us"].(map[string]any); !ok {
		t.Fatalf("intent_process_us histogram missing from /metrics: %v", m["intent_process_us"])
	} else if c, ok := h["count"].(float64); !ok || c != 2 {
		t.Fatalf("intent_process_us.count = %v, want 2", h["count"])
	}
	check("words_accepted", 1)
	check("words_rejected", 1)
	check("active_matches", 1)
}

func TestResultsReaperRemovesStaleEntries(t *testing.T) {
	api := NewAPI()
	stale := matchResult{MatchID: 1, recordedAt: time.Now().Add(-10 * time.Minute)}
	fresh := matchResult{MatchID: 2, recordedAt: time.Now()}
	api.resultsMu.Lock()
	api.results[1] = stale
	api.results[2] = fresh
	api.resultsMu.Unlock()

	api.reapResults()

	api.resultsMu.Lock()
	defer api.resultsMu.Unlock()
	if _, ok := api.results[1]; ok {
		t.Fatal("stale result not removed")
	}
	if _, ok := api.results[2]; !ok {
		t.Fatal("fresh result wrongly removed")
	}
}

func TestIntentRateLimitClosesConnection(t *testing.T) {
	api := NewAPI()
	api.intentsPerSec = 3
	srv := httptest.NewServer(api.Routes())
	defer srv.Close()

	seed := uint64(1)
	id, tokens, _ := createMatch(t, srv, "en", &seed)
	c := dial(t, srv, id, tokens[0])
	defer c.close()
	_ = c.readSnapshot(t)

	// Three intents fit in the per-second budget; the fourth is refused.
	for i := uint32(1); i <= 3; i++ {
		c.submit(t, id, []uint32{0, 1, 2}, i)
	}
	c.submit(t, id, []uint32{0, 1, 2}, 4)

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, _, err := c.conn.Read(t.Context()); err != nil {
			return // expected: server closed the connection
		}
	}
	t.Fatal("connection stayed open past the intent rate limit")
}

func TestIntentRateLimitDisabledWhenZero(t *testing.T) {
	api := NewAPI()
	api.intentsPerSec = 0
	srv := httptest.NewServer(api.Routes())
	defer srv.Close()

	seed := uint64(1)
	id, tokens, _ := createMatch(t, srv, "en", &seed)
	c := dial(t, srv, id, tokens[0])
	defer c.close()
	_ = c.readSnapshot(t)

	// With the limiter disabled, several intents in a burst must be accepted.
	for i := uint32(1); i <= 5; i++ {
		c.submit(t, id, []uint32{0, 1, 2}, i)
	}
	if ev := c.readWordEvent(t); ev == nil {
		t.Fatal("expected a word event when limiter is disabled")
	}
}

func TestWSRejectsOversizedFrame(t *testing.T) {
	api := NewAPI()
	api.maxWSBytes = 4096
	srv := httptest.NewServer(api.Routes())
	defer srv.Close()

	seed := uint64(1)
	id, tokens, _ := createMatch(t, srv, "en", &seed)
	c := dial(t, srv, id, tokens[0])
	defer c.close()
	_ = c.readSnapshot(t)

	// 5000 indices serialize far above the 4 KiB read limit.
	big := make([]uint32, 5000)
	for i := range big {
		big[i] = uint32(i % 12)
	}
	c.submit(t, id, big, 1)

	// The server must terminate the connection after the oversized frame.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		_, _, err := c.conn.Read(t.Context())
		if err != nil {
			return // expected: connection closed by the server
		}
	}
	t.Fatal("connection stayed open after oversized frame")
}

// TestIntentWindowIsKeyedByMatchAndSeat pins the key of the per-seat intent
// budget. It used to be the packed integer matchID<<1|seat, which only two
// seats can share: match 1 seat 2 packed to the same key as match 2 seat 0, so
// one match's activity spent another match's budget. The case is written at
// seat 2 deliberately - that is the first seat the old packing aliased.
func TestIntentWindowIsKeyedByMatchAndSeat(t *testing.T) {
	api := NewAPI()
	api.intentsPerSec = 3

	for i := 1; i <= 3; i++ {
		if !api.allowIntent(1, 2) {
			t.Fatalf("intent %d on match 1 seat 2 was refused inside its own budget", i)
		}
	}
	if api.allowIntent(1, 2) {
		t.Fatal("match 1 seat 2 exceeded its own per-second budget")
	}
	// The aliased pair from the old packing: (1, 2) and (2, 0).
	if !api.allowIntent(2, 0) {
		t.Error("match 2 seat 0 was throttled by match 1 seat 2; the window key still collides")
	}
	// Neighbours inside the same match keep separate budgets too.
	if !api.allowIntent(1, 3) {
		t.Error("match 1 seat 3 was throttled by match 1 seat 2; seats share one window")
	}
}

// TestClearSeatWindowsReleasesEverySeat covers the second half of the same
// defect: cleanup named only seats 0 and 1, so every higher seat kept its
// timestamps in the map for the lifetime of the process. The assertion is at a
// high seat number on purpose - a 60-seat roster is a shipped configuration.
func TestClearSeatWindowsReleasesEverySeat(t *testing.T) {
	api := NewAPI()
	api.intentsPerSec = 1

	if !api.allowIntent(7, 60) {
		t.Fatal("first intent on match 7 seat 60 was refused")
	}
	if api.allowIntent(7, 60) {
		t.Fatal("match 7 seat 60 exceeded a one-intent budget")
	}
	// A second match, to prove cleanup does not reach into another match.
	if !api.allowIntent(8, 0) {
		t.Fatal("first intent on match 8 seat 0 was refused")
	}

	api.clearSeatWindows(7, 61)

	// Checked before any further intent, because an allowed intent re-inserts
	// its own key: only match 8's seat 0 window may survive the cleanup.
	if n := len(api.seatWindows); n != 1 {
		t.Errorf("seat window map holds %d entries after clearing a 61-seat match, want 1 (match 8 seat 0)", n)
	}
	if !api.allowIntent(7, 60) {
		t.Error("seat 60 kept its window after its match ended: cleanup does not release high seats")
	}
	if api.allowIntent(8, 0) {
		t.Error("clearing match 7 released match 8's window: cleanup reaches across matches")
	}
}
