package main

// Regression coverage for the uint64 columns of match_results.
//
// The live service on arm-server-01 dropped the durable result of roughly half
// of all finished matches on 2026-09-10 with
//
//	match lifecycle=result_store_error err=postgres: put result: unable to
//	encode 0xc07644e8862430b9 into binary format for int8 (OID 20):
//	13868347868007641273 is greater than maximum value for int64
//
// randomSeed() draws eight crypto-random bytes, so about half of server-chosen
// seeds sit above MaxInt64, and match_id/seed were declared BIGINT. The match
// still played and still reported over=true, so nothing looked wrong: only the
// durable row was missing. Every existing test used curated small seeds
// (1512, 9001), which is why the suite was green.
//
// The unit test below pins the wire representation without a database, so the
// failure class stays caught even where no Postgres is available. The
// integration test pins the same property against a real server.

import (
	"math"
	"testing"
	"time"
)

func TestUint64ColumnParamRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		in   uint64
	}{
		{"zero", 0},
		{"curated seed 1512", 1512},
		{"max int64", math.MaxInt64},
		{"first value the old BIGINT column rejected", math.MaxInt64 + 1},
		{"the value from the live failure", 13868347868007641273},
		{"max uint64", math.MaxUint64},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := scanU64("seed", u64Param(tc.in))
			if err != nil {
				t.Fatalf("round trip of %d: %v", tc.in, err)
			}
			if got != tc.in {
				t.Fatalf("round trip of %d produced %d", tc.in, got)
			}
		})
	}
}

func TestScanU64RejectsOutOfRange(t *testing.T) {
	// NUMERIC(20,0) admits values a Go uint64 cannot. A row in that state is a
	// corrupted store, and silently truncating it would misreport which match a
	// result belongs to, so the scan must refuse it.
	for _, bad := range []string{"", "abc", "-1", "18446744073709551616"} {
		if _, err := scanU64("seed", bad); err == nil {
			t.Errorf("scanU64(%q) = nil error, want a rejection", bad)
		}
	}
}

// TestPostgresResultHighBitSeed is the test that would have caught the live
// defect. It writes a result whose seed has the top bit set and whose match id
// does too, then reads it back through a second API instance, which is the path
// a restart takes when the in-memory result TTL has already expired.
func TestPostgresResultHighBitSeed(t *testing.T) {
	dsn := pgTestDSN(t)
	api, err := NewAPIWithPostgres(dsn)
	if err != nil {
		t.Fatalf("NewAPIWithPostgres: %v", err)
	}
	defer api.Stop()

	// Not MaxUint64: match_id is the durable high-water mark, and a maximal id
	// would leave the next allocated id overflowing for every later test that
	// shares this database. The row is removed below regardless.
	const matchID = uint64(1)<<63 | 0x5EED
	const seed = uint64(13868347868007641273) // the value from the live log

	// Registered after `defer api.Stop()` so it runs first (LIFO): the delete
	// needs the live handle. The row must not survive the test either way,
	// because match_id is the durable high-water mark and a value above
	// MaxInt64 left behind here would poison id allocation for every later test
	// sharing this database.
	defer func() {
		if _, err := api.pgDB.Exec(`DELETE FROM match_results WHERE match_id = $1::numeric`,
			u64Param(matchID)); err != nil {
			t.Errorf("cleanup: %v", err)
		}
	}()

	res := matchResult{
		MatchID: matchID, Seed: seed, Language: "en", Over: true,
		WinnerSeat: 0, IsTie: false, Scores: [2]int64{43, 45},
		StateVer: 12, ServerTick: 91, recordedAt: time.Now(),
		Events: []replayEvent{
			{Seq: 1, Tick: 3, Seat: 0, Word: "arena", Result: "accepted", ScoreAdded: 12, TotalScore: 12},
		},
	}

	if err := api.resultRepo.Put(res); err != nil {
		t.Fatalf("put with a high-bit seed: %v", err)
	}

	// A second instance on the same DSN, i.e. what a restart looks like: the
	// in-memory cache is empty and the durable row is the only source.
	restarted, err := NewAPIWithPostgres(dsn)
	if err != nil {
		t.Fatalf("second NewAPIWithPostgres: %v", err)
	}
	defer restarted.Stop()

	got, ok, err := restarted.resultRepo.Get(matchID)
	if err != nil {
		t.Fatalf("get after restart: %v", err)
	}
	if !ok {
		t.Fatal("get after restart = not found: the result was not persisted")
	}
	if got.Seed != seed {
		t.Errorf("seed = %d, want %d (lossy round trip)", got.Seed, seed)
	}
	if got.MatchID != matchID {
		t.Errorf("match_id = %d, want %d", got.MatchID, matchID)
	}
	if got.Scores != res.Scores || got.ServerTick != res.ServerTick || len(got.Events) != 1 {
		t.Errorf("round-trip mismatch: %+v", got)
	}

	// MaxMatchID reads the high-water mark through the same column, so it must
	// see a value above MaxInt64 rather than a negative one.
	max, err := restarted.resultRepo.(interface{ MaxMatchID() (uint64, error) }).MaxMatchID()
	if err != nil {
		t.Fatalf("MaxMatchID: %v", err)
	}
	if max < matchID {
		t.Errorf("MaxMatchID = %d, want at least %d", max, matchID)
	}
}
