package match

import "testing"

// Q9 (batch 31A): per-wave elimination with a survivor share. These tests pin
// the rule itself, the protections around it, and the guarantee that nothing
// below EliminationMinSeats changed.

func newRoster(t *testing.T, seats int) *Match {
	t.Helper()
	m, err := New(Config{MatchID: 1, Seed: 0xABCD, Lang: "en", Seats: seats})
	if err != nil {
		t.Fatalf("%d seats: %v", seats, err)
	}
	return m
}

func TestSurvivorTargetShrinksTheLobbyByAThird(t *testing.T) {
	cases := []struct{ active, want int }{
		{60, 40}, {40, 26}, {26, 17}, {9, 6}, {4, 2}, {3, 2}, {2, 2},
	}
	for _, c := range cases {
		if got := survivorTarget(c.active); got != c.want {
			t.Errorf("survivorTarget(%d) = %d, want %d", c.active, got, c.want)
		}
	}
}

func TestCullRemovesTheLowestScorers(t *testing.T) {
	m := newRoster(t, 9) // target after one cull: 6
	// Distinct scores so the cut line is unambiguous: seat i scores i.
	for i := range m.players {
		m.players[i].Score = int64(i)
	}
	m.cullAtWaveBoundary()

	// Seats 0,1,2 are the three lowest and must go; 3..8 survive.
	for seat := 0; seat < 9; seat++ {
		wantOut := seat < 3
		if got := m.IsEliminated(Seat(seat)); got != wantOut {
			t.Errorf("seat %d (score %d) eliminated=%v, want %v", seat, seat, got, wantOut)
		}
	}
	if got := m.activeCount(); got != 6 {
		t.Fatalf("%d seats survived, want 6", got)
	}
	// The wave index is recorded, which is what makes the cull replayable.
	if m.players[0].EliminatedAtWave != m.wave {
		t.Fatalf("elimination recorded at wave %d, want %d", m.players[0].EliminatedAtWave, m.wave)
	}
}

func TestCullKeepsEveryoneTiedWithTheLastSurvivor(t *testing.T) {
	// Losing a Royale on seat index alone would be indefensible, so a tie
	// across the cut line is resolved in the players' favour: the roster
	// shrinks more slowly instead.
	m := newRoster(t, 9) // target 6
	for i := range m.players {
		m.players[i].Score = 5 // everyone level
	}
	m.cullAtWaveBoundary()
	if got := m.activeCount(); got != 9 {
		t.Fatalf("%d seats survived an all-tie wave, want all 9 kept", got)
	}

	// A partial tie: four seats share the cut-line score.
	m2 := newRoster(t, 9)
	scores := []int64{9, 8, 7, 6, 5, 5, 5, 5, 1}
	for i := range m2.players {
		m2.players[i].Score = scores[i]
	}
	m2.cullAtWaveBoundary()
	// Target is 6; the 6th-best score is 5, and seats 4..7 all hold 5, so
	// all four stay. Only seat 8 (score 1) is culled.
	for seat := 0; seat < 8; seat++ {
		if m2.IsEliminated(Seat(seat)) {
			t.Errorf("seat %d (score %d) was culled but ties or beats the cut line", seat, scores[seat])
		}
	}
	if !m2.IsEliminated(Seat(8)) {
		t.Error("the sole below-cut seat survived")
	}
}

func TestSmallRostersNeverCull(t *testing.T) {
	for _, seats := range []int{2, 3} {
		m := newRoster(t, seats)
		for i := range m.players {
			m.players[i].Score = int64(i) // a clear loser exists
		}
		m.cullAtWaveBoundary()
		if got := m.activeCount(); got != seats {
			t.Fatalf("%d-seat match culled down to %d; rosters below %d must be untouched",
				seats, got, EliminationMinSeats)
		}
		if m.IsEliminated(0) {
			t.Fatalf("%d-seat match eliminated seat 0", seats)
		}
	}
}

func TestCullNeverEmptiesTheMatch(t *testing.T) {
	m := newRoster(t, 8)
	for i := range m.players {
		m.players[i].Score = int64(i)
	}
	// Cull repeatedly, far more often than a match ever would.
	for i := 0; i < 20; i++ {
		m.cullAtWaveBoundary()
	}
	if got := m.activeCount(); got < MinSurvivors {
		t.Fatalf("repeated culls left %d seats, below the floor of %d", got, MinSurvivors)
	}
}

func TestEliminatedSeatCannotScore(t *testing.T) {
	m, err := NewWithBoard(Config{MatchID: 1, Seed: 1, Lang: "en", Seats: 6}, "catdogfunrun")
	if err != nil {
		t.Fatal(err)
	}
	path := findCells(m, 0, "cat")
	if path == nil {
		t.Fatal("fixture board must contain cat")
	}
	m.players[0].EliminatedAtWave = 0

	before := m.Fingerprint()
	ev := m.Submit(0, path)
	if ev.Result != ResultBlockedByRule {
		t.Fatalf("an eliminated seat scored: %v", ev.Result)
	}
	if m.players[0].Score != 0 {
		t.Fatalf("eliminated seat gained %d points", m.players[0].Score)
	}
	if m.Fingerprint() != before {
		t.Fatal("a spectator's intent mutated competitive state")
	}
	// The intent is still logged, so replay sees exactly what happened.
	if len(m.Events()) != 1 {
		t.Fatalf("%d events logged, want the rejected intent recorded", len(m.Events()))
	}
	// A live seat is unaffected.
	if ev := m.Submit(1, path); ev.Result != ResultAccepted {
		t.Fatalf("a live seat was blocked: %v", ev.Result)
	}
}

func TestEliminationIsFoldedIntoTheFingerprint(t *testing.T) {
	a := newRoster(t, 8)
	b := newRoster(t, 8)
	if a.Fingerprint() != b.Fingerprint() {
		t.Fatal("identical matches disagree before any change")
	}
	b.players[3].EliminatedAtWave = 1
	if a.Fingerprint() == b.Fingerprint() {
		t.Fatal("a differing elimination set compares equal; replay could not detect a divergent cull")
	}
}

func TestWaveBoundaryCullsAndSnapshotReportsIt(t *testing.T) {
	m := newRoster(t, 12) // target after wave 0: 8
	for i := range m.players {
		m.players[i].Score = int64(i)
	}
	// Run the wave out of time; the boundary must cull before the next wave.
	m.AdvanceTicks(WaveTicks + 1)

	if m.wave != 1 {
		t.Fatalf("wave %d, want the match to have advanced to 1", m.wave)
	}
	if got := m.activeCount(); got != 8 {
		t.Fatalf("%d seats active after the first boundary, want 8", got)
	}
	snap := m.Snapshot()
	out := 0
	for _, p := range snap.Players {
		if p.IsEliminated {
			out++
		}
	}
	if out != 4 {
		t.Fatalf("snapshot reports %d eliminated players, want 4", out)
	}
	if len(snap.Players) != 12 {
		t.Fatalf("snapshot dropped players: %d rows, want the full roster of 12", len(snap.Players))
	}
}

func TestRoyaleRosterShrinksAcrossAFullMatch(t *testing.T) {
	// The headline shape of the mode: 60 -> 40 -> 26 over three waves.
	m := newRoster(t, 60)
	for i := range m.players {
		m.players[i].Score = int64(i)
	}
	want := []int{40, 26}
	for w, expect := range want {
		m.AdvanceTicks(WaveTicks + 1)
		if got := m.activeCount(); got != expect {
			t.Fatalf("after wave %d boundary: %d survivors, want %d", w, got, expect)
		}
	}
}
