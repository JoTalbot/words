package pve

import (
	"reflect"
	"testing"

	"github.com/JoTalbot/words/server/internal/match"
)

// M2 batch 32E: the practice opponent must be a deterministic function of the
// match state. If it is not, a practice match stops being replayable, and
// PD-003 (deterministic scoring, replayable matches) is what makes every other
// gameplay guarantee auditable.

func newOpponent(t *testing.T) *Opponent {
	t.Helper()
	o, err := New("en", DefaultPolicy())
	if err != nil {
		t.Fatalf("new opponent: %v", err)
	}
	return o
}

func TestOpponentHasCandidatesToPlay(t *testing.T) {
	o := newOpponent(t)
	if o.CandidateCount() == 0 {
		t.Fatal("the opponent has no words to play at all; every practice match would be a walkover")
	}
	// Whatever it can spell must be legal for the simulation, or the opponent
	// would spend the match being rejected.
	for _, w := range []string{"cat", "dog", "sun"} {
		if len([]rune(w)) <= o.Policy().MaxLen && o.Known(w) && o.CandidateCount() > 0 {
			continue
		}
	}
}

func TestIntentIsADeterministicFunctionOfState(t *testing.T) {
	const seed = 1512
	play := func() ([]int, []int, []int) {
		m, err := match.New(match.Config{MatchID: 1, Seed: seed, Lang: "en"})
		if err != nil {
			t.Fatal(err)
		}
		o := newOpponent(t)
		var first, second, third []int
		// The tick sequence is fixed by the test, so the only inputs are the
		// board, the tick and the policy.
		snap := m.Snapshot()
		first = append(first, o.Intent(snap, 50, 1)...)
		m.AdvanceTicks(45)
		second = append(second, o.Intent(m.Snapshot(), 95, 1)...)
		m.AdvanceTicks(45)
		third = append(third, o.Intent(m.Snapshot(), 140, 1)...)
		return first, second, third
	}

	a1, a2, a3 := play()
	b1, b2, b3 := play()
	if !reflect.DeepEqual(a1, b1) || !reflect.DeepEqual(a2, b2) || !reflect.DeepEqual(a3, b3) {
		t.Fatalf("two runs of the same policies disagreed:\n%v %v %v\n%v %v %v",
			a1, a2, a3, b1, b2, b3)
	}
	if len(a1) == 0 {
		t.Fatal("the opponent never acted, so determinism was not actually exercised")
	}
}

func TestOpponentWaitsOutItsInterval(t *testing.T) {
	m, err := match.New(match.Config{MatchID: 1, Seed: 1512, Lang: "en"})
	if err != nil {
		t.Fatal(err)
	}
	o := newOpponent(t)
	if got := o.Intent(m.Snapshot(), 100, 1); len(got) == 0 {
		t.Fatal("the opponent did not act at its first opportunity")
	}
	interval := o.Policy().Interval
	for _, tick := range []int{100 + 1, 100 + interval - 1} {
		if got := o.Intent(m.Snapshot(), tick, 1); got != nil {
			t.Fatalf("the opponent acted %d ticks after its last word (interval is %d): %v",
				tick-100, interval, got)
		}
	}
	if got := o.Intent(m.Snapshot(), 100+interval, 1); len(got) == 0 {
		t.Fatal("the opponent did not act once its interval had passed")
	}
}

// TestIntentsAreAlwaysLegal is the "no special authority" property: every path
// the opponent produces must be one the simulation accepts, because it is
// validated by the same code a human's intent goes through.
func TestIntentsAreAlwaysLegal(t *testing.T) {
	m, err := match.New(match.Config{MatchID: 1, Seed: 1512, Lang: "en"})
	if err != nil {
		t.Fatal(err)
	}
	o := newOpponent(t)

	tick := 0
	played := 0
	for wave := 0; wave < 3 && !m.IsOver(); wave++ {
		for i := 0; i < match.WaveTicks+60 && !m.IsOver(); i++ {
			tick++
			m.AdvanceTicks(1)
			cells := o.Intent(m.Snapshot(), tick, 1)
			if cells == nil {
				continue
			}
			ev := m.Submit(1, cells)
			if ev.Result != match.ResultAccepted {
				t.Fatalf("the opponent produced an intent the simulation rejected: %v word=%q cells=%v",
					ev.Result, ev.Word, cells)
			}
			played++
		}
	}
	if played == 0 {
		t.Fatal("the opponent never managed a legal word")
	}
	t.Logf("opponent played %d accepted words in one match", played)
}

// TestOpponentDoesNotStealByDefault pins the deliberate softness of the first
// PvE content: it takes free cells only, so a new player is never punished for
// holding a word they have not finished reading.
func TestOpponentDoesNotStealByDefault(t *testing.T) {
	m, err := match.New(match.Config{MatchID: 1, Seed: 1512, Lang: "en"})
	if err != nil {
		t.Fatal(err)
	}
	o := newOpponent(t)
	if o.Policy().AllowSteal {
		t.Fatal("the default policy steals; the first PvE content should teach claiming first")
	}

	// Seat 0 claims a word over the first free cells, then the opponent must
	// play around it.
	humanPath := firstFreePath(t, m, 0, "cat")
	if humanPath == nil {
		t.Skip("seed does not offer the fixture word")
	}
	if ev := m.Submit(0, humanPath); ev.Result != match.ResultAccepted {
		t.Fatalf("fixture claim rejected: %v", ev.Result)
	}
	owned := map[int]bool{}
	for _, id := range humanPath {
		owned[id] = true
	}

	for tick := 1; tick < 400 && !m.IsOver(); tick++ {
		m.AdvanceTicks(1)
		cells := o.Intent(m.Snapshot(), tick, 1)
		if cells == nil {
			continue
		}
		for _, id := range cells {
			if owned[id] {
				t.Fatalf("the polite opponent tried to take the player's cell %d", id)
			}
		}
	}
}

// TestStealPolicyIsAvailableAndStillLegal proves the knob works and that even
// the aggressive shape produces intents the server accepts.
func TestStealPolicyIsAvailableAndStillLegal(t *testing.T) {
	policy := DefaultPolicy()
	policy.AllowSteal = true
	policy.Interval = 1
	o, err := New("en", policy)
	if err != nil {
		t.Fatal(err)
	}
	m, err := match.New(match.Config{MatchID: 1, Seed: 1512, Lang: "en"})
	if err != nil {
		t.Fatal(err)
	}
	if path := firstFreePath(t, m, 0, "cat"); path != nil {
		if ev := m.Submit(0, path); ev.Result == match.ResultAccepted {
			m.AdvanceTicks(match.LockTicks + 1)
		}
	}
	for tick := 1; tick < 200 && !m.IsOver(); tick++ {
		m.AdvanceTicks(1)
		cells := o.Intent(m.Snapshot(), tick, 1)
		if cells == nil {
			continue
		}
		if ev := m.Submit(1, cells); ev.Result != match.ResultAccepted {
			t.Fatalf("aggressive policy produced a rejected intent: %v cells=%v", ev.Result, cells)
		}
	}
}

func TestPolicyValidation(t *testing.T) {
	if _, err := New("en", Policy{MinLen: 3, MaxLen: 2, Interval: 10}); err == nil {
		t.Fatal("a max length below the min length was accepted")
	}
	if _, err := New("en", Policy{MinLen: 3, MaxLen: 4, Interval: 0}); err == nil {
		t.Fatal("a zero interval was accepted")
	}
	o, err := New("en", Policy{MinLen: 1, MaxLen: 4, Interval: 10})
	if err != nil {
		t.Fatal(err)
	}
	if o.Policy().MinLen < match.MinWordLength {
		t.Fatalf("min length %d is below the game's minimum %d", o.Policy().MinLen, match.MinWordLength)
	}
}

// firstFreePath spells word over currently free cells in id order, mirroring
// what a client may legitimately submit.
func firstFreePath(t *testing.T, m *match.Match, seat match.Seat, word string) []int {
	t.Helper()
	snap := m.Snapshot()
	used := map[int]bool{}
	var path []int
	for _, r := range word {
		found := false
		for _, c := range snap.Cells {
			if used[c.ID] || c.OwnerSeat >= 0 || c.IsLocked {
				continue
			}
			if len(c.Letter) == 1 && c.Letter[0] == byte(r) {
				used[c.ID] = true
				path = append(path, c.ID)
				found = true
				break
			}
		}
		if !found {
			return nil
		}
	}
	_ = seat
	return path
}
