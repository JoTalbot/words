package main

// Player profile tests (M1 vertical slice): CRUD + validation + stats
// folding from finished matches.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func createPlayer(t *testing.T, srv *httptest.Server, nick, lang string) (Profile, int) {
	t.Helper()
	resp, err := http.Post(srv.URL+"/v1/players", "application/json",
		strings.NewReader(fmt.Sprintf(`{"nickname":%q,"language":%q}`, nick, lang)))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var p Profile
	if resp.StatusCode == http.StatusCreated {
		if err := json.NewDecoder(resp.Body).Decode(&p); err != nil {
			t.Fatal(err)
		}
	}
	return p, resp.StatusCode
}

func getPlayer(t *testing.T, srv *httptest.Server, id uint64) (Profile, int) {
	t.Helper()
	resp, err := http.Get(fmt.Sprintf("%s/v1/players/%d", srv.URL, id))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var p Profile
	if resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(&p); err != nil {
			t.Fatal(err)
		}
	}
	return p, resp.StatusCode
}

func TestPlayerCreateAndGet(t *testing.T) {
	api := NewAPI()
	srv := httptest.NewServer(api.Routes())
	defer srv.Close()

	p, code := createPlayer(t, srv, "alice", "en")
	if code != http.StatusCreated || p.ID == 0 || p.Nickname != "alice" || p.Language != "en" {
		t.Fatalf("create = %+v (status %d)", p, code)
	}
	if p.MatchesPlayed != 0 || p.TotalScore != 0 {
		t.Fatalf("fresh profile has stats: %+v", p)
	}

	got, code := getPlayer(t, srv, p.ID)
	if code != http.StatusOK || got.ID != p.ID || got.Nickname != "alice" {
		t.Fatalf("get = %+v (status %d)", got, code)
	}

	if _, code := getPlayer(t, srv, 999999); code != http.StatusNotFound {
		t.Fatalf("unknown profile status = %d, want 404", code)
	}
}

func TestPlayerValidation(t *testing.T) {
	api := NewAPI()
	srv := httptest.NewServer(api.Routes())
	defer srv.Close()

	if _, code := createPlayer(t, srv, "", "en"); code != http.StatusBadRequest {
		t.Fatalf("empty nickname status = %d, want 400", code)
	}
	if _, code := createPlayer(t, srv, strings.Repeat("x", 33), "en"); code != http.StatusBadRequest {
		t.Fatalf("long nickname status = %d, want 400", code)
	}
	if _, code := createPlayer(t, srv, "bob", "fr"); code != http.StatusBadRequest {
		t.Fatalf("bad language status = %d, want 400", code)
	}
}

func TestMatchWithProfilesUpdatesStats(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping profile-stats integration test in -short mode")
	}
	api := NewAPI()
	srv := httptest.NewServer(api.Routes())
	defer srv.Close()

	p0, _ := createPlayer(t, srv, "alice", "en")
	p1, _ := createPlayer(t, srv, "bob", "en")

	seed := uint64(1512)
	moves, want, solvable := egSolve(t, "en", seed)
	if !solvable {
		t.Fatalf("seed %d not fully claimable", seed)
	}

	body := fmt.Sprintf(`{"language":"en","seed":%d,"player_ids":[%d,%d]}`, seed, p0.ID, p1.ID)
	resp, err := http.Post(srv.URL+"/v1/matches", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	var created createMatchResponse
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if created.UserIDs != [2]uint64{p0.ID, p1.ID} {
		t.Fatalf("user_ids = %v, want profile ids %d/%d", created.UserIDs, p0.ID, p1.ID)
	}

	id := created.MatchID
	c0 := dial(t, srv, id, created.Tokens[0])
	defer c0.close()
	c1 := dial(t, srv, id, created.Tokens[1])
	defer c1.close()
	c0.seat, c0.user = 0, created.UserIDs[0]
	c1.seat, c1.user = 1, created.UserIDs[1]
	_ = c0.readSnapshot(t)
	_ = c1.readSnapshot(t)

	bots := []*testClient{c0, c1}
	users := [2]uint64{created.UserIDs[0], created.UserIDs[1]}
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

	// Stats are folded in asynchronously (ticker records on match end).
	var f0, f1 Profile
	deadline := time.Now().Add(5 * time.Second)
	for {
		f0, _ = getPlayer(t, srv, p0.ID)
		f1, _ = getPlayer(t, srv, p1.ID)
		if f0.MatchesPlayed == 1 && f1.MatchesPlayed == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("stats not updated within 5s: p0=%+v p1=%+v", f0, f1)
		}
		time.Sleep(50 * time.Millisecond)
	}

	if f0.TotalScore != want[0] || f1.TotalScore != want[1] {
		t.Fatalf("total_score = %d/%d, want %d/%d", f0.TotalScore, f1.TotalScore, want[0], want[1])
	}
	switch {
	case want[0] > want[1]:
		if f0.Wins != 1 || f1.Losses != 1 {
			t.Fatalf("expected p0 win / p1 loss, got %+v / %+v", f0, f1)
		}
	case want[0] < want[1]:
		if f0.Losses != 1 || f1.Wins != 1 {
			t.Fatalf("expected p0 loss / p1 win, got %+v / %+v", f0, f1)
		}
	default:
		if f0.Draws != 1 || f1.Draws != 1 {
			t.Fatalf("expected draws, got %+v / %+v", f0, f1)
		}
	}
	t.Logf("profile stats ok: p0=%+v p1=%+v", f0, f1)
}

func TestMatchRejectsUnknownPlayerID(t *testing.T) {
	api := NewAPI()
	srv := httptest.NewServer(api.Routes())
	defer srv.Close()

	p0, _ := createPlayer(t, srv, "alice", "en")
	resp, err := http.Post(srv.URL+"/v1/matches", "application/json",
		strings.NewReader(fmt.Sprintf(`{"language":"en","player_ids":[%d,424242]}`, p0.ID)))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}
