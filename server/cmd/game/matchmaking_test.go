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

func TestMatchmakerReaperPurgesAbandonedWaiting(t *testing.T) {
	mm := newMatchmaker()
	mm.ttl = 20 * time.Millisecond

	// A waiting entry that is never polled must not leak.
	e := mm.enqueue("en", 0, func(string, *[2]uint64) (uint64, uint64, [2]string, [2]uint64, matchAccess, error) {
		return 0, 0, [2]string{}, [2]uint64{}, matchAccess{}, errRoomCapacity
	})
	if e.Status != "waiting" {
		t.Fatalf("entry should be waiting: %+v", e)
	}
	time.Sleep(60 * time.Millisecond)
	mm.reap()

	if _, ok := mm.poll(e.ID); ok {
		t.Fatalf("abandoned waiting entry survived reap")
	}
	if len(mm.entries) != 0 {
		t.Fatalf("entries map not purged: %d left", len(mm.entries))
	}
	if len(mm.waiting["en"]) != 0 {
		t.Fatalf("waiting queue not purged: %d left", len(mm.waiting["en"]))
	}
}

func TestMatchmakerReaperPurgesMatchedUnpolled(t *testing.T) {
	mm := newMatchmaker()
	mm.ttl = 30 * time.Millisecond
	mk := func(string, *[2]uint64) (uint64, uint64, [2]string, [2]uint64, matchAccess, error) {
		return 7, 99, [2]string{"a", "b"}, [2]uint64{1, 2},
			matchAccess{Code: "code-7", ReadCap: "cap-7"}, nil
	}
	a := mm.enqueue("en", 0, mk)
	b := mm.enqueue("en", 0, mk)
	if b.Status != "matched" {
		t.Fatalf("second entry should be matched: %+v", b)
	}
	time.Sleep(80 * time.Millisecond)
	mm.reap()
	if len(mm.entries) != 0 {
		t.Fatalf("matched-but-unpolled entries not purged: %d", len(mm.entries))
	}
	_ = a
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

// enqueueWithPlayer posts a queue entry optionally bound to a profile.
func enqueueWithPlayer(t *testing.T, srv *httptest.Server, lang string, playerID uint64) (queueEntry, int) {
	t.Helper()
	body := fmt.Sprintf(`{"language":%q`, lang)
	if playerID != 0 {
		body += fmt.Sprintf(`,"player_id":%d`, playerID)
	}
	body += `}`
	resp, err := http.Post(srv.URL+"/v1/queue", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var e queueEntry
	if resp.StatusCode == http.StatusAccepted {
		if err := json.NewDecoder(resp.Body).Decode(&e); err != nil {
			t.Fatal(err)
		}
	}
	return e, resp.StatusCode
}

func TestMatchmakingQueueBindsProfiles(t *testing.T) {
	api := NewAPI()
	srv := httptest.NewServer(api.Routes())
	defer srv.Close()

	pa, code := createPlayer(t, srv, "alice", "en")
	if code != http.StatusCreated {
		t.Fatalf("create alice = %d", code)
	}
	pb, code := createPlayer(t, srv, "bob", "en")
	if code != http.StatusCreated {
		t.Fatalf("create bob = %d", code)
	}

	a, code := enqueueWithPlayer(t, srv, "en", pa.ID)
	if code != http.StatusAccepted || a.Status != "waiting" || a.PlayerID != pa.ID {
		t.Fatalf("first entry = %+v (status %d)", a, code)
	}
	b, code := enqueueWithPlayer(t, srv, "en", pb.ID)
	if code != http.StatusAccepted || b.Status != "matched" {
		t.Fatalf("second entry = %+v (status %d)", b, code)
	}
	qa, _ := pollQueue(t, srv, a.ID)
	if qa.Status != "matched" || qa.MatchID != b.MatchID {
		t.Fatalf("poll A = %+v", qa)
	}
	if qa.UserID != pa.ID || b.UserID != pb.ID {
		t.Fatalf("seats not bound to profiles: A=%d (want %d) B=%d (want %d)",
			qa.UserID, pa.ID, b.UserID, pb.ID)
	}

	api.mu.Lock()
	room := api.rooms[b.MatchID]
	api.mu.Unlock()
	if room == nil {
		t.Fatal("matched room missing")
	}
	if room.UserID(0) != pa.ID || room.UserID(1) != pb.ID {
		t.Fatalf("room user ids = %d/%d, want %d/%d", room.UserID(0), room.UserID(1), pa.ID, pb.ID)
	}
	if !api.isProfiledMatch(b.MatchID) {
		t.Fatal("profile-bound match must be marked profiled")
	}
}

func TestMatchmakingQueueMixedProfileAndAnonymous(t *testing.T) {
	api := NewAPI()
	srv := httptest.NewServer(api.Routes())
	defer srv.Close()

	p, code := createPlayer(t, srv, "alice", "en")
	if code != http.StatusCreated {
		t.Fatalf("create alice = %d", code)
	}

	a, code := enqueueWithPlayer(t, srv, "en", p.ID) // profiled seat 0
	if code != http.StatusAccepted {
		t.Fatalf("profiled enqueue = %d", code)
	}
	b, code := enqueueWithPlayer(t, srv, "en", 0) // anonymous seat 1
	if code != http.StatusAccepted || b.Status != "matched" {
		t.Fatalf("second entry = %+v (status %d)", b, code)
	}
	qa, _ := pollQueue(t, srv, a.ID)
	if qa.UserID != p.ID {
		t.Fatalf("profiled seat user id = %d, want %d", qa.UserID, p.ID)
	}
	if b.UserID == 0 || b.UserID == p.ID {
		t.Fatalf("anonymous seat user id = %d, want a synthetic non-profile id", b.UserID)
	}
	api.mu.Lock()
	room := api.rooms[b.MatchID]
	api.mu.Unlock()
	if room == nil || room.UserID(0) != p.ID {
		t.Fatal("profiled seat not bound")
	}
}

func TestMatchmakingQueueUnknownPlayerRejected(t *testing.T) {
	api := NewAPI()
	srv := httptest.NewServer(api.Routes())
	defer srv.Close()

	_, code := enqueueWithPlayer(t, srv, "en", 424242)
	if code != http.StatusNotFound {
		t.Fatalf("unknown player_id status = %d, want 404", code)
	}
}

func TestMatchmakingQueueDuplicateProfileIsIdempotent(t *testing.T) {
	api := NewAPI()
	srv := httptest.NewServer(api.Routes())
	defer srv.Close()

	p, code := createPlayer(t, srv, "alice", "en")
	if code != http.StatusCreated {
		t.Fatalf("create alice = %d", code)
	}
	a, _ := enqueueWithPlayer(t, srv, "en", p.ID)
	b, _ := enqueueWithPlayer(t, srv, "en", p.ID)
	if a.ID != b.ID {
		t.Fatalf("re-enqueue of the same profile must return the same entry: %q vs %q", a.ID, b.ID)
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

// TestMatchmakerHandsBothSeatsTheSameReadHandle pins the batch 21g contract for
// the queue path. A queued player never calls POST /v1/matches, so the poll
// response is the only place the match code and read capability can reach them;
// if the matchmaker dropped the pair, a queued player could create a match and
// then never be able to read its result once the read is gated.
func TestMatchmakerHandsBothSeatsTheSameReadHandle(t *testing.T) {
	mm := newMatchmaker()
	mk := func(string, *[2]uint64) (uint64, uint64, [2]string, [2]uint64, matchAccess, error) {
		return 7, 99, [2]string{"a", "b"}, [2]uint64{1, 2},
			matchAccess{Code: "code-7", ReadCap: "cap-7"}, nil
	}
	a := mm.enqueue("en", 0, mk)
	b := mm.enqueue("en", 0, mk)

	for name, e := range map[string]*queueEntry{"seat0": a, "seat1": b} {
		if e.Status != "matched" {
			t.Fatalf("%s: status = %q, want matched", name, e.Status)
		}
		if e.MatchCode != "code-7" {
			t.Errorf("%s: match_code = %q, want code-7", name, e.MatchCode)
		}
		if e.ReadCapability != "cap-7" {
			t.Errorf("%s: read_capability = %q, want cap-7", name, e.ReadCapability)
		}
	}
}
