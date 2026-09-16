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

// M2 batch 35A: the rule's constants are per-match parameters with the 25/2/15
// batch 32C set as the zero-value default, so the calibration sweep (and any
// future documented provisional values) can vary them without touching code.

func catchUpMatchWithParams(t *testing.T, params CatchUpParams) *Match {
	t.Helper()
	m, err := NewWithBoard(Config{
		MatchID: 1, Seed: 1, Lang: "en", Seats: 4, AntiSnowball: true,
		CatchUp: params,
	}, "catdogfunrun")
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestCatchUpParamsZeroValueIsTheShippedSet(t *testing.T) {
	// The zero value must behave exactly like the batch 32C constants: a
	// seat 24 points behind is inside the 25-point gap and earns nothing.
	m := catchUpMatchWithParams(t, CatchUpParams{})
	if m.catchUp != DefaultCatchUpParams() {
		t.Fatalf("the zero-value config resolved to %v, want the 25/2/15 set", m.catchUp)
	}
	m.players[0].Score = int64(CatchUpGap) - 1
	ev := submitWord(t, m, 1, "cat")
	if ev.Result != ResultAccepted {
		t.Fatalf("fixture word was %v", ev.Result)
	}
	if ev.CatchUpBonus != 0 {
		t.Fatalf("a seat inside the default gap earned a bonus of %d", ev.CatchUpBonus)
	}
}

func TestCatchUpParamsGapControlsTheTrigger(t *testing.T) {
	// The same 15-point deficit: a bonus under a 10-point gap, none under the
	// default 25 - the gap parameter is the trigger, and only the trigger.
	m10 := catchUpMatchWithParams(t, CatchUpParams{Gap: 10})
	m10.players[0].Score = 15
	ev10 := submitWord(t, m10, 1, "cat")

	mDef := catchUpMatchWithParams(t, CatchUpParams{})
	mDef.players[0].Score = 15
	evDef := submitWord(t, mDef, 1, "cat")

	if ev10.CatchUpBonus <= 0 {
		t.Fatalf("a 15-point deficit under a 10-point gap earned no bonus")
	}
	if evDef.CatchUpBonus != 0 {
		t.Fatalf("the same deficit under the default gap earned a bonus of %d", evDef.CatchUpBonus)
	}
	// Only the trigger moved: the word and its own points are identical.
	if ev10.ScoreAdded-ev10.CatchUpBonus != evDef.ScoreAdded {
		t.Fatal("changing the gap changed the word's own points")
	}
}

func TestCatchUpParamsDivisorControlsTheRate(t *testing.T) {
	m := catchUpMatchWithParams(t, CatchUpParams{Gap: 5, Divisor: 3})
	m.players[0].Score = 200
	ev := submitWord(t, m, 1, "cat")
	base := ev.ScoreAdded - ev.CatchUpBonus
	if want := min64(base/3, int64(CatchUpMaxBonus)); ev.CatchUpBonus != want {
		t.Fatalf("bonus %d, want %d (base %d over divisor 3, capped at %d)",
			ev.CatchUpBonus, want, base, CatchUpMaxBonus)
	}
	// And the divisor-2 default awards more on the same base.
	m2 := catchUpMatchWithParams(t, CatchUpParams{Gap: 5})
	m2.players[0].Score = 200
	ev2 := submitWord(t, m2, 1, "cat")
	if ev2.CatchUpBonus <= ev.CatchUpBonus {
		t.Fatalf("divisor 3 (%d) does not award strictly less than divisor 2 (%d)",
			ev.CatchUpBonus, ev2.CatchUpBonus)
	}
}

func TestCatchUpParamsMaxBonusIsTheHardCap(t *testing.T) {
	m := catchUpMatchWithParams(t, CatchUpParams{Gap: 5, MaxBonus: 2})
	m.players[0].Score = 200
	ev := submitWord(t, m, 1, "cat")
	if ev.Result != ResultAccepted {
		t.Fatalf("fixture word was %v", ev.Result)
	}
	if ev.CatchUpBonus != 2 {
		t.Fatalf("bonus %d, want the cap of 2 (word base %d)", ev.CatchUpBonus, ev.ScoreAdded-ev.CatchUpBonus)
	}
}

func TestCatchUpParamsClampTheNonsensical(t *testing.T) {
	// A negative cap is "no bonus": the rule is on and would fire, but the
	// cap allows nothing. The rule must not crash or invent points.
	m := catchUpMatchWithParams(t, CatchUpParams{Gap: 1, MaxBonus: -5})
	m.players[0].Score = 200
	if ev := submitWord(t, m, 1, "cat"); ev.CatchUpBonus != 0 {
		t.Fatalf("a negative cap awarded a bonus of %d", ev.CatchUpBonus)
	}
	// Negative gap and divisor fall back to the shipped defaults.
	if p := (CatchUpParams{Gap: -1, Divisor: -1}).normalized(); p != DefaultCatchUpParams() {
		t.Fatalf("negative gap/divisor resolved to %v, want the default set", p)
	}
}

func TestCatchUpParamsAreVisibleInTheSnapshot(t *testing.T) {
	on := catchUpMatchWithParams(t, CatchUpParams{Gap: 10})
	if !on.Snapshot().AntiSnowball {
		t.Fatal("the snapshot does not show the rule on")
	}
	m, err := NewWithBoard(Config{MatchID: 2, Seed: 1, Lang: "en", Seats: 4}, "catdogfunrun")
	if err != nil {
		t.Fatal(err)
	}
	if m.Snapshot().AntiSnowball {
		t.Fatal("the snapshot shows the rule on for a default match")
	}
}

// --- batch 35D: the roster-scaled (leader-relative) trigger ----------------

func TestCatchUpParamsPercentScalesTheTriggerWithTheLeader(t *testing.T) {
	// The same 100-point deficit against a 1000-point leader: the legacy
	// 25-point trigger fires, a 20%-of-leader trigger (threshold 200) does
	// not. The score scale, not the roster, is what the rule must follow.
	// Leader 1000, trailing seat 900: deficit 100.
	mLegacy := catchUpMatchWithParams(t, CatchUpParams{})
	mLegacy.players[0].Score = 1000
	mLegacy.players[1].Score = 900
	evLegacy := submitWord(t, mLegacy, 1, "cat")

	mPct := catchUpMatchWithParams(t, CatchUpParams{GapPercent: 20})
	mPct.players[0].Score = 1000
	mPct.players[1].Score = 900
	evPct := submitWord(t, mPct, 1, "cat")

	if evLegacy.CatchUpBonus <= 0 {
		t.Fatal("the legacy trigger missed a 100-point deficit (regression)")
	}
	if evPct.CatchUpBonus != 0 {
		t.Fatalf("a 100-point deficit against a 20%%-of-leader trigger (threshold 200) earned %d", evPct.CatchUpBonus)
	}
	// Only the trigger moved: the word's own points are identical.
	if evLegacy.ScoreAdded-evLegacy.CatchUpBonus != evPct.ScoreAdded {
		t.Fatal("changing the trigger changed the word's own points")
	}
}

func TestCatchUpParamsPercentFloorGovernsTheEarlyGame(t *testing.T) {
	// A small leader score must not shrink the trigger below the absolute
	// floor: leader 10 at 40% would be 4 points, the floor keeps 25.
	m := catchUpMatchWithParams(t, CatchUpParams{GapPercent: 40})
	m.players[0].Score = 10
	ev := submitWord(t, m, 1, "cat")
	if ev.CatchUpBonus != 0 {
		t.Fatalf("an early-game deficit inside the 25-point floor earned %d", ev.CatchUpBonus)
	}
	// And at the floor boundary the percentage is inert: deficit 26 vs
	// threshold max(25, 4) = 25 fires exactly like the legacy rule.
	m2 := catchUpMatchWithParams(t, CatchUpParams{GapPercent: 40})
	m2.players[0].Score = 26
	if ev2 := submitWord(t, m2, 1, "cat"); ev2.CatchUpBonus <= 0 {
		t.Fatal("a 26-point deficit against a floor-25 trigger earned nothing")
	}
}

func TestCatchUpParamsZeroPercentIsTheLegacyTrigger(t *testing.T) {
	// Zero must be the shipped roster-blind behaviour at ANY leader score:
	// a 100-point deficit against a 1000-point leader fires (>= 25), a
	// 20-point deficit does not (< 25).
	m := catchUpMatchWithParams(t, CatchUpParams{})
	m.players[0].Score = 1000
	m.players[1].Score = 900
	if ev := submitWord(t, m, 1, "cat"); ev.CatchUpBonus <= 0 {
		t.Fatal("zero GapPercent must keep the 25-point trigger (100-point deficit)")
	}
	m2 := catchUpMatchWithParams(t, CatchUpParams{})
	m2.players[0].Score = 1000
	m2.players[1].Score = 980 // deficit 20
	if ev2 := submitWord(t, m2, 1, "cat"); ev2.CatchUpBonus != 0 {
		t.Fatalf("a 20-point deficit under the legacy 25-point trigger earned %d", ev2.CatchUpBonus)
	}
}

func TestCatchUpParamsNegativePercentClampsToLegacy(t *testing.T) {
	// Negative percentages are nonsensical input from a future caller; the
	// clamp keeps them from meaning "always fire".
	if p := (CatchUpParams{GapPercent: -50}).normalized(); p.GapPercent != 0 {
		t.Fatalf("normalized GapPercent = %d, want 0", p.GapPercent)
	}
	m := catchUpMatchWithParams(t, CatchUpParams{GapPercent: -50})
	m.players[0].Score = 1000
	m.players[1].Score = 900 // deficit 100 >= 25 fires
	if ev := submitWord(t, m, 1, "cat"); ev.CatchUpBonus <= 0 {
		t.Fatal("a clamped negative percentage must behave exactly like zero (100-point deficit fires)")
	}
	m2 := catchUpMatchWithParams(t, CatchUpParams{GapPercent: -50})
	m2.players[0].Score = 1000
	m2.players[1].Score = 980 // deficit 20 < 25
	if ev2 := submitWord(t, m2, 1, "cat"); ev2.CatchUpBonus != 0 {
		t.Fatalf("a clamped negative percentage changed the trigger (earned %d)", ev2.CatchUpBonus)
	}
}

func TestCatchUpScaledTriggerIsDeterministicInTheFingerprint(t *testing.T) {
	// The scaled trigger must replay exactly: two matches, same seed, same
	// params, same driven words - identical fingerprints; and the percentage
	// must be visible in competitive state (a different pct is allowed to
	// produce a different fingerprint when it changes any score).
	run := func() string {
		m := catchUpMatchWithParams(t, CatchUpParams{GapPercent: 25})
		m.players[0].Score = 400
		submitWord(t, m, 1, "cat")
		submitWord(t, m, 1, "dog")
		return m.Fingerprint()
	}
	if a, b := run(), run(); a != b {
		t.Fatal("the scaled trigger is not deterministic")
	}
}
