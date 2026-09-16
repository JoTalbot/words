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
	a := driver(2, 4131, policies["aggressive"], true, match.CatchUpParams{}, 1)
	b := driver(2, 4131, policies["aggressive"], true, match.CatchUpParams{}, 1)
	c := driver(2, 4131, policies["aggressive"], false, match.CatchUpParams{}, 1)
	// WallMillis is measurement, not game state: compare everything else.
	a.WallMillis, b.WallMillis, c.WallMillis = 0, 0, 0
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("the same match produced different records:\n%+v\n%+v", a, b)
	}
	if reflect.DeepEqual(a, c) {
		t.Fatal("the rule on and off produced identical matches: the flag is a no-op")
	}
}
