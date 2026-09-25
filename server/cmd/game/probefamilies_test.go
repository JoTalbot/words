package main

import (
	"strconv"
	"testing"
)

func TestProbeMaxHistBucketPlacement(t *testing.T) {
	var h probeMaxHist
	// bucket boundaries: 2, 3, 4, 5, 6, 8, 10 (exclusive upper, cumulative).
	cases := []struct {
		depth int
		want  string
	}{
		{1, "le_2"},   // a single identical rejection (retype), below threshold
		{2, "le_3"},   //
		{4, "le_5"},   // deepest episode just under the signal threshold
		{5, "le_6"},   // exactly at wordProbeRepeatAt
		{7, "le_8"},   //
		{9, "le_10"},  //
		{10, "le_inf"}, // overflow
	}
	for _, tc := range cases {
		h.record(tc.depth)
	}
	s := h.snapshot()
	if s.Count != 7 || s.SumDepth != 1+2+4+5+7+9+10 {
		t.Fatalf("count/sum: got %d/%d", s.Count, s.SumDepth)
	}
	cum := uint64(0)
	for i, ub := range probeMaxBounds {
		cum += h.buckets[i].Load()
		if got := s.Buckets["le_"+strconv.FormatInt(ub, 10)]; got != cum {
			t.Fatalf("bucket le_%d: want %d (cumulative), got %d", ub, cum, got)
		}
	}
	cum += h.buckets[len(h.buckets)-1].Load()
	if got := s.Buckets["le_inf"]; got != cum {
		t.Fatalf("le_inf: want %d, got %d", cum, got)
	}
	// le_5 counts only matches whose deepest episode stayed under 5.
	if s.Buckets["le_5"] != 3 {
		t.Fatalf("le_5 (cumulative) = %d, want 3", s.Buckets["le_5"])
	}
}

func TestProbeMaxPrometheusLines(t *testing.T) {
	var h probeMaxHist
	h.record(5)
	lines := h.prometheusLines("wordarena_behavior_probe_max_per_match")
	if lines[0] != `wordarena_behavior_probe_max_per_match_bucket{le="2"} 0` {
		t.Fatalf("first bucket line: %q", lines[0])
	}
	if last := lines[len(lines)-1]; last != "wordarena_behavior_probe_max_per_match_count 1" {
		t.Fatalf("last line: %q", last)
	}
	if len(lines) != len(probeMaxBounds)+3 {
		t.Fatalf("prometheusLines returned %d lines, want %d", len(lines), len(probeMaxBounds)+3)
	}
}

func TestFamiliesMaxHistBucketPlacement(t *testing.T) {
	var h familiesMaxHist
	// bucket boundaries: 2, 3, 4 (exclusive upper, cumulative).
	cases := []struct {
		fams int
	}{
		{1}, // one family: flagged but never joined
		{2}, // exactly the multiSignalArity join
		{3}, //
		{4}, // overflow: three or more distinct families
	}
	for _, tc := range cases {
		h.record(tc.fams)
	}
	s := h.snapshot()
	if s.Count != 4 || s.SumFamilies != 1+2+3+4 {
		t.Fatalf("count/sum: got %d/%d", s.Count, s.SumFamilies)
	}
	if s.Buckets["le_2"] != 1 || s.Buckets["le_3"] != 2 || s.Buckets["le_4"] != 3 || s.Buckets["le_inf"] != 4 {
		t.Fatalf("cumulative buckets wrong: %v", s.Buckets)
	}
}

func TestFamiliesMaxPrometheusLines(t *testing.T) {
	var h familiesMaxHist
	h.record(3)
	lines := h.prometheusLines("wordarena_behavior_families_max_per_match")
	if lines[0] != `wordarena_behavior_families_max_per_match_bucket{le="2"} 0` {
		t.Fatalf("first bucket line: %q", lines[0])
	}
	if last := lines[len(lines)-1]; last != "wordarena_behavior_families_max_per_match_count 1" {
		t.Fatalf("last line: %q", last)
	}
	if len(lines) != len(familiesMaxBounds)+3 {
		t.Fatalf("prometheusLines returned %d lines, want %d", len(lines), len(familiesMaxBounds)+3)
	}
}
