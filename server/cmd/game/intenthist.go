package main

import (
	"fmt"
	"sync/atomic"
)

// intentProcessBounds are the upper bounds (exclusive, microseconds) of the
// fixed buckets for the server-side intent processing histogram. The bounds
// bracket the interesting range for one authoritative SubmitWithSeq call:
// sub-100 µs is the in-memory fast path, hundreds of µs appears under
// contention or with a Postgres-backed write on the critical path, and
// anything beyond a few milliseconds is worth an incident note. The buckets
// are FIXED (not caller-configurable) so two /metrics scrapes taken days apart
// are directly comparable - this is an observability surface, not a tuning
// knob (M2 batch 35C, docs/M2-LOAD-TESTING.md).
var intentProcessBounds = [...]int64{50, 100, 250, 500, 1000, 2500, 5000, 10000}

// intentHist is a fixed-bucket histogram of microsecond durations, built from
// atomics like the rest of the metrics struct: the intent path is hot (every
// word every seat submits, 30 Hz rooms), so recording must be lock-free and
// allocation-free.
type intentHist struct {
	buckets [len(intentProcessBounds) + 1]atomic.Uint64
	count   atomic.Uint64
	sumUs   atomic.Uint64
}

// record adds one observation in microseconds.
func (h *intentHist) record(us int64) {
	obs := us
	if obs < 0 {
		obs = 0
	}
	for i, ub := range intentProcessBounds {
		if us < ub {
			h.buckets[i].Add(1)
			h.count.Add(1)
			h.sumUs.Add(uint64(obs))
			return
		}
	}
	h.buckets[len(h.buckets)-1].Add(1)
	h.count.Add(1)
	h.sumUs.Add(uint64(obs))
}

// intentHistSnapshot is a stable rendering of one histogram. Bucket keys are
// "le_<bound>" and CUMULATIVE (the Prometheus convention), so a consumer can
// diff two snapshots without knowing the bound list.
type intentHistSnapshot struct {
	Count    uint64            `json:"count"`
	SumUs    uint64            `json:"sum_us"`
	Overflow uint64            `json:"overflow_over_10ms"`
	Buckets  map[string]uint64 `json:"buckets"`
}

// snapshot copies the histogram without locking (atomics only).
func (h *intentHist) snapshot() intentHistSnapshot {
	s := intentHistSnapshot{Buckets: make(map[string]uint64, len(intentProcessBounds)+1)}
	var cum uint64
	for i, ub := range intentProcessBounds {
		cum += h.buckets[i].Load()
		s.Buckets[fmt.Sprintf("le_%d", ub)] = cum
	}
	s.Overflow = h.buckets[len(h.buckets)-1].Load()
	cum += s.Overflow
	s.Buckets["le_inf"] = cum
	s.Count = h.count.Load()
	s.SumUs = h.sumUs.Load()
	return s
}

// prometheus renders the histogram in the Prometheus text format into the
// lines a caller appends to the export. Kept as a method here so the bucket
// list lives in exactly one file; the metric NAME is owned by the exporter in
// telemetry.go, like every other metric name.
func (h *intentHist) prometheusLines(name string) []string {
	s := h.snapshot()
	lines := make([]string, 0, len(intentProcessBounds)+4)
	for _, ub := range intentProcessBounds {
		lines = append(lines, fmt.Sprintf("%s_bucket{le=\"%d\"} %d", name, ub, s.Buckets[fmt.Sprintf("le_%d", ub)]))
	}
	lines = append(lines, fmt.Sprintf("%s_bucket{le=\"+Inf\"} %d", name, s.Buckets["le_inf"]))
	lines = append(lines, fmt.Sprintf("%s_sum %d", name, s.SumUs))
	lines = append(lines, fmt.Sprintf("%s_count %d", name, s.Count))
	return lines
}
