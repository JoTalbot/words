package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// The intent histogram is the server-side half of the batch 35C off-box
// latency story (docs/M2-LOAD-TESTING.md): if the bucketing or the cumulative
// snapshot is wrong, a latency decomposition built on it would be wrong too,
// so the invariants are pinned here rather than trusted.

func TestIntentHistBucketsAndCumulativeSnapshot(t *testing.T) {
	var h intentHist
	for _, us := range []int64{10, 60, 200, 900, 4000, 6000, 20000} {
		h.record(us)
	}
	s := h.snapshot()
	if s.Count != 7 {
		t.Fatalf("count = %d, want 7", s.Count)
	}
	want := map[string]uint64{
		"le_50":    1, // 10
		"le_100":   2, // +60
		"le_250":   3, // +200
		"le_500":   3,
		"le_1000":  4, // +900
		"le_2500":  4,
		"le_5000":  5, // +4000
		"le_10000": 6, // +6000
		"le_inf":   7, // +20000 overflow
	}
	for k, v := range want {
		if s.Buckets[k] != v {
			t.Errorf("bucket %s = %d, want %d", k, s.Buckets[k], v)
		}
	}
	if s.Overflow != 1 {
		t.Errorf("overflow = %d, want 1 (the 20000µs observation)", s.Overflow)
	}
	if s.SumUs != uint64(10+60+200+900+4000+6000+20000) {
		t.Errorf("sum = %d", s.SumUs)
	}
}

func TestIntentHistNegativeDurationClampsToZero(t *testing.T) {
	var h intentHist
	h.record(-5)
	s := h.snapshot()
	if s.Count != 1 || s.SumUs != 0 || s.Buckets["le_50"] != 1 {
		t.Fatalf("negative observation broke the histogram: %+v", s)
	}
}

func TestIntentHistJSONShape(t *testing.T) {
	var h intentHist
	h.record(30)
	h.record(70)
	b, err := json.Marshal(h.snapshot())
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if m["count"].(float64) != 2 {
		t.Errorf("json count = %v", m["count"])
	}
	buckets, ok := m["buckets"].(map[string]any)
	if !ok {
		t.Fatalf("json buckets missing: %s", b)
	}
	if buckets["le_100"].(float64) != 2 {
		t.Errorf("json le_100 = %v, want 2", buckets["le_100"])
	}
}

func TestIntentHistPrometheusLines(t *testing.T) {
	var h intentHist
	h.record(30)
	h.record(20000)
	lines := strings.Join(h.prometheusLines("wordarena_intent_process_us"), "\n")
	for _, want := range []string{
		`wordarena_intent_process_us_bucket{le="50"} 1`,
		`wordarena_intent_process_us_bucket{le="+Inf"} 2`,
		"wordarena_intent_process_us_count 2",
	} {
		if !strings.Contains(lines, want) {
			t.Errorf("prometheus lines missing %q:\n%s", want, lines)
		}
	}
}
