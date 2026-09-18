package main

import (
	"testing"

	"github.com/JoTalbot/words/server/internal/match"
)

func TestWordLenHistBucketPlacement(t *testing.T) {
	var w wordLenHist

	// bucket boundaries: 3, 4, 5, 6, 7, 8, 9, 10 (exclusive upper)
	cases := []struct {
		res   match.WordResult
		cells int
		want  string // expected bucket key
	}{
		{match.ResultAccepted, 2, "le_3"},
		{match.ResultAccepted, 3, "le_4"},
		{match.ResultAccepted, 9, "le_10"},
		{match.ResultAccepted, 10, "le_inf"},
		{match.ResultRejectedNotInDict, 4, "le_5"},
		{match.ResultBlockedByRule, 7, "le_8"},
		{match.ResultInvalidInput, 11, "le_inf"},
	}
	for _, tc := range cases {
		w.record(tc.res, tc.cells)
	}
	s := w.snapshot()

	get := func(h wordHistSnapshot) map[string]uint64 { return h.Buckets }

	// accepted got 2,3,9,10 -> le_inf count 4, sum 2+3+9+10=24
	acc := s.Accepted
	if acc.Count != 4 || acc.SumCells != 24 {
		t.Fatalf("accepted: count=%d sum=%d want 4/24", acc.Count, acc.SumCells)
	}
	if get(acc)["le_3"] != 1 || get(acc)["le_4"] != 2 || get(acc)["le_10"] != 3 || get(acc)["le_inf"] != 4 {
		t.Fatalf("accepted buckets wrong: %v", acc.Buckets)
	}
	if s.RejectedNotInDict.Count != 1 || s.BlockedByRule.Count != 1 || s.InvalidInput.Count != 1 {
		t.Fatalf("outcome classes not routed: %+v", s)
	}
	if get(s.InvalidInput)["le_inf"] != 1 {
		t.Fatalf("invalid input 11 cells should be overflow: %v", s.InvalidInput.Buckets)
	}
}

func TestWordLenHistMatchNotActiveNotRecorded(t *testing.T) {
	var w wordLenHist
	w.record(match.ResultMatchNotActive, 5)
	s := w.snapshot()
	if s.Accepted.Count+s.RejectedNotInDict.Count+s.BlockedByRule.Count+s.InvalidInput.Count != 0 {
		t.Fatalf("MATCH_NOT_ACTIVE must not be recorded: %+v", s)
	}
}

func TestWordLenPrometheusLines(t *testing.T) {
	var w wordLenHist
	w.record(match.ResultAccepted, 2)
	lines := w.accepted.prometheusLines("wordarena_intent_wordlen_accepted_cells")
	// first bucket line and the terminal count line must exist
	if lines[0] != `wordarena_intent_wordlen_accepted_cells_bucket{le="3"} 1` {
		t.Fatalf("first bucket line: %q", lines[0])
	}
	last := lines[len(lines)-1]
	if last != "wordarena_intent_wordlen_accepted_cells_count 1" {
		t.Fatalf("last line: %q", last)
	}
	// len(wordLenBounds) bucket lines + le="+Inf" + _sum + _count.
	if len(lines) != len(wordLenBounds)+3 {
		t.Fatalf("prometheusLines returned %d lines, want %d", len(lines), len(wordLenBounds)+3)
	}
}
