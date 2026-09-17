package match

import "testing"

// Board-side levers (batch 36D). These tests exist because the levers are only
// safe to sweep if the zero value is provably the shipped rule and if shortening
// a lock cannot break the "a fresh claim is not recaptured inside its own wave"
// property the board relies on.

func boardMatch(t *testing.T, board BoardParams) *Match {
	t.Helper()
	m, err := NewWithBoard(Config{
		MatchID: 1, Seed: 1, Lang: "en", Seats: 4, Board: board,
	}, "catdogfunrun")
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// lockedCells returns the cell ids the seat owns and that are still locked.
func lockedCells(m *Match, seat Seat) []int {
	var out []int
	for i := range m.cells {
		if m.cells[i].State == CellOwnedLocked && m.cells[i].Owner == seat {
			out = append(out, i)
		}
	}
	return out
}

func TestBoardZeroValueIsTheShippedRule(t *testing.T) {
	got := BoardParams{}.normalized()
	if got != DefaultBoardParams() {
		t.Fatalf("the zero value resolved to %+v, want the shipped %+v", got, DefaultBoardParams())
	}
	if got.LockTicks != LockTicks {
		t.Fatalf("default lock is %d ticks, want the shipped constant %d", got.LockTicks, LockTicks)
	}
	if got.StealDebitPercent != boardDebitPercentFull {
		t.Fatalf("default steal debit is %d%%, want %d%%", got.StealDebitPercent, boardDebitPercentFull)
	}

	// The stronger claim: an untouched Config must simulate identically to one
	// that spells the defaults, so every fixture, replay and baseline keeps its
	// numbers.
	a, b := boardMatch(t, BoardParams{}), boardMatch(t, DefaultBoardParams())
	for _, m := range []*Match{a, b} {
		submitWord(t, m, 1, "cat")
		submitWord(t, m, 2, "dog")
		m.AdvanceTicks(LockTicks + 1)
		submitWord(t, m, 3, "run")
	}
	if a.Fingerprint() != b.Fingerprint() {
		t.Fatal("an explicit default board produced a different fingerprint than an omitted one")
	}
	if a.players[1].Score != b.players[1].Score {
		t.Fatalf("scores moved: %d vs %d", a.players[1].Score, b.players[1].Score)
	}
}

func TestBoardLockTicksShortensLeaderProtection(t *testing.T) {
	// A cell claimed this wave is protected by the lock, not by the tick
	// counter alone: with a one-tick lock a rival still cannot recapture it
	// before the match advances. That ordering is the property this lever could
	// have destroyed, so it is asserted at both settings.
	for _, tc := range []struct {
		name     string
		lock     int
		advance  int
		wantTake bool
	}{
		{"shipped lock survives one tick", 0, 1, false},
		// The boundary is exact: the counter is decremented on the tick that
		// follows the claim, so a lock of N ticks is gone once N ticks have
		// passed - not N+1. The 35A-era test used LockTicks+1 and never
		// measured the edge.
		{"shipped lock survives one tick short of its duration", 0, LockTicks - 1, false},
		{"shipped lock expires at exactly its duration", 0, LockTicks, true},
		{"shipped lock stays expired past its duration", 0, LockTicks + 1, true},
		{"one-tick lock protects inside its wave", 1, 0, false},
		{"one-tick lock releases after a tick", 1, 1, true},
	} {
		m := boardMatch(t, BoardParams{LockTicks: tc.lock})
		claim := submitWord(t, m, 1, "cat")
		if claim.Result != ResultAccepted {
			t.Fatalf("%s: claim was %v", tc.name, claim.Result)
		}
		if len(lockedCells(m, 1)) == 0 {
			t.Fatalf("%s: the claim left no locked cells to test", tc.name)
		}
		m.AdvanceTicks(tc.advance)
		steal := m.Submit(2, []int{0, 1, 2})
		if got := steal.Result == ResultAccepted; got != tc.wantTake {
			t.Fatalf("%s: steal accepted=%v, want %v (result %v)", tc.name, got, tc.wantTake, steal.Result)
		}
	}
}

// stealVictimLoss claims "cat" for seat 1, lets the lock expire, steals the same
// cells with seat 2, and returns what the victim actually lost. expectedDebit
// is computed from the per-cell credited values captured before the steal,
// because a successful steal re-credits them.
func stealVictimLoss(t *testing.T, m *Match) (lost int64, credit []int) {
	t.Helper()
	if submitWord(t, m, 1, "cat").Result != ResultAccepted {
		t.Fatal("the victim's claim was rejected")
	}
	cells := []int{0, 1, 2}
	for _, id := range cells {
		credit = append(credit, m.cells[id].CreditedValue)
	}
	if credit[0]+credit[1]+credit[2] <= 0 {
		t.Fatal("the fixture cells carry no credited value; the test proves nothing")
	}
	m.AdvanceTicks(LockTicks + 1)
	before := m.players[1].Score
	if steal := m.Submit(2, cells); steal.Result != ResultAccepted {
		t.Fatalf("the steal was rejected: %v", steal.Result)
	}
	return before - m.players[1].Score, credit
}

func expectedDebit(credit []int, percent int) int64 {
	effective := BoardParams{StealDebitPercent: percent}.normalized().StealDebitPercent
	var want int64
	for _, v := range credit {
		want += int64(v * effective / boardDebitPercentFull)
	}
	return want
}

func TestBoardStealDebitIsAFractionOfTheCreditedValue(t *testing.T) {
	// The debit is floored per cell, so a 50% setting on odd letter values is
	// not half of the total: the expectation is built the same way the rule
	// builds it rather than by halving a sum.
	for _, percent := range []int{100, 75, 50, 25, -1} {
		m := boardMatch(t, BoardParams{StealDebitPercent: percent})
		lost, credit := stealVictimLoss(t, m)
		if want := expectedDebit(credit, percent); lost != want {
			t.Fatalf("percent=%d on credit %v: victim lost %d, want %d", percent, credit, lost, want)
		}
	}
	// Explicitly: "no debit at all" is what the negative request means, and the
	// victim must keep every point.
	lost, _ := stealVictimLoss(t, boardMatch(t, BoardParams{StealDebitPercent: -1}))
	if lost != 0 {
		t.Fatalf("a zero debit still cost the victim %d points", lost)
	}
}

func TestBoardDebitFractionLeavesTheStealersWordScoreAlone(t *testing.T) {
	// The lever moves what the victim loses. It must not reprice the stealing
	// word, which is the same invariant 35A proved for the catch-up bonus.
	stealer := map[int]int64{}
	victim := map[int]int64{}
	for _, percent := range []int{100, 50, -1} {
		m := boardMatch(t, BoardParams{StealDebitPercent: percent})
		submitWord(t, m, 1, "cat")
		m.AdvanceTicks(LockTicks + 1)
		steal := m.Submit(2, []int{0, 1, 2})
		if steal.Result != ResultAccepted {
			t.Fatalf("percent=%d: steal rejected (%v)", percent, steal.Result)
		}
		stealer[percent] = m.players[2].Score
		victim[percent] = m.players[1].Score
	}
	if stealer[100] != stealer[50] || stealer[50] != stealer[-1] {
		t.Fatalf("the stealer's score depends on the victim's debit: %v", stealer)
	}
	if !(victim[100] < victim[50] && victim[50] < victim[-1]) {
		t.Fatalf("a smaller debit did not leave the victim strictly better off: %v", victim)
	}
}

func TestBoardParamsAreClamped(t *testing.T) {
	cases := []struct{ in, want BoardParams }{
		{BoardParams{LockTicks: -5}, BoardParams{LockTicks: LockTicks, StealDebitPercent: 100}},
		{BoardParams{LockTicks: boardLockTicksMax + 1000000}, BoardParams{LockTicks: boardLockTicksMax, StealDebitPercent: 100}},
		{BoardParams{StealDebitPercent: 500}, BoardParams{LockTicks: LockTicks, StealDebitPercent: 100}},
		{BoardParams{StealDebitPercent: -7}, BoardParams{LockTicks: LockTicks, StealDebitPercent: 0}},
	}
	for _, c := range cases {
		if got := c.in.normalized(); got != c.want {
			t.Fatalf("%+v normalized to %+v, want %+v", c.in, got, c.want)
		}
	}
	// A nonsense value must still produce a playable match, not an error.
	if m := boardMatch(t, BoardParams{LockTicks: -1, StealDebitPercent: 900}); m.board.LockTicks != LockTicks {
		t.Fatal("an invalid lock duration reached the match unnormalized")
	}
}
