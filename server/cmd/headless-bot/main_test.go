package main

import (
	"errors"
	"testing"

	wordarenav1 "github.com/JoTalbot/words/server/gen/wordarena/net/v1"
	"google.golang.org/protobuf/proto"
)

func TestMsgsEqual(t *testing.T) {
	cell := func(id uint32, letter string, owner uint64, locked bool) *wordarenav1.BoardCell {
		return &wordarenav1.BoardCell{CellId: id, Letter: letter, OwnerUserId: owner, IsLocked: locked}
	}
	a := []*wordarenav1.BoardCell{cell(1, "a", 0, false), cell(2, "b", 7, true)}

	if !msgsEqual(a, []*wordarenav1.BoardCell{cell(1, "a", 0, false), cell(2, "b", 7, true)}) {
		t.Error("identical boards reported as different")
	}
	// Ownership is what a cross-steal changes, so it must be visible here: this
	// helper is what tells a real board divergence from a clock-only one.
	if msgsEqual(a, []*wordarenav1.BoardCell{cell(1, "a", 0, false), cell(2, "b", 9, true)}) {
		t.Error("differing owner reported as equal")
	}
	if msgsEqual(a, []*wordarenav1.BoardCell{cell(1, "a", 0, false), cell(2, "b", 7, false)}) {
		t.Error("differing lock state reported as equal")
	}
	if msgsEqual(a, []*wordarenav1.BoardCell{cell(1, "a", 0, false)}) {
		t.Error("different lengths reported as equal")
	}
	if !msgsEqual[proto.Message](nil, nil) {
		t.Error("two empty boards reported as different")
	}
}

// TestSolveSeedClassifiesAnUncoverableBoard pins the behaviour batch 22b exists
// to separate. Seed 13868347868007641273 was observed live on arm-server-01 on
// 2026-09-10 reporting "wave 2 not fully claimable": the offline planner cannot
// cover that board with dictionary words. That is a harness precondition on an
// arbitrary board, not a server fault, so it must be a typed error a caller can
// classify - not a bare error string indistinguishable from a real divergence.
func TestSolveSeedClassifiesAnUncoverableBoard(t *testing.T) {
	_, _, err := solveSeed("en", 13868347868007641273)
	if err == nil {
		t.Fatal("this seed was expected to be uncoverable; if the dictionary or the planner changed, pick a new fixture and say so")
	}
	var nc *errNotCoverable
	if !errors.As(err, &nc) {
		t.Fatalf("err = %v (%T), want *errNotCoverable so callers can skip instead of fail", err, err)
	}
	if nc.seed != 13868347868007641273 {
		t.Errorf("nc.seed = %d, want the requested seed", nc.seed)
	}
}

// TestSolveSeedStillSolvesCuratedSeeds guards the other direction: classifying
// the planner's precondition must not start swallowing genuine solve failures on
// the seeds the exit gate depends on.
func TestSolveSeedStillSolvesCuratedSeeds(t *testing.T) {
	for _, seed := range []uint64{1512, 1513, 1517} {
		moves, want, err := solveSeed("en", seed)
		if err != nil {
			t.Fatalf("seed %d: %v", seed, err)
		}
		if len(moves) == 0 {
			t.Errorf("seed %d: solved to zero moves", seed)
		}
		if want[0]+want[1] == 0 {
			t.Errorf("seed %d: solved to a zero total score", seed)
		}
	}
}
