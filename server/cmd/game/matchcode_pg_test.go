package main

// The batch 8 acceptance criterion under the batch 21g gate: a finished result
// must still be readable after a restart. That is the reason the read capability
// is stored with the result at all - seat tokens are process-lifetime state, so
// a credential that has to outlive a restart cannot be one of them. Skipped
// unless WORDARENA_TEST_POSTGRES_DSN is set, like the other Postgres tests.

import (
	"testing"
	"time"
)

func TestPostgresMatchCodeSurvivesRestart(t *testing.T) {
	dsn := pgTestDSN(t)
	api, err := NewAPIWithPostgres(dsn)
	if err != nil {
		t.Fatalf("NewAPIWithPostgres: %v", err)
	}
	defer api.Stop()

	// A modest id, and removed below: match_id is the durable high-water mark,
	// and leaving a large one behind would shift id allocation for every later
	// test sharing this database.
	base := uint64(910001)
	if repo, ok := api.resultRepo.(interface{ MaxMatchID() (uint64, error) }); ok {
		if max, err := repo.MaxMatchID(); err == nil && max >= base {
			base = max + 1
		}
	}
	const code = "c0ffee00c0ffee00c0ffee00c0ffee00"
	const readCap = "deadbeefdeadbeefdeadbeefdeadbeef"

	defer func() {
		if _, err := api.pgDB.Exec(`DELETE FROM match_results WHERE match_id = $1::numeric`,
			u64Param(base)); err != nil {
			t.Errorf("cleanup: %v", err)
		}
	}()

	res := matchResult{
		MatchID: base, Seed: 1512, Language: "en", Over: true,
		WinnerSeat: 0, IsTie: false, Scores: [2]int64{43, 45},
		StateVer: 12, ServerTick: 91, Code: code, ReadCap: readCap,
		recordedAt: time.Now(),
		Events: []replayEvent{
			{Seq: 1, Tick: 3, Seat: 0, Word: "arena", Result: "accepted", ScoreAdded: 12, TotalScore: 12},
		},
	}
	if err := api.resultRepo.Put(res); err != nil {
		t.Fatalf("put: %v", err)
	}

	// A second instance on the same DSN is what a restart looks like: the
	// in-memory matchCaps and resultCodes indexes are empty.
	restarted, err := NewAPIWithPostgres(dsn)
	if err != nil {
		t.Fatalf("second NewAPIWithPostgres: %v", err)
	}
	defer restarted.Stop()

	got, ok, err := restarted.resultRepo.GetByCode(code)
	if err != nil {
		t.Fatalf("GetByCode after restart: %v", err)
	}
	if !ok {
		t.Fatal("GetByCode after restart = not found; the code did not survive")
	}
	if got.MatchID != base || got.Seed != 1512 || got.Scores != res.Scores {
		t.Errorf("round-trip mismatch: %+v", got)
	}
	// The capability must come back with the row, or the gate has nothing to
	// compare against and every hardened read would 401.
	if got.ReadCap != readCap {
		t.Errorf("read capability after restart = %q, want %q", got.ReadCap, readCap)
	}

	// And the endpoint itself must serve it under the gate, which is the
	// user-visible form of the criterion.
	restarted.requireReadCap = true
	if _, ok := restarted.lookupResultByCode(code); !ok {
		t.Fatal("lookupResultByCode after restart = not found")
	}
	if _, ok := restarted.resolveMatchRef(u64Param(base)); ok {
		t.Error("the sequential id resolved while hardened; enumeration is not blocked")
	}

	// An unknown code must not resolve, and must not be confused with a real one.
	if _, ok, err := restarted.resultRepo.GetByCode("ffffffffffffffffffffffffffffffff"); err != nil || ok {
		t.Errorf("unknown code = ok=%v err=%v, want not found", ok, err)
	}
	// An empty code must not match the pre-003 rows whose column is NULL.
	if _, ok, err := restarted.resultRepo.GetByCode(""); err != nil || ok {
		t.Errorf("empty code = ok=%v err=%v, want not found", ok, err)
	}
}

// TestPostgresLegacyRowHasNoCode pins the shape of rows written before migration
// 003. They must read back cleanly with empty code and capability rather than
// failing the scan, because the live database still contains them.
func TestPostgresLegacyRowHasNoCode(t *testing.T) {
	dsn := pgTestDSN(t)
	api, err := NewAPIWithPostgres(dsn)
	if err != nil {
		t.Fatalf("NewAPIWithPostgres: %v", err)
	}
	defer api.Stop()

	base := uint64(910501)
	if repo, ok := api.resultRepo.(interface{ MaxMatchID() (uint64, error) }); ok {
		if max, err := repo.MaxMatchID(); err == nil && max >= base {
			base = max + 1
		}
	}
	defer func() {
		if _, err := api.pgDB.Exec(`DELETE FROM match_results WHERE match_id = $1::numeric`,
			u64Param(base)); err != nil {
			t.Errorf("cleanup: %v", err)
		}
	}()

	// Insert the way the pre-003 code did: no code, no capability.
	if _, err := api.pgDB.Exec(`
		INSERT INTO match_results
			(match_id, seed, language, over, winner_seat, is_tie,
			 score0, score1, state_version, server_tick, events, recorded_at)
		VALUES ($1::numeric,$2::numeric,'en',true,0,false,1,2,3,4,'[]'::jsonb,now())
		ON CONFLICT (match_id) DO NOTHING`, u64Param(base), u64Param(1512)); err != nil {
		t.Fatalf("legacy insert: %v", err)
	}

	got, ok, err := api.resultRepo.Get(base)
	if err != nil {
		t.Fatalf("Get on a legacy row: %v", err)
	}
	if !ok {
		t.Fatal("legacy row not found")
	}
	if got.Code != "" || got.ReadCap != "" {
		t.Errorf("legacy row = code %q cap %q, want both empty", got.Code, got.ReadCap)
	}
}
