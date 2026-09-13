package match

import "testing"

// Batch 30A (M2 foundation): the simulation moved from a hardcoded pair of
// seats to a variable-length roster. Two properties matter and both are
// asserted here rather than assumed:
//
//  1. 1v1 behaviour is UNCHANGED - the default roster is 2, and the rank/tie/
//     result rules produce exactly what the M0/M1 rules produced;
//  2. a larger roster is accepted, scores independently per seat and ranks
//     competitively (ties share the better rank).

func newMatch(t *testing.T, seats int) *Match {
	t.Helper()
	m, err := New(Config{MatchID: 1, Seed: 1512, Lang: "en", Seats: seats})
	if err != nil {
		t.Fatalf("new match with %d seats: %v", seats, err)
	}
	return m
}

func TestDefaultRosterIsTwoSeats(t *testing.T) {
	m, err := New(Config{MatchID: 1, Seed: 1512, Lang: "en"})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if m.Seats() != 2 {
		t.Fatalf("default roster %d, want 2 (existing 1v1 callers must be unaffected)", m.Seats())
	}
	if got := len(m.Snapshot().Players); got != 2 {
		t.Fatalf("snapshot players %d, want 2", got)
	}
}

func TestRosterBoundsAreEnforced(t *testing.T) {
	for _, seats := range []int{1, MaxSeats + 1, 1000} {
		if _, err := New(Config{MatchID: 1, Seed: 1, Lang: "en", Seats: seats}); err == nil {
			t.Fatalf("seats=%d was accepted; the bound exists so a bad request cannot allocate freely", seats)
		}
	}
	for _, seats := range []int{MinSeats, 8, MaxSeats} {
		if _, err := New(Config{MatchID: 1, Seed: 1, Lang: "en", Seats: seats}); err != nil {
			t.Fatalf("seats=%d rejected: %v", seats, err)
		}
	}
}

func TestRankIsCompetitiveAcrossAnyRoster(t *testing.T) {
	m := newMatch(t, 6)
	// 30, 30, 20, 10, 0, 0 -> ranks 1,1,3,4,5,5
	scores := []int64{30, 30, 20, 10, 0, 0}
	want := []int{1, 1, 3, 4, 5, 5}
	for i, v := range scores {
		m.players[i].Score = v
	}
	for seat, wantRank := range want {
		if got := m.rankOf(Seat(seat)); got != wantRank {
			t.Errorf("seat %d rank %d, want %d", seat, got, wantRank)
		}
	}
	if !m.isTied() {
		t.Error("two seats share the top score: isTied must be true")
	}
}

func TestOneVsOneRankRulesUnchanged(t *testing.T) {
	m := newMatch(t, 2)
	m.players[0].Score = 5
	m.players[1].Score = 0
	if m.rankOf(0) != 1 || m.rankOf(1) != 2 {
		t.Fatalf("1v1 ranks %d/%d, want 1/2", m.rankOf(0), m.rankOf(1))
	}
	if m.isTied() {
		t.Fatal("distinct scores must not be a tie")
	}
	m.players[1].Score = 5
	if m.rankOf(0) != 1 || m.rankOf(1) != 1 {
		t.Fatalf("tied 1v1 ranks %d/%d, want 1/1 (the M0 draw rule)", m.rankOf(0), m.rankOf(1))
	}
	if !m.isTied() {
		t.Fatal("equal scores in 1v1 must be a tie")
	}
}

func TestResultPicksTheHighestScoringSeat(t *testing.T) {
	m := newMatch(t, 4)
	m.players[2].Score = 41
	m.players[0].Score = 12
	m.phase = "over"
	res := m.Result()
	if !res.Over || res.IsTie || res.WinnerSeat != 2 {
		t.Fatalf("result %+v, want winner seat 2", res)
	}

	m2 := newMatch(t, 4)
	m2.players[1].Score = 7
	m2.players[3].Score = 7
	m2.phase = "over"
	if res := m2.Result(); !res.IsTie {
		t.Fatalf("result %+v, want a tie when the top score is shared", res)
	}
}

// The fingerprint feeds replay equality checks, so it must cover every seat's
// competitive state - not just the first two.
func TestFingerprintCoversEverySeat(t *testing.T) {
	a := newMatch(t, 5)
	b := newMatch(t, 5)
	if a.Fingerprint() != b.Fingerprint() {
		t.Fatal("identical matches must share a fingerprint")
	}
	b.players[4].Score = 3
	if a.Fingerprint() == b.Fingerprint() {
		t.Fatal("a score change on the last seat must change the fingerprint")
	}
}

// A word submitted by a high seat index must score for that seat only: the
// scoring path used to index a fixed pair.
func TestSubmitScoresTheSubmittingSeatOnly(t *testing.T) {
	m, err := NewWithBoard(Config{MatchID: 1, Seed: 1, Lang: "en", Seats: 8}, "catxxxxxxxxx")
	if err != nil {
		t.Fatalf("new with board: %v", err)
	}
	ev := m.Submit(7, []int{0, 1, 2})
	if ev.Result != ResultAccepted {
		t.Fatalf("CAT from seat 7: %s", ev.WordResultString)
	}
	if m.Score(7) <= 0 {
		t.Fatalf("seat 7 score %d, want > 0", m.Score(7))
	}
	for seat := 0; seat < 8; seat++ {
		if seat == 7 && m.Score(Seat(seat)) > 0 {
			continue
		}
		if seat != 7 && m.Score(Seat(seat)) != 0 {
			t.Fatalf("seat %d scored %d for another seat's word", seat, m.Score(Seat(seat)))
		}
	}
	for _, id := range []int{0, 1, 2} {
		if m.cells[id].Owner != 7 || m.cells[id].State != CellOwnedLocked {
			t.Fatalf("cell %d owner=%d state=%v, want owned+locked by seat 7", id, m.cells[id].Owner, m.cells[id].State)
		}
	}
}
