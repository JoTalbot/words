package match

import (
	"testing"

	"github.com/JoTalbot/words/server/internal/dictionary"
	"github.com/JoTalbot/words/server/internal/prng"
)

// testCfg returns a reusable EN match config with a fixed seed.
func testCfg() Config {
	return Config{MatchID: 1, Seed: 0xABCDEF1234567890, Lang: "en"}
}

// mustMatch builds a match and overrides wave-0 cells with explicit letters
// so rule tests do not depend on generator output.
func mustMatch(t *testing.T, letters string) *Match {
	t.Helper()
	rs := []rune(letters)
	if len(rs) != CellsPerWave {
		t.Fatalf("board needs %d letters, got %d (%q)", CellsPerWave, len(rs), letters)
	}
	m, err := NewWithBoard(testCfg(), string(rs))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// findCells finds a path spelling word using cells that are free or already
// owned by seat — mirroring what a legitimate client may submit. Returns nil
// when impossible. Deterministic: prefers the lowest cell ids.
func findCells(m *Match, seat Seat, word string) []int {
	rs := []rune(word)
	var path []int
	used := map[int]bool{}
	var search func(int) bool
	search = func(pos int) bool {
		if pos == len(rs) {
			return true
		}
		for i := range m.cells {
			if used[i] {
				continue
			}
			c := m.cells[i]
			if c.Letter != rs[pos] {
				continue
			}
			if c.State != CellFree && c.Owner != seat {
				continue // opponent-owned cells are only used explicitly
			}
			used[i] = true
			path = append(path, i)
			if search(pos + 1) {
				return true
			}
			path = path[:len(path)-1]
			delete(used, i)
		}
		return false
	}
	if !search(0) {
		return nil
	}
	return path
}

// ---- board determinism ----

// Golden board vector: (seed 0xABCDEF1234567890, en, wave 0). Pinned after
// deterministic-pool fix; changing the PRNG or letter data breaks this test.
func TestBoardGoldenVector(t *testing.T) {
	m, err := New(testCfg())
	if err != nil {
		t.Fatal(err)
	}
	got := boardString(m)
	if want := "rhsornertars"; got != want {
		t.Fatalf("board = %q, want %q", got, want)
	}
}

func TestBoardDeterminismAcrossInstances(t *testing.T) {
	a, _ := New(testCfg())
	b, _ := New(testCfg())
	if boardString(a) != boardString(b) {
		t.Fatalf("same seed diverged: %q vs %q", boardString(a), boardString(b))
	}
}

func TestBoardWaveDeterminism(t *testing.T) {
	// Wave 1 must be deterministic across instances too.
	a, _ := New(testCfg())
	b, _ := New(testCfg())
	a.startWave(1)
	b.startWave(1)
	if boardString(a) != boardString(b) {
		t.Fatal("wave 1 diverged across instances")
	}
	if boardString(a) == "rhsornertars" {
		t.Log("wave1 equals wave0 by chance (fine)")
	}
}

func boardString(m *Match) string {
	b := make([]byte, CellsPerWave)
	for i, c := range m.cells {
		b[i] = byte(c.Letter)
	}
	return string(b)
}

func TestBoardLetterPoolShape(t *testing.T) {
	alpha := map[rune]bool{}
	for _, r := range "abcdefghijklmnopqrstuvwxyz" {
		alpha[r] = true
	}
	cfg := testCfg()
	totalVowels := 0
	for seed := uint64(1); seed <= 300; seed++ {
		cfg.Seed = seed
		m, err := New(cfg)
		if err != nil {
			t.Fatal(err)
		}
		vowels := 0
		for _, c := range m.cells {
			if !alpha[c.Letter] {
				t.Fatalf("seed %d: letter %q outside en alphabet", seed, c.Letter)
			}
			switch c.Letter {
			case 'a', 'e', 'i', 'o', 'u':
				vowels++
			}
		}
		totalVowels += vowels
	}
	avg := float64(totalVowels) / 300
	if avg < 2 || avg > 10 {
		t.Fatalf("average vowels per board %.1f out of plausible range", avg)
	}
}

// ---- claims ----

func TestClaimBasic(t *testing.T) {
	m := mustMatch(t, "catdefghijkl") // c=3 a=1 t=1 -> 5 points, combo 1
	path := findCells(m, 0, "cat")
	if path == nil {
		t.Fatal("board cannot spell cat")
	}
	ev := m.Submit(0, path)
	if ev.Result != ResultAccepted {
		t.Fatalf("result = %s", ev.WordResultString)
	}
	if ev.Word != "cat" || ev.IsSteal {
		t.Fatalf("event %+v", ev)
	}
	if ev.ScoreAdded != 5 {
		t.Fatalf("score_added = %d, want 5 (c3+a1+t1)", ev.ScoreAdded)
	}
	if m.Score(0) != 5 || m.Combo(0) != 1 {
		t.Fatalf("score/combo = %d/%d", m.Score(0), m.Combo(0))
	}
	if m.StateVersion() != 1 {
		t.Fatalf("state_version = %d, want 1", m.StateVersion())
	}
	for _, id := range path {
		c := m.cells[id]
		if c.Owner != 0 || c.State != CellOwnedLocked || c.LockRemainingTicks != LockTicks {
			t.Fatalf("cell %d not claimed/locked: %+v", id, c)
		}
	}
}

func TestClaimBySeatOne(t *testing.T) {
	m := mustMatch(t, "catdefghijkl")
	path := findCells(m, 1, "cat")
	if path == nil {
		t.Fatal("no cat path")
	}
	ev := m.Submit(1, path)
	if ev.Result != ResultAccepted {
		t.Fatal(ev.WordResultString)
	}
	if m.cells[path[0]].Owner != 1 {
		t.Fatal("owner mismatch for seat 1")
	}
}

func TestClaimRejectedNotInDict(t *testing.T) {
	m := mustMatch(t, "xqzvwypbcfkj") // first three letters xqz — no word
	ev := m.Submit(1, []int{0, 1, 2})
	if ev.Result != ResultRejectedNotInDict {
		t.Fatalf("result = %s, want rejected_not_in_dict", ev.WordResultString)
	}
	if ev.ScoreAdded != 0 || m.Score(1) != 0 || m.StateVersion() != 0 {
		t.Fatal("rejected word must not mutate state")
	}
	if m.Combo(1) != 0 {
		t.Fatal("rejected word must not touch combo")
	}
}

func TestSubmitInvalid(t *testing.T) {
	m := mustMatch(t, "catdefghijkl")
	if ev := m.Submit(0, []int{0}); ev.Result != ResultInvalidInput {
		t.Fatalf("short word = %s", ev.WordResultString)
	}
	if ev := m.Submit(0, []int{0, 0, 1}); ev.Result != ResultInvalidInput {
		t.Fatalf("duplicate cells = %s", ev.WordResultString)
	}
	if ev := m.Submit(0, []int{0, 1, 99}); ev.Result != ResultInvalidInput {
		t.Fatalf("out-of-range = %s", ev.WordResultString)
	}
}

func TestPureReuseBlocked(t *testing.T) {
	m := mustMatch(t, "catdefghijkl")
	first := m.Submit(0, findCells(m, 0, "cat"))
	if first.Result != ResultAccepted {
		t.Fatal("setup failed")
	}
	// Reusing own cells without any fresh/steal cell is blocked.
	second := m.Submit(0, findCells(m, 0, "cat"))
	if second.Result != ResultBlockedByRule {
		t.Fatalf("pure reuse = %s, want blocked_by_rule", second.WordResultString)
	}
	if m.StateVersion() != 1 {
		t.Fatal("blocked submit must not bump state version")
	}
}

func TestStealFlow(t *testing.T) {
	m := mustMatch(t, "cabdnefgijkl")
	// A claims "cab" = cells {c,a,b}: c3+a1+b3 = 7.
	cab := []int{0, 1, 2}
	if ev := m.Submit(0, cab); ev.Result != ResultAccepted || ev.ScoreAdded != 7 {
		t.Fatalf("A cab: %s added %d", ev.WordResultString, ev.ScoreAdded)
	}
	// Opponent-locked cells block attempts.
	bad := []int{2, 1, 3} // b(A locked) a(A locked) d(free)
	if ev := m.Submit(1, bad); ev.Result != ResultBlockedByRule {
		t.Fatalf("steal while locked = %s, want blocked_by_rule", ev.WordResultString)
	}
	// Locks expire after 91 ticks.
	m.AdvanceTicks(LockTicks + 1)
	for _, id := range cab {
		if m.cells[id].State != CellOwnedUnlocked {
			t.Fatalf("cell %d still locked after expiry", id)
		}
	}
	// B steals "bad": b(A) a(A) d(free): b3+a1+d2 = 6. Debit A: credited
	// b=3 + a=1 (claimed at combo 1) = 4. A: 7-4 = 3.
	ev := m.Submit(1, bad)
	if ev.Result != ResultAccepted {
		t.Fatalf("steal = %s", ev.WordResultString)
	}
	if !ev.IsSteal {
		t.Fatal("event must be marked is_steal")
	}
	if ev.ScoreAdded != 6 {
		t.Fatalf("steal added %d, want 6", ev.ScoreAdded)
	}
	if m.Score(0) != 3 || m.Score(1) != 6 {
		t.Fatalf("scores A=%d B=%d, want 3/6", m.Score(0), m.Score(1))
	}
	for _, id := range []int{1, 2, 3} {
		c := m.cells[id]
		if c.Owner != 1 || c.State != CellOwnedLocked {
			t.Fatalf("cell %d not transferred to B: %+v", id, c)
		}
	}
	if m.cells[1].CreditedValue != 1 || m.cells[2].CreditedValue != 3 {
		t.Fatalf("credited values wrong: %d/%d", m.cells[1].CreditedValue, m.cells[2].CreditedValue)
	}
}

func TestComboGrowthAndReset(t *testing.T) {
	m := mustMatch(t, "catdogpqrstu")
	// claim1: cat -> 5 pts, combo 1.
	if ev := m.Submit(0, findCells(m, 0, "cat")); ev.ScoreAdded != 5 {
		t.Fatalf("claim1 added %d, want 5", ev.ScoreAdded)
	}
	// claim2: dog = d2+o1+g2 = 5; combo 2 -> mult 1.25 -> floor(6.25) = 6.
	ev2 := m.Submit(0, findCells(m, 0, "dog"))
	if ev2.ScoreAdded != 6 || ev2.ComboMult != 1.25 {
		t.Fatalf("claim2 added %d mult %v", ev2.ScoreAdded, ev2.ComboMult)
	}
	if m.Score(0) != 11 {
		t.Fatalf("score = %d, want 11", m.Score(0))
	}
	// Wait: no fresh cells left except p,q,r,s,t,u — no valid words; skip.
	_ = m

	// Reset path: new match, claim word, wait past window, claim again.
	m2 := mustMatch(t, "catdogfunxyz")
	_ = m2.Submit(0, findCells(m2, 0, "fun"))
	evA := m2.Submit(0, findCells(m2, 0, "cat"))
	if evA.ComboMult != 1.25 {
		t.Fatalf("combo mult = %v, want 1.25", evA.ComboMult)
	}
	m2.AdvanceTicks(ComboResetWindowTicks + 2)
	evB := m2.Submit(0, findCells(m2, 0, "dog"))
	if evB.ComboMult != 1.0 {
		t.Fatalf("combo mult after idle = %v, want 1.0", evB.ComboMult)
	}
	if m2.Combo(0) != 1 {
		t.Fatalf("combo = %d, want 1 after reset", m2.Combo(0))
	}
}

func TestIdenticalIntentsIdenticalStates(t *testing.T) {
	a := mustMatch(t, "catdogfunman")
	b := mustMatch(t, "catdogfunman")
	order := [][2]any{{0, "cat"}, {1, "dog"}, {0, "fun"}, {1, "man"}}
	for _, o := range order {
		seat := o[0].(int)
		word := o[1].(string)
		pa, pb := findCells(a, Seat(seat), word), findCells(b, Seat(seat), word)
		if pa == nil || pb == nil {
			t.Fatalf("no path for %s", word)
		}
		ea := a.Submit(Seat(seat), pa)
		eb := b.Submit(Seat(seat), pb)
		if ea.Result != eb.Result || ea.ScoreAdded != eb.ScoreAdded {
			t.Fatalf("events diverged for %s", word)
		}
	}
	if a.Fingerprint() != b.Fingerprint() {
		t.Fatal("identical intent streams diverged")
	}
	// Deterministic winner of the same cell: the first processed claim owns it.
	if a.cells[0].Owner != 0 {
		t.Fatalf("seat0 claim should own cell 0, got %d", a.cells[0].Owner)
	}
}

func TestWaveAdvanceOnFullBoard(t *testing.T) {
	m := mustMatch(t, "catdogfunman")
	claims := []struct {
		seat Seat
		word string
	}{{0, "cat"}, {1, "dog"}, {0, "fun"}, {1, "man"}}
	for _, c := range claims {
		p := findCells(m, c.seat, c.word)
		if p == nil {
			t.Fatalf("no path for %s", c.word)
		}
		if ev := m.Submit(c.seat, p); ev.Result != ResultAccepted {
			t.Fatalf("claim %s by seat %d: %s", c.word, c.seat, ev.WordResultString)
		}
	}
	if m.Wave() != 1 {
		t.Fatalf("wave = %d, want auto-advance to 1", m.Wave())
	}
	// New wave cells are fresh and deterministic.
	for _, c := range m.cells {
		if c.State != CellFree {
			t.Fatalf("wave 1 cell not fresh: %+v", c)
		}
	}
	// seat0: cat (combo1, 5) + fun (combo2, floor(6*1.25)=7) = 12.
	// seat1: dog (combo1, 5) + man (combo2, floor(5*1.25)=6) = 11.
	if m.Score(0) != 12 || m.Score(1) != 11 {
		t.Fatalf("scores %d/%d", m.Score(0), m.Score(1))
	}
}

func TestWaveTimeoutAndMatchEnd(t *testing.T) {
	m := mustMatch(t, "catdogpqrstu")
	m.AdvanceTicks(WaveTicks + 5)
	if m.Wave() != 1 {
		t.Fatalf("wave after timeout = %d, want 1", m.Wave())
	}
	m.AdvanceTicks(WaveTicks + 5)
	m.AdvanceTicks(WaveTicks + 5)
	if !m.IsOver() {
		t.Fatal("match must be over after 3 timed-out waves")
	}
	res := m.Result()
	if !res.Over || !res.IsTie || m.Score(0) != 0 || m.Score(1) != 0 {
		t.Fatalf("result %+v scores %d/%d", res, m.Score(0), m.Score(1))
	}
	if ev := m.Submit(0, []int{0, 1, 2}); ev.Result != ResultMatchNotActive {
		t.Fatalf("late submit = %s", ev.WordResultString)
	}
}

func TestMatchEndWinner(t *testing.T) {
	m := mustMatch(t, "catdogpqrstu")
	if ev := m.Submit(0, findCells(m, 0, "cat")); ev.Result != ResultAccepted {
		t.Fatal(ev.WordResultString)
	}
	// Drain remaining waves by timeout; seat0 keeps the lead.
	m.AdvanceTicks(WaveTicks)
	m.AdvanceTicks(WaveTicks)
	m.AdvanceTicks(WaveTicks)
	res := m.Result()
	if !res.Over || res.IsTie || res.WinnerSeat != 0 {
		t.Fatalf("result %+v", res)
	}
	if m.Score(0) != 5 {
		t.Fatalf("score = %d", m.Score(0))
	}
}

// ---- replay ----

func TestReplayProperty(t *testing.T) {
	rng := prng.New(0x5151)
	words := []string{"cat", "dog", "fun", "run", "sun", "bat", "cab", "bad"}
	seedBoards := []string{
		"catdogfunrun", // c a t d o g f u n r u n — n/r/u collisions ok
		"cabdogfunred", // mix with stealable short words
	}
	for trial := 0; trial < 80; trial++ {
		cfg := testCfg()
		cfg.Seed = rng.Uint64()
		letters := seedBoards[rng.Intn(len(seedBoards))]
		m := mustMatch(t, letters)

		steps := 30 + rng.Intn(60)
		for step := 0; step < steps && !m.IsOver(); step++ {
			roll := rng.Intn(10)
			if roll < 2 {
				m.AdvanceTicks(1 + rng.Intn(400))
				continue
			}
			if roll == 9 && m.tick > 10 {
				// occasional reset-style gap crossing the combo window
				m.AdvanceTicks(ComboResetWindowTicks + 2)
				continue
			}
			seat := Seat(rng.Intn(2))
			w := words[rng.Intn(len(words))]
			path := findCells(m, seat, w)
			if path == nil {
				m.Submit(seat, []int{rng.Intn(12), rng.Intn(12), rng.Intn(12)})
				continue
			}
			m.Submit(seat, path)
		}
		m.AdvanceTicks(500)

		// Deterministic replay from the event log.
		r := mustMatch(t, letters)
		for _, ev := range m.Events() {
			for r.Tick() < ev.Tick {
				r.AdvanceTicks(1)
			}
			got := r.Submit(ev.Seat, ev.CellIDs)
			if got.Result != ev.Result {
				t.Fatalf("trial %d seq %d: replay result %s != %s", trial, ev.Seq, got.WordResultString, ev.WordResultString)
			}
			if got.ScoreAdded != ev.ScoreAdded || got.TotalScore != ev.TotalScore {
				t.Fatalf("trial %d seq %d: replay score %d/%d != %d/%d",
					trial, ev.Seq, got.ScoreAdded, got.TotalScore, ev.ScoreAdded, ev.TotalScore)
			}
			if got.IsSteal != ev.IsSteal {
				t.Fatalf("trial %d seq %d: replay steal flag mismatch", trial, ev.Seq)
			}
		}
		// The original match advanced 500 trailing ticks after its last
		// event; the replay must mirror that to compare canonical state.
		for r.Tick() < m.Tick() {
			r.AdvanceTicks(1)
		}
		if r.Fingerprint() != m.Fingerprint() {
			t.Fatalf("trial %d: replay fingerprint diverged\norig: %s\nrp  : %s",
				trial, m.Fingerprint(), r.Fingerprint())
		}
	}
}

// ---- snapshots & languages ----

func TestSnapshotConsistency(t *testing.T) {
	m := mustMatch(t, "catdefghijkl")
	if ev := m.Submit(0, findCells(m, 0, "cat")); ev.Result != ResultAccepted {
		t.Fatal("setup")
	}
	s := m.Snapshot()
	if s.StateVersion != 1 || s.CurrentWave != 0 || s.Phase != "active" {
		t.Fatalf("snapshot header: %+v", s)
	}
	if s.Players[0].Score != 5 || s.Players[1].Score != 0 {
		t.Fatalf("scores %+v", s.Players)
	}
	if s.Players[0].RankPosition != 1 || s.Players[1].RankPosition != 2 {
		t.Fatalf("ranks %+v", s.Players)
	}
	if s.RemainingTimeMs <= 0 || s.RemainingTimeMs > WaveTicks*tickMillis {
		t.Fatalf("remaining time %d", s.RemainingTimeMs)
	}
	if len(s.Cells) != CellsPerWave {
		t.Fatalf("cells %d", len(s.Cells))
	}
	for i := 0; i < 3; i++ {
		if s.Cells[i].OwnerSeat != 0 || !s.Cells[i].IsLocked {
			t.Fatalf("cell %d view %+v", i, s.Cells[i])
		}
	}
	if s.Cells[3].OwnerSeat != -1 || s.Cells[3].IsLocked {
		t.Fatalf("cell 3 should be free: %+v", s.Cells[3])
	}
}

func TestAllLanguagesBoot(t *testing.T) {
	for _, lang := range []string{"en", "ru", "uk"} {
		m, err := New(Config{MatchID: 7, Seed: 99, Lang: lang})
		if err != nil {
			t.Fatalf("%s: %v", lang, err)
		}
		if len(m.cells) != CellsPerWave {
			t.Fatalf("%s board size %d", lang, len(m.cells))
		}
		if s := m.Snapshot(); s.Language != lang || s.CurrentWave != 0 {
			t.Fatalf("%s snapshot %+v", lang, s)
		}
	}
}

func TestRuClaim(t *testing.T) {
	cfg := Config{MatchID: 2, Seed: 7, Lang: "ru"}
	m, err := NewWithBoard(cfg, "котдомпрустт")
	if err != nil {
		t.Fatal(err)
	}
	path := findCells(m, 0, "кот")
	if path == nil {
		t.Fatal("cannot spell кот")
	}
	ev := m.Submit(0, path)
	if ev.Result != ResultAccepted || ev.Word != "кот" {
		t.Fatalf("ru claim %s %s", ev.WordResultString, ev.Word)
	}
	// к2 о1 т1 = 4 at combo 1.
	if ev.ScoreAdded != 4 {
		t.Fatalf("ru score %d, want 4", ev.ScoreAdded)
	}
}

func TestUkClaim(t *testing.T) {
	cfg := Config{MatchID: 3, Seed: 8, Lang: "uk"}
	m, err := NewWithBoard(cfg, "кітсільдоммм")
	if err != nil {
		t.Fatal(err)
	}
	path := findCells(m, 0, "кіт")
	if path == nil {
		t.Fatal("cannot spell кіт")
	}
	ev := m.Submit(0, path)
	if ev.Result != ResultAccepted || ev.Word != "кіт" {
		t.Fatalf("uk claim %s %s", ev.WordResultString, ev.Word)
	}
	// к2 і1 т1 = 4.
	if ev.ScoreAdded != 4 {
		t.Fatalf("uk score %d, want 4", ev.ScoreAdded)
	}
}

// ---- Sudden Death (docs/M1-SUDDEN-DEATH.md) ----

// suddenCfg returns an EN config with the opt-in tiebreak enabled.
func suddenCfg() Config {
	c := testCfg()
	c.SuddenDeath = true
	return c
}

// findAnyWord returns the first legal 3-cell word path on the current board
// that the pinned dictionary accepts, or nil. Deterministic: enumerates
// distinct cell triples in ascending cell-id order and prefers free/own cells
// for the given seat.
func findAnyWord(m *Match, seat Seat) []int {
	n := len(m.cells)
	for i := 0; i < n; i++ {
		for j := 0; j < n; j++ {
			if j == i {
				continue
			}
			for k := 0; k < n; k++ {
				if k == i || k == j {
					continue
				}
				word := string([]rune{m.cells[i].Letter, m.cells[j].Letter, m.cells[k].Letter})
				if !m.dict.ContainsNormalized(word) {
					continue
				}
				path := []int{i, j, k}
				legal := true
				for _, id := range path {
					c := m.cells[id]
					if c.State != CellFree && c.Owner != seat {
						legal = false
						break
					}
				}
				if legal {
					return path
				}
			}
		}
	}
	return nil
}

// TestSuddenDeathDisabledTieIsDraw locks M0 behaviour: with the flag off, a
// tied final score ends the match as a draw (no extra wave).
func TestSuddenDeathDisabledTieIsDraw(t *testing.T) {
	m, err := New(testCfg()) // SuddenDeath == false
	if err != nil {
		t.Fatal(err)
	}
	m.AdvanceTicks(WavesPerMatch * WaveTicks)
	if !m.IsOver() {
		t.Fatal("match must end after the final wave, not enter a tiebreak")
	}
	if m.Wave() != WavesPerMatch-1 {
		t.Fatalf("wave = %d, want %d", m.Wave(), WavesPerMatch-1)
	}
	res := m.Result()
	if !res.Over || !res.IsTie {
		t.Fatalf("result = %+v, want draw", res)
	}
}

// TestSuddenDeathEntersTiebreak checks that a tied final wave starts the
// opt-in tiebreak instead of a draw.
func TestSuddenDeathEntersTiebreak(t *testing.T) {
	m, err := New(suddenCfg())
	if err != nil {
		t.Fatal(err)
	}
	m.AdvanceTicks(WavesPerMatch * WaveTicks)
	if m.IsOver() {
		t.Fatal("tied match must enter sudden death, not end")
	}
	if m.Wave() != WavesPerMatch {
		t.Fatalf("wave = %d, want sudden-death wave %d", m.Wave(), WavesPerMatch)
	}
	s := m.Snapshot()
	if s.Phase != "sudden_death" || !s.SuddenDeath {
		t.Fatalf("snapshot phase=%q sudden_death=%v", s.Phase, s.SuddenDeath)
	}
	if s.RemainingTimeMs <= 0 {
		t.Fatalf("sudden death wave must have a fresh time budget, got %d ms", s.RemainingTimeMs)
	}
	if m.freeCount() != CellsPerWave {
		t.Fatalf("sudden death board must be fresh, %d free cells", m.freeCount())
	}
}

// TestSuddenDeathFirstWordWins checks the core tiebreak rule: the first
// accepted word ends the match immediately with that seat winning.
func TestSuddenDeathFirstWordWins(t *testing.T) {
	// White-box setup so the rule is tested against a controlled board.
	m := mustMatch(t, "catdogpqrstu")
	m.suddenDeath = true
	m.inSuddenDeath = true
	m.wave = WavesPerMatch
	m.waveStart = m.tick
	path := findCells(m, 0, "cat")
	if path == nil {
		t.Fatal("cannot spell cat on the controlled board")
	}
	ev := m.Submit(0, path)
	if ev.Result != ResultAccepted {
		t.Fatalf("word rejected in sudden death: %s", ev.WordResultString)
	}
	if !m.IsOver() {
		t.Fatal("first accepted word must end sudden death immediately")
	}
	res := m.Result()
	if res.IsTie || res.WinnerSeat != 0 {
		t.Fatalf("result = %+v, want winner seat 0", res)
	}
}

// TestSuddenDeathTimeoutIsDraw checks that a tiebreak wave that runs out of
// time with no accepted word ends the match as a draw.
func TestSuddenDeathTimeoutIsDraw(t *testing.T) {
	m, err := New(suddenCfg())
	if err != nil {
		t.Fatal(err)
	}
	m.AdvanceTicks(WavesPerMatch*WaveTicks + WaveTicks)
	if !m.IsOver() {
		t.Fatal("timed-out sudden death must end the match")
	}
	res := m.Result()
	if !res.IsTie {
		t.Fatalf("result = %+v, want draw", res)
	}
}

// TestSuddenDeathBoardDeterminism checks the tiebreak board is generated
// deterministically from (seed, language, wave) like any other wave.
func TestSuddenDeathBoardDeterminism(t *testing.T) {
	a, err := New(suddenCfg())
	if err != nil {
		t.Fatal(err)
	}
	b, err := New(suddenCfg())
	if err != nil {
		t.Fatal(err)
	}
	a.AdvanceTicks(WavesPerMatch * WaveTicks)
	b.AdvanceTicks(WavesPerMatch * WaveTicks)
	if boardString(a) != boardString(b) {
		t.Fatalf("sudden-death board diverged: %q vs %q", boardString(a), boardString(b))
	}
	if boardString(a) == "rhsornertars" {
		t.Log("sudden-death board equals wave-0 board by chance (fine)")
	}
}

// TestSuddenDeathReplayProperty verifies that a full sudden-death match
// (tie -> tiebreak -> first word wins) replays to the identical final state.
func TestSuddenDeathReplayProperty(t *testing.T) {
	cfg := suddenCfg()
	m, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	m.AdvanceTicks(WavesPerMatch * WaveTicks)
	path := findAnyWord(m, 0)
	if path == nil {
		t.Fatalf("no 3-letter word on sudden-death board %q", boardString(m))
	}
	ev := m.Submit(0, path)
	if ev.Result != ResultAccepted {
		t.Fatalf("sudden-death word rejected: %s", ev.WordResultString)
	}
	if !m.IsOver() {
		t.Fatal("match must end after the first sudden-death word")
	}

	r, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range m.Events() {
		for r.Tick() < e.Tick {
			r.AdvanceTicks(1)
		}
		got := r.Submit(e.Seat, e.CellIDs)
		if got.Result != e.Result || got.ScoreAdded != e.ScoreAdded {
			t.Fatalf("replay seq %d: result %s/%d != %s/%d",
				e.Seq, got.WordResultString, got.ScoreAdded, e.WordResultString, e.ScoreAdded)
		}
	}
	for r.Tick() < m.Tick() {
		r.AdvanceTicks(1)
	}
	if r.Fingerprint() != m.Fingerprint() {
		t.Fatalf("replay fingerprint mismatch:\n orig=%s\n repl=%s", m.Fingerprint(), r.Fingerprint())
	}
	if !r.IsOver() {
		t.Fatal("replay must end in sudden death win")
	}
	if res := r.Result(); res.IsTie || res.WinnerSeat != 0 {
		t.Fatalf("replay result = %+v", res)
	}
}

// dictionary import guard (used in config types via tests above)
var _ = dictionary.En
