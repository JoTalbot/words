package match

import (
	"testing"

	"github.com/JoTalbot/words/server/internal/prng"
)

// Batch 30E: replay determinism is the property the whole anti-cheat and
// spectator story rests on, and TestReplayProperty only ever exercised two
// seats. A roster-sized version is the difference between "the simulation
// compiles with N seats" and "an N-seat match can be re-derived from its log".

// TestRosterReplayProperty runs randomized N-seat matches and rebuilds each one
// from its event log alone. Per-seat state (score, combo, cell ownership) is
// folded into the fingerprint, so a single seat's state drifting - for example
// a combo window tracked globally rather than per player - fails here.
func TestRosterReplayProperty(t *testing.T) {
	rng := prng.New(0x30E5)
	words := []string{"cat", "dog", "fun", "run", "sun", "bat", "cab", "bad"}
	boards := []string{"catdogfunrun", "cabdogfunred"}
	rosters := []int{3, 5, 8, 16}

	for trial := 0; trial < 40; trial++ {
		seats := rosters[trial%len(rosters)]
		letters := boards[rng.Intn(len(boards))]
		cfg := Config{MatchID: 1, Seed: 0xABCDEF1234567890, Lang: "en", Seats: seats}

		m, err := NewWithBoard(cfg, letters)
		if err != nil {
			t.Fatalf("trial %d: new %d-seat match: %v", trial, seats, err)
		}
		if m.Seats() != seats {
			t.Fatalf("trial %d: roster %d, want %d", trial, m.Seats(), seats)
		}

		steps := 40 + rng.Intn(60)
		for step := 0; step < steps && !m.IsOver(); step++ {
			switch roll := rng.Intn(10); {
			case roll < 2:
				m.AdvanceTicks(1 + rng.Intn(400))
			case roll == 9 && m.tick > 10:
				m.AdvanceTicks(ComboResetWindowTicks + 2)
			default:
				seat := Seat(rng.Intn(seats))
				path := findCells(m, seat, words[rng.Intn(len(words))])
				if path == nil {
					m.Submit(seat, []int{rng.Intn(CellsPerWave), rng.Intn(CellsPerWave), rng.Intn(CellsPerWave)})
					continue
				}
				m.Submit(seat, path)
			}
		}
		m.AdvanceTicks(500)

		r, err := NewWithBoard(cfg, letters)
		if err != nil {
			t.Fatalf("trial %d: new replay match: %v", trial, err)
		}
		for _, ev := range m.Events() {
			for r.Tick() < ev.Tick {
				r.AdvanceTicks(1)
			}
			got := r.Submit(ev.Seat, ev.CellIDs)
			if got.Result != ev.Result || got.ScoreAdded != ev.ScoreAdded || got.TotalScore != ev.TotalScore {
				t.Fatalf("trial %d (%d seats) seq %d from seat %d: replay %s %d/%d != %s %d/%d",
					trial, seats, ev.Seq, ev.Seat,
					got.WordResultString, got.ScoreAdded, got.TotalScore,
					ev.WordResultString, ev.ScoreAdded, ev.TotalScore)
			}
			if got.IsSteal != ev.IsSteal {
				t.Fatalf("trial %d seq %d: replay steal flag mismatch", trial, ev.Seq)
			}
		}
		for r.Tick() < m.Tick() {
			r.AdvanceTicks(1)
		}
		if r.Fingerprint() != m.Fingerprint() {
			t.Fatalf("trial %d (%d seats): replay fingerprint %s != %s",
				trial, seats, r.Fingerprint(), m.Fingerprint())
		}
		// Every seat, not just the two that happen to be busiest, must have
		// been reproduced exactly.
		for s := 0; s < seats; s++ {
			if r.Score(Seat(s)) != m.Score(Seat(s)) {
				t.Fatalf("trial %d: seat %d replayed score %d != %d",
					trial, s, r.Score(Seat(s)), m.Score(Seat(s)))
			}
		}
	}
}

// TestBoardGenerationIgnoresRosterSize pins that the roster does not leak into
// wave generation: the same (seed, language, wave) must produce the same board
// whether two players or sixty are seated, otherwise a client could not join a
// match without knowing the final roster first.
func TestBoardGenerationIgnoresRosterSize(t *testing.T) {
	const seed = 0x1512
	ref := ""
	for _, seats := range []int{2, 3, 7, 30, 60} {
		m, err := New(Config{MatchID: 1, Seed: seed, Lang: "en", Seats: seats})
		if err != nil {
			t.Fatalf("%d seats: %v", seats, err)
		}
		board := ""
		for _, c := range m.Snapshot().Cells {
			board += string(c.Letter)
		}
		if ref == "" {
			ref = board
			continue
		}
		if board != ref {
			t.Fatalf("%d seats produced board %q, want %q - the roster leaked into wave generation",
				seats, board, ref)
		}
	}

	// The same must hold for later waves, which are generated lazily.
	ref = ""
	for _, seats := range []int{2, 9, 60} {
		m, err := New(Config{MatchID: 1, Seed: seed, Lang: "en", Seats: seats})
		if err != nil {
			t.Fatalf("%d seats: %v", seats, err)
		}
		m.AdvanceTicks(WaveTicks + 1)
		board := ""
		for _, c := range m.Snapshot().Cells {
			board += string(c.Letter)
		}
		if ref == "" {
			ref = board
			continue
		}
		if board != ref {
			t.Fatalf("wave 1 with %d seats is %q, want %q", seats, board, ref)
		}
	}
}
