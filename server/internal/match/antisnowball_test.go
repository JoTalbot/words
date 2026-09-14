package match

import (
	"reflect"
	"testing"
)

// M2 anti-snowball tests. The rule is opt-in, so most of these guard the
// properties that make shipping it safe rather than the rule itself: off by
// default, deterministic, capped, and - the one that is easy to get wrong -
// it must not re-price steals.

// catchUpMatch builds a 4-seat match with an explicit board. The caller sets
// scores directly to place a leader; the board spells "cat" and "dog".
func catchUpMatch(t *testing.T, antiSnowball bool) *Match {
	t.Helper()
	m, err := NewWithBoard(Config{
		MatchID: 1, Seed: 1, Lang: "en", Seats: 4, AntiSnowball: antiSnowball,
	}, "catdogfunrun")
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func submitWord(t *testing.T, m *Match, seat Seat, word string) Event {
	t.Helper()
	path := findCells(m, seat, word)
	if path == nil {
		t.Fatalf("fixture board cannot spell %q for seat %d", word, seat)
	}
	return m.Submit(seat, path)
}

func TestAntiSnowballIsOffByDefault(t *testing.T) {
	m := catchUpMatch(t, false)
	if m.AntiSnowball() {
		t.Fatal("the catch-up rule is enabled by default; every M0/M1 baseline and replay would move")
	}
	m.players[0].Score = 500
	ev := submitWord(t, m, 1, "cat")
	if ev.Result != ResultAccepted {
		t.Fatalf("fixture word was %v", ev.Result)
	}
	if ev.CatchUpBonus != 0 {
		t.Fatalf("a default match awarded a catch-up bonus of %d", ev.CatchUpBonus)
	}
	if ev.TotalScore != ev.ScoreAdded {
		t.Fatalf("score %d does not equal the word's own points %d", ev.TotalScore, ev.ScoreAdded)
	}
}

func TestCatchUpBonusAppliesWhenFarBehind(t *testing.T) {
	m := catchUpMatch(t, true)
	if !m.AntiSnowball() {
		t.Fatal("the opt-in rule did not engage")
	}
	m.players[0].Score = int64(CatchUpGap) + 100 // leader well beyond the gap

	ev := submitWord(t, m, 1, "cat")
	if ev.Result != ResultAccepted {
		t.Fatalf("fixture word was %v", ev.Result)
	}
	if ev.CatchUpBonus <= 0 {
		t.Fatalf("a seat %d points behind the leader earned no bonus", CatchUpGap+100)
	}
	base := ev.ScoreAdded - ev.CatchUpBonus
	if want := base / CatchUpBonusDivisor; ev.CatchUpBonus != min64(int64(want), CatchUpMaxBonus) {
		t.Fatalf("bonus %d, want %d of the word's %d points (capped at %d)",
			ev.CatchUpBonus, want, base, CatchUpMaxBonus)
	}
	if ev.TotalScore != ev.ScoreAdded {
		t.Fatalf("total %d != credited %d: the bonus was not folded into the score", ev.TotalScore, ev.ScoreAdded)
	}
	if m.players[1].Score != ev.ScoreAdded {
		t.Fatalf("player score %d != event score %d", m.players[1].Score, ev.ScoreAdded)
	}
}

func TestCatchUpBonusNotAwardedInsideTheGap(t *testing.T) {
	m := catchUpMatch(t, true)
	m.players[0].Score = CatchUpGap - 1
	if ev := submitWord(t, m, 1, "cat"); ev.CatchUpBonus != 0 {
		t.Fatalf("a seat %d points behind got a bonus of %d; the gap is %d",
			CatchUpGap-1, ev.CatchUpBonus, CatchUpGap)
	}

	// The leader never tops themselves up, however far ahead they are.
	m2 := catchUpMatch(t, true)
	m2.players[0].Score = 900
	if ev := submitWord(t, m2, 0, "cat"); ev.CatchUpBonus != 0 {
		t.Fatalf("the leader awarded themselves a catch-up bonus of %d", ev.CatchUpBonus)
	}
}

func TestCatchUpBonusIsCapped(t *testing.T) {
	// A long word on a fresh board is worth far more than a short one; without
	// the cap the mechanic would hand the game to whoever is furthest behind
	// the moment a big board opens. The cap is asserted on the rule itself,
	// with the word values a fresh board can actually produce.
	m := catchUpMatch(t, true)
	m.players[0].Score = 1000

	for _, points := range []int{CatchUpMaxBonus * 2, 100, 10_000} {
		if got := m.CatchUpBonus(1, points); got != CatchUpMaxBonus {
			t.Fatalf("a %d-point word earned a bonus of %d, want the cap of %d",
				points, got, CatchUpMaxBonus)
		}
	}
	// Below the cap the bonus really is half again, truncated by integer
	// division so the result cannot depend on floating point.
	for _, tc := range []struct{ points, want int }{{2, 1}, {9, 4}, {29, 14}, {30, CatchUpMaxBonus}} {
		if got := m.CatchUpBonus(1, tc.points); got != tc.want {
			t.Fatalf("a %d-point word earned %d, want %d", tc.points, got, tc.want)
		}
	}
	// The cap is reachable through a real submission too, which is what keeps
	// this from being a unit test of a function nobody calls.
	ev := submitWord(t, m, 1, "cat")
	if ev.Result != ResultAccepted {
		t.Fatalf("fixture word was %v", ev.Result)
	}
	if ev.CatchUpBonus <= 0 || ev.CatchUpBonus > CatchUpMaxBonus {
		t.Fatalf("a real submission earned a bonus of %d, want 1..%d", ev.CatchUpBonus, CatchUpMaxBonus)
	}
}

func TestCatchUpBonusIgnoresEliminatedLeaders(t *testing.T) {
	m := catchUpMatch(t, true)
	// The only seat far ahead is a spectator whose score is frozen. Living
	// seats are level, so nobody is behind anyone.
	m.players[0].Score = 500
	m.players[0].EliminatedAtWave = 0

	if ev := submitWord(t, m, 1, "cat"); ev.CatchUpBonus != 0 {
		t.Fatalf("an eliminated seat's frozen score defined the gap; bonus %d", ev.CatchUpBonus)
	}
}

// TestCatchUpDoesNotRepriceSteals is the property that makes this rule small.
// The bonus is added to a score and never to a cell's CreditedValue, so a later
// steal must debit exactly what it would have debited without the rule. The
// test runs the same submissions twice - rule off, rule on - and requires the
// victim's two scores to differ by exactly the bonus and nothing else.
func TestCatchUpDoesNotRepriceSteals(t *testing.T) {
	run := func(antiSnowball bool) (victimEarly, victimLate, bonus int64) {
		m := catchUpMatch(t, antiSnowball)
		m.players[0].Score = int64(CatchUpGap) + 100 // a leader nobody can reach
		claim := submitWord(t, m, 1, "cat")
		victimEarly = m.players[1].Score
		if claim.Result != ResultAccepted {
			t.Fatalf("claim was %v", claim.Result)
		}
		// A rival steals one of the victim's cells. Claims are locked for
		// LockTicks, and a locked cell cannot be stolen, so the match has to
		// advance past the lock first.
		m.AdvanceTicks(LockTicks + 1)
		// findCells mirrors "cells a legitimate client may use", which
		// excludes rival-owned cells, so a steal path is spelled directly:
		// cells 0,1,2 are c,a,t on the fixture board.
		steal := m.Submit(2, []int{0, 1, 2})
		if steal.Result != ResultAccepted {
			t.Fatalf("steal was %v", steal.Result)
		}
		if !steal.IsSteal {
			t.Fatal("the second claim on the same cells was not recorded as a steal")
		}
		return victimEarly, m.players[1].Score, claim.CatchUpBonus
	}

	offEarly, offLate, offBonus := run(false)
	onEarly, onLate, onBonus := run(true)

	if offBonus != 0 {
		t.Fatalf("the control match awarded a bonus of %d", offBonus)
	}
	if onBonus <= 0 {
		t.Fatal("the rule never engaged, so the comparison proves nothing")
	}
	if got := onEarly - offEarly; got != onBonus {
		t.Fatalf("before the steal the scores differ by %d, want exactly the bonus %d", got, onBonus)
	}
	if got := onLate - offLate; got != onBonus {
		t.Fatalf("after the steal the scores differ by %d, want exactly the bonus %d - "+
			"the steal debit changed, which means the bonus leaked into CreditedValue", got, onBonus)
	}
	if offLate != 0 {
		t.Fatalf("control victim ended on %d; the fixture expects their cells to be fully stolen", offLate)
	}
}

func TestAntiSnowballIsDeterministicAndParticipatesInTheFingerprint(t *testing.T) {
	play := func(antiSnowball bool) (*Match, []Event) {
		m := catchUpMatch(t, antiSnowball)
		m.players[0].Score = int64(CatchUpGap) + 40
		submitWord(t, m, 1, "cat")
		submitWord(t, m, 2, "dog")
		return m, m.Events()
	}

	mA, evA := play(true)
	mB, evB := play(true)
	if mCfg := mB.Fingerprint(); mA.Fingerprint() != mCfg {
		t.Fatal("two identical runs of the rule diverged; the rule is not a pure function of match state")
	}
	if !reflect.DeepEqual(evA, evB) {
		t.Fatalf("the same submissions produced different events:\n%v\n%v", evA, evB)
	}

	mOff, _ := play(false)
	if mA.Fingerprint() == mOff.Fingerprint() {
		t.Fatal("enabling the rule produced the same fingerprint: a divergent score could replay equal")
	}
	if mA.Players()[1].Score <= mOff.Players()[1].Score {
		t.Fatal("the rule was enabled but changed nothing observable")
	}
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}
