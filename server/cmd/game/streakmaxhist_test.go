package main

import (
	"strconv"
	"testing"
)

func TestStreakMaxHistBucketPlacement(t *testing.T) {
	var h streakMaxHist
	// bucket boundaries: 2, 4, 8, 12, 16, 20, 40 (exclusive upper, cumulative).
	cases := []struct {
		max  int
		want string
	}{
		{1, "le_2"},   // a single rejection
		{3, "le_4"},   // human band
		{7, "le_8"},   //
		{11, "le_12"}, // deep but below the signal threshold
		{19, "le_20"}, // just under rejectionStreakSignalAt (20)
		{20, "le_40"}, // exactly the signal threshold: in le_40, not le_20
		{31, "le_40"}, //
		{40, "le_inf"}, // overflow (>= 40)
	}
	for _, tc := range cases {
		h.record(tc.max)
	}
	s := h.snapshot()
	if s.Count != 8 || s.SumStreaks != 1+3+7+11+19+20+31+40 {
		t.Fatalf("count/sum: got %d/%d", s.Count, s.SumStreaks)
	}
	cum := uint64(0)
	for i, ub := range streakMaxBounds {
		cum += h.buckets[i].Load()
		if got := s.Buckets[ubKey(ub)]; got != cum {
			t.Fatalf("bucket le_%d: want %d (cumulative), got %d", ub, cum, got)
		}
	}
	cum += h.buckets[len(h.buckets)-1].Load()
	if got := s.Buckets["le_inf"]; got != cum {
		t.Fatalf("le_inf: want %d, got %d", cum, got)
	}
	// Explicit placement checks for the boundary semantics.
	if s.Buckets["le_20"] != 5 {
		t.Fatalf("le_20 (cumulative) = %d, want 5 (1,3,7,11,19)", s.Buckets["le_20"])
	}
	if s.Buckets["le_40"] != 7 {
		t.Fatalf("le_40 (cumulative) = %d, want 7", s.Buckets["le_40"])
	}
}

func TestStreakMaxHistZeroNeverRecordedByCaller(t *testing.T) {
	// The histogram itself would accept 0 via record(0); the invariant that
	// clean matches stay quiet is enforced at the call site (clear only
	// records a non-zero max). This test pins the record shape for a value a
	// caller might pass, and the close-path tests pin the call-site rule.
	var h streakMaxHist
	h.record(0)
	if h.snapshot().Count != 1 {
		t.Fatalf("record(0) must still be a valid observation at the histogram layer; count = %d", h.snapshot().Count)
	}
}

func TestStreakMaxPrometheusLines(t *testing.T) {
	var h streakMaxHist
	h.record(20)
	lines := h.prometheusLines("wordarena_behavior_streak_max_per_match")
	if lines[0] != `wordarena_behavior_streak_max_per_match_bucket{le="2"} 0` {
		t.Fatalf("first bucket line: %q", lines[0])
	}
	if last := lines[len(lines)-1]; last != "wordarena_behavior_streak_max_per_match_count 1" {
		t.Fatalf("last line: %q", last)
	}
	if len(lines) != len(streakMaxBounds)+3 {
		t.Fatalf("prometheusLines returned %d lines, want %d", len(lines), len(streakMaxBounds)+3)
	}
	// The +Inf bucket equals the count (cumulative).
	if lines[len(lines)-3] != `wordarena_behavior_streak_max_per_match_bucket{le="+Inf"} 1` {
		t.Fatalf("+Inf bucket line: %q", lines[len(lines)-3])
	}
}

func ubKey(ub int64) string {
	return "le_" + strconv.FormatInt(ub, 10)
}
