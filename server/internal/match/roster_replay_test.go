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

		// Long enough to cross wave boundaries, so elimination (Q9) is part
		// of what must replay identically.
		steps := 40 + rng.Intn(60)
		for step := 0; step < steps && !m.IsOver(); step++ {
			switch roll := rng.Intn(10); {
			case roll < 2:
				m.AdvanceTicks(1 + rng.Intn(400))
			case roll == 9 && m.tick > 10:
				m.AdvanceTicks(ComboResetWindowTicks + 2)
			case roll == 8:
				// Push past a wave boundary so culls happen mid-trial.
				m.AdvanceTicks(WaveTicks / 2)
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
			if r.IsEliminated(Seat(s)) != m.IsEliminated(Seat(s)) {
				t.Fatalf("trial %d: seat %d elimination replayed as %v, want %v",
					trial, s, r.IsEliminated(Seat(s)), m.IsEliminated(Seat(s)))
			}
		}
	}
}

// TestBoardGenerationIsPrefixStableAcrossRosters replaces the older
// "board is identical at every roster" rule, which Q10 deliberately retired:
// the board now GROWS with the roster (twelve cells shared by sixty players is
// not a game). What must survive is the weaker but sufficient property that
// makes that safe - the letter at cell i is a function of (seed, language,
// wave) alone, never of the roster. A bigger board is a prefix-compatible
// extension of a smaller one, so wave generation stays deterministic and a
// 1v1 board is bit-identical to what M0 and M1 shipped.
func TestBoardGenerationIsPrefixStableAcrossRosters(t *testing.T) {
	const seed = 0x1512
	for _, wave := range []int{0, 1, 2} {
		ref := generateWave(seed, "en", wave, MaxCellsPerWave)
		for _, seats := range []int{2, 3, 7, 30, 60} {
			cells := generateWave(seed, "en", wave, BoardCells(seats))
			for i, c := range cells {
				if c.Letter != ref[i].Letter {
					t.Fatalf("wave %d, %d seats: cell %d is %q, want %q - the roster leaked into wave generation",
						wave, seats, i, c.Letter, ref[i].Letter)
				}
				if c.ID != i {
					t.Fatalf("cell %d has ID %d", i, c.ID)
				}
			}
		}
	}

	// A two-seat match must still be exactly the M0 board.
	m, err := New(Config{MatchID: 1, Seed: seed, Lang: "en"})
	if err != nil {
		t.Fatal(err)
	}
	if got := len(m.Snapshot().Cells); got != CellsPerWave {
		t.Fatalf("1v1 board has %d cells, want the unchanged %d", got, CellsPerWave)
	}
}

// TestBoardCellsScalesWithRoster pins the Q10 sizing curve itself.
func TestBoardCellsScalesWithRoster(t *testing.T) {
	if got := BoardCells(2); got != CellsPerWave {
		t.Fatalf("1v1 board %d, want %d unchanged", got, CellsPerWave)
	}
	prev := 0
	for seats := 2; seats <= MaxSeats; seats++ {
		n := BoardCells(seats)
		if n < prev {
			t.Fatalf("board shrank from %d to %d cells at %d seats", prev, n, seats)
		}
		if n > MaxCellsPerWave {
			t.Fatalf("%d seats produced %d cells, above the %d cap", seats, n, MaxCellsPerWave)
		}
		if n < CellsPerWave {
			t.Fatalf("%d seats produced %d cells, below the 1v1 floor", seats, n)
		}
		if seats > 2 && n%BoardColumnsLarge != 0 {
			t.Fatalf("%d seats produced %d cells, not whole rows of %d", seats, n, BoardColumnsLarge)
		}
		// Contention must stay real: never more cells than two per seat.
		if seats > 2 && n > seats*BoardCellsPerSeat+BoardColumnsLarge {
			t.Fatalf("%d seats got %d cells, too generous", seats, n)
		}
		prev = n
	}
	if got := BoardCells(MaxSeats); got != MaxCellsPerWave {
		t.Fatalf("a full %d-seat lobby gets %d cells, want the %d cap (one per player)",
			MaxSeats, got, MaxCellsPerWave)
	}
}
