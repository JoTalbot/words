package main

// Matchmaking + replay endpoint tests (M1 vertical slice).

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func enqueue(t *testing.T, srv *httptest.Server, lang string) queueEntry {
	t.Helper()
	resp, err := http.Post(srv.URL+"/v1/queue", "application/json",
		strings.NewReader(fmt.Sprintf(`{"language":%q}`, lang)))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("enqueue status = %d, want 202", resp.StatusCode)
	}
	var e queueEntry
	if err := json.NewDecoder(resp.Body).Decode(&e); err != nil {
		t.Fatal(err)
	}
	return e
}

func pollQueue(t *testing.T, srv *httptest.Server, id string) (queueEntry, int) {
	t.Helper()
	resp, err := http.Get(srv.URL + "/v1/queue/" + id)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var e queueEntry
	if resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(&e); err != nil {
			t.Fatal(err)
		}
	}
	return e, resp.StatusCode
}

func TestMatchmakingPairsTwoPlayers(t *testing.T) {
	api := NewAPI()
	srv := httptest.NewServer(api.Routes())
	defer srv.Close()

	a := enqueue(t, srv, "en")
	if a.Status != "waiting" || a.ID == "" {
		t.Fatalf("first entry = %+v", a)
	}
	b := enqueue(t, srv, "en")
	if b.Status != "matched" || b.MatchID == 0 {
		t.Fatalf("second entry should match immediately: %+v", b)
	}

	pa, code := pollQueue(t, srv, a.ID)
	if code != http.StatusOK || pa.Status != "matched" {
		t.Fatalf("poll A = %+v (status %d)", pa, code)
	}
	if pa.MatchID != b.MatchID {
		t.Fatalf("match ids differ: A=%d B=%d", pa.MatchID, b.MatchID)
	}
	if pa.Token == b.Token || pa.UserID == b.UserID {
		t.Fatalf("seats must differ: A token=%q B token=%q", pa.Token, b.Token)
	}
	if pa.Seed != b.Seed {
		t.Fatalf("seeds differ: %d vs %d", pa.Seed, b.Seed)
	}

	// Both seats must be able to join the live match.
	c0 := dial(t, srv, pa.MatchID, pa.Token)
	defer c0.close()
	s0 := c0.readSnapshot(t)
	if len(s0.Cells) != 12 {
		t.Fatalf("seat0 board size = %d", len(s0.Cells))
	}
	c1 := dial(t, srv, b.MatchID, b.Token)
	defer c1.close()
	s1 := c1.readSnapshot(t)
	for i := range s0.Cells {
		if s0.Cells[i].Letter != s1.Cells[i].Letter {
			t.Fatalf("board diverged at cell %d", i)
		}
	}

	// B's entry has not been polled yet: the first poll delivers the match.
	if pb, code := pollQueue(t, srv, b.ID); code != http.StatusOK || pb.Status != "matched" {
		t.Fatalf("first poll of B = %+v (status %d), want matched", pb, code)
	}
	// One-shot delivery: a second poll of the same entry returns 404.
	if _, code := pollQueue(t, srv, b.ID); code != http.StatusNotFound {
		t.Fatalf("matched entry should be delivered once, got status %d", code)
	}
}

func TestMatchmakingDifferentLanguagesDoNotPair(t *testing.T) {
	api := NewAPI()
	srv := httptest.NewServer(api.Routes())
	defer srv.Close()

	a := enqueue(t, srv, "en")
	b := enqueue(t, srv, "ru")
	if b.Status != "waiting" {
		t.Fatalf("cross-language pairing must not happen: %+v", b)
	}
	pa, code := pollQueue(t, srv, a.ID)
	if code != http.StatusOK || pa.Status != "waiting" {
		t.Fatalf("A should still wait: %+v", pa)
	}
	pb, code := pollQueue(t, srv, b.ID)
	if code != http.StatusOK || pb.Status != "waiting" {
		t.Fatalf("B should still wait: %+v", pb)
	}
}

func TestMatchmakingExpiry(t *testing.T) {
	api := NewAPI()
	api.mm.ttl = 20 * time.Millisecond
	srv := httptest.NewServer(api.Routes())
	defer srv.Close()

	e := enqueue(t, srv, "en")
	time.Sleep(60 * time.Millisecond)
	_, code := pollQueue(t, srv, e.ID)
	if code != http.StatusGone {
		t.Fatalf("expired poll status = %d, want 410", code)
	}
}

func TestReplayEndpointAfterMatch(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping replay-endpoint test in -short mode")
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
	egReadUntilOver(t, c0)

	// Poll until the replay is recorded.
	var rep struct {
		MatchID  uint64        `json:"match_id"`
		Seed     uint64        `json:"seed"`
		Language string        `json:"language"`
		Over     bool          `json:"over"`
		Scores   [2]int64      `json:"scores"`
		Events   []replayEvent `json:"events"`
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp, err := http.Get(fmt.Sprintf("%s/v1/matches/%d/replay", srv.URL, id))
		if err == nil {
			if resp.StatusCode == http.StatusOK {
				_ = json.NewDecoder(resp.Body).Decode(&rep)
				resp.Body.Close()
				break
			}
			resp.Body.Close()
		}
		if time.Now().After(deadline) {
			t.Fatal("replay not available within 5 s of match end")
		}
		time.Sleep(50 * time.Millisecond)
	}

	if !rep.Over || rep.Scores != want {
		t.Fatalf("replay header mismatch: over=%v scores=%d:%d want %d:%d",
			rep.Over, rep.Scores[0], rep.Scores[1], want[0], want[1])
	}
	if len(rep.Events) != len(moves) {
		t.Fatalf("replay events = %d, want %d (script length)", len(rep.Events), len(moves))
	}
	for i, ev := range rep.Events {
		if ev.Result != "accepted" {
			t.Fatalf("event %d result = %q, want accepted", i, ev.Result)
		}
		if ev.Word != moves[i].word {
			t.Fatalf("event %d word = %q, want %q", i, ev.Word, moves[i].word)
		}
	}
	t.Logf("replay endpoint ok: %d events, final %d:%d", len(rep.Events), want[0], want[1])
}
