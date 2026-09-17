package main

import (
	"reflect"
	"testing"

	"github.com/JoTalbot/words/server/internal/match"
)

// TestDriverIsDeterministic: the same (seed, roster, set, policy) tuple must
// produce the same match every time - PD-003 is what makes the sweep
// comparable across re-runs and sharding trustworthy. A two-seat match is
// enough: the driver, the policy and the room path are all the same code
// the grid runs, only smaller.
func TestDriverIsDeterministic(t *testing.T) {
	a := driver(2, 4131, policies["aggressive"], true, match.CatchUpParams{}, match.BoardParams{}, 1)
	b := driver(2, 4131, policies["aggressive"], true, match.CatchUpParams{}, match.BoardParams{}, 1)
	c := driver(2, 4131, policies["aggressive"], false, match.CatchUpParams{}, match.BoardParams{}, 1)
	// WallMillis is measurement, not game state: compare everything else.
	a.WallMillis, b.WallMillis, c.WallMillis = 0, 0, 0
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("the same match produced different records:\n%+v\n%+v", a, b)
	}
	if reflect.DeepEqual(a, c) {
		t.Fatal("the rule on and off produced identical matches: the flag is a no-op")
	}

	// Batch 36D added a second family of arms, so the same two properties have
	// to hold for them: an omitted board is the shipped board, and a board arm
	// is not a no-op. The zero-value check is exact; the no-op check walks a few
	// seeds, because whether a two-seat match contains a steal at all is a
	// property of the seed, and a test that fails on one unlucky seed would be
	// reporting the fixture rather than the code.
	e := driver(2, 4131, policies["aggressive"], false, match.CatchUpParams{}, match.DefaultBoardParams(), 1)
	e.WallMillis = 0
	if !reflect.DeepEqual(c, e) {
		t.Fatal("an explicit default board changed the match; the sweep baseline would not be the shipped rules")
	}
	var moved bool
	for seed := int64(4100); seed < 4112 && !moved; seed++ {
		x := driver(2, seed, policies["aggressive"], false, match.CatchUpParams{}, match.BoardParams{}, uint64(seed))
		y := driver(2, seed, policies["aggressive"], false, match.CatchUpParams{},
			match.BoardParams{StealDebitPercent: -1}, uint64(seed))
		x.WallMillis, y.WallMillis = 0, 0
		moved = !reflect.DeepEqual(x, y)
	}
	if !moved {
		t.Fatal("removing the steal debit entirely changed no match in twelve seeds: the board lever is a no-op")
	}
}
