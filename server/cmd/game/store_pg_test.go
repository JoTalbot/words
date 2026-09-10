package main

// PostgreSQL integration tests (M1 durable storage). Skipped unless
// WORDARENA_TEST_POSTGRES_DSN is set, e.g.:
//
//	WORDARENA_TEST_POSTGRES_DSN='postgres://wordarena:wordarena@127.0.0.1:5432/wordarena_test?sslmode=disable' \
//	  go test -count=1 ./cmd/game/ -run Postgres -v

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func pgTestDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("WORDARENA_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("WORDARENA_TEST_POSTGRES_DSN not set; skipping Postgres integration test")
	}
	return dsn
}

func TestPostgresProfileCRUDAndStats(t *testing.T) {
	dsn := pgTestDSN(t)
	api, err := NewAPIWithPostgres(dsn)
	if err != nil {
		t.Fatalf("NewAPIWithPostgres: %v", err)
	}
	defer api.Stop()

	p, err := api.profiles.Create("alice", "en")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if p.ID == 0 {
		t.Fatal("profile id must be assigned by the backend")
	}

	got, ok, err := api.profiles.Get(p.ID)
	if err != nil || !ok || got.Nickname != "alice" || got.Language != "en" {
		t.Fatalf("get = %+v ok=%v err=%v", got, ok, err)
	}

	if err := api.profiles.Record(p.ID, 42, "win"); err != nil {
		t.Fatalf("record win: %v", err)
	}
	if err := api.profiles.Record(p.ID, 10, "loss"); err != nil {
		t.Fatalf("record loss: %v", err)
	}
	if err := api.profiles.Record(p.ID, 0, "draw"); err != nil {
		t.Fatalf("record draw: %v", err)
	}
	got, _, _ = api.profiles.Get(p.ID)
	if got.MatchesPlayed != 3 || got.Wins != 1 || got.Losses != 1 || got.Draws != 1 || got.TotalScore != 52 {
		t.Fatalf("stats after 3 outcomes = %+v", got)
	}

	// Unknown ids are no-ops, mirroring the in-memory backend.
	if err := api.profiles.Record(999999, 1, "win"); err != nil {
		t.Fatalf("record unknown id: %v", err)
	}
	if _, ok, _ := api.profiles.Get(999999); ok {
		t.Fatal("unknown id must not resolve")
	}
}

func TestPostgresResultPutGet(t *testing.T) {
	dsn := pgTestDSN(t)
	api, err := NewAPIWithPostgres(dsn)
	if err != nil {
		t.Fatalf("NewAPIWithPostgres: %v", err)
	}
	defer api.Stop()

	// Ids are derived from the durable high-water mark instead of being
	// hard-coded. With a literal 9001 the test was only correct on an empty
	// database: a second run found 9001 already present (so Put became a no-op
	// against stale data) and the "absent id" probe below collided with the id
	// TestPostgresDurabilityAcrossInstances had allocated. That made the whole
	// Postgres suite non-re-runnable against a live database, which is exactly
	// how it is run on the deployment host.
	base := uint64(9001)
	if repo, ok := api.resultRepo.(interface{ MaxMatchID() (uint64, error) }); ok {
		max, err := repo.MaxMatchID()
		if err != nil {
			t.Fatalf("MaxMatchID: %v", err)
		}
		if max >= base {
			base = max + 1
		}
	}
	absent := base + 1

	res := matchResult{
		MatchID: base, Seed: 1512, Language: "en", Over: true,
		WinnerSeat: 0, IsTie: false, Scores: [2]int64{42, 7},
		StateVer: 10, ServerTick: 5400, recordedAt: time.Now(),
		Events: []replayEvent{
			{Seq: 1, Tick: 1, Seat: 0, Word: "cat", Result: "accepted", ScoreAdded: 5, TotalScore: 5},
		},
	}
	if err := api.resultRepo.Put(res); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, ok, err := api.resultRepo.Get(base)
	if err != nil || !ok {
		t.Fatalf("get = ok=%v err=%v", ok, err)
	}
	if got.Seed != res.Seed || got.WinnerSeat != 0 || got.Scores != res.Scores || len(got.Events) != 1 {
		t.Fatalf("round-trip mismatch: %+v", got)
	}

	// Duplicate put is a no-op, not an error.
	if err := api.resultRepo.Put(res); err != nil {
		t.Fatalf("duplicate put: %v", err)
	}
	if _, ok, _ := api.resultRepo.Get(absent); ok {
		t.Fatalf("id %d was never written but resolved", absent)
	}
}

// TestPostgresDurabilityAcrossInstances is the strongest check: a full live
// match with profile-bound seats folds stats and persists results; a second
// API instance over the same DSN (a "restart") reads both back.
func TestPostgresDurabilityAcrossInstances(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping full-match durability test in -short mode")
	}
	dsn := pgTestDSN(t)

	api, err := NewAPIWithPostgres(dsn)
	if err != nil {
		t.Fatalf("NewAPIWithPostgres: %v", err)
	}
	srv := httptest.NewServer(api.Routes())
	defer srv.Close()
	defer api.Stop()

	pa, code := createPlayer(t, srv, "alice", "en")
	if code != http.StatusCreated {
		t.Fatalf("create alice = %d", code)
	}
	pb, code := createPlayer(t, srv, "bob", "en")
	if code != http.StatusCreated {
		t.Fatalf("create bob = %d", code)
	}

	seed := uint64(1512)
	moves, want, solvable := egSolve(t, "en", seed)
	if !solvable {
		t.Fatalf("seed %d not fully claimable", seed)
	}

	id, tokens, userIDs := createMatchWithPlayers(t, srv, "en", &seed, [2]uint64{pa.ID, pb.ID})
	if userIDs != [2]uint64{pa.ID, pb.ID} {
		t.Fatalf("user ids = %v, want profile ids", userIDs)
	}
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

	// Wait for the profile stats to fold (recorded on match end).
	var ga, gb Profile
	deadline := time.Now().Add(10 * time.Second)
	for {
		ga, _ = getPlayer(t, srv, pa.ID)
		gb, _ = getPlayer(t, srv, pb.ID)
		if ga.MatchesPlayed == 1 && gb.MatchesPlayed == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("profile stats not folded: alice=%+v bob=%+v", ga, gb)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if ga.TotalScore != want[0] || gb.TotalScore != want[1] {
		t.Fatalf("profile total scores = %d/%d, want %d/%d", ga.TotalScore, gb.TotalScore, want[0], want[1])
	}

	// "Restart": a fresh instance over the same DSN reads durable state.
	api2, err := NewAPIWithPostgres(dsn)
	if err != nil {
		t.Fatalf("second NewAPIWithPostgres: %v", err)
	}
	defer api2.Stop()
	srv2 := httptest.NewServer(api2.Routes())
	defer srv2.Close()

	ga2, code := getPlayer(t, srv2, pa.ID)
	if code != http.StatusOK || ga2.MatchesPlayed != 1 || ga2.TotalScore != want[0] {
		t.Fatalf("durable profile = %+v (status %d)", ga2, code)
	}

	// Result and replay must be served from durable storage (cache empty).
	resp, err := http.Get(fmt.Sprintf("%s/v1/matches/%d/replay", srv2.URL, id))
	if err != nil {
		t.Fatalf("replay after restart: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("replay after restart status = %d, want 200", resp.StatusCode)
	}
	var rep struct {
		MatchID uint64        `json:"match_id"`
		Over    bool          `json:"over"`
		Scores  [2]int64      `json:"scores"`
		Events  []replayEvent `json:"events"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rep); err != nil {
		t.Fatal(err)
	}
	if !rep.Over || rep.Scores != want || len(rep.Events) != len(moves) {
		t.Fatalf("durable replay = over=%v scores=%d:%d events=%d", rep.Over, rep.Scores[0], rep.Scores[1], len(rep.Events))
	}
}

// createMatchWithPlayers mirrors createMatch but binds seats to profiles.
func createMatchWithPlayers(t *testing.T, srv *httptest.Server, lang string, seed *uint64, pids [2]uint64) (id uint64, tokens [2]string, userIDs [2]uint64) {
	t.Helper()
	body := fmt.Sprintf(`{"language":%q,"player_ids":[%d,%d]`, lang, pids[0], pids[1])
	if seed != nil {
		body += fmt.Sprintf(`,"seed":%d`, *seed)
	}
	body += `}`
	resp, err := http.Post(srv.URL+"/v1/matches", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("create match: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status %d", resp.StatusCode)
	}
	var out createMatchResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out.MatchID, out.Tokens, out.UserIDs
}
