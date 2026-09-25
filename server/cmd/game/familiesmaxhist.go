package main

import (
	"fmt"
	"sync/atomic"
)

// familiesMaxBounds are the upper bounds (exclusive, family count) of the
// fixed buckets for the per-match maximum distinct-signal-family count
// histogram (M3 batch 46A). multiSignalArity is 2, so:
//
//   - le_2 counts matches whose deepest seat fired exactly ONE distinct
//     family (flagged, but never joined - the join needs two);
//   - le_3 counts matches whose deepest seat reached the arity-2 join;
//   - le_4 and overflow count matches whose deepest seat fired three or more
//     families - the heaviest co-occurrence evidence a policy day could weigh.
//
// The bounds are FIXED so two /metrics scrapes stay comparable, like every
// histogram in this package.
var familiesMaxBounds = [...]int64{2, 3, 4}

// familiesMaxHist is a fixed-bucket histogram of per-match maximum distinct
// signal-family counts, built from atomics for the same cross-goroutine
// scrape/close concurrency as the other close-time histograms.
type familiesMaxHist struct {
	buckets [len(familiesMaxBounds) + 1]atomic.Uint64
	count   atomic.Uint64
	sumFam  atomic.Uint64
}

// record adds one observation: the maximum number of distinct signal families
// (max over seats) a finished match exhibited. Callers record only when at
// least one family fired, so count is the matches-with-a-flagged-seat
// denominator of the families distribution.
func (h *familiesMaxHist) record(maxFamilies int) {
	f := maxFamilies
	if f < 0 {
		f = 0
	}
	for i, ub := range familiesMaxBounds {
		if int64(f) < ub {
			h.buckets[i].Add(1)
			h.count.Add(1)
			h.sumFam.Add(uint64(f))
			return
		}
	}
	h.buckets[len(h.buckets)-1].Add(1)
	h.count.Add(1)
	h.sumFam.Add(uint64(f))
}

// snapshot copies the histogram without locking (atomics only).
func (h *familiesMaxHist) snapshot() familiesMaxHistSnapshot {
	s := familiesMaxHistSnapshot{Buckets: make(map[string]uint64, len(familiesMaxBounds)+1)}
	var cum uint64
	for i, ub := range familiesMaxBounds {
		cum += h.buckets[i].Load()
		s.Buckets[fmt.Sprintf("le_%d", ub)] = cum
	}
	s.Overflow = h.buckets[len(h.buckets)-1].Load()
	cum += s.Overflow
	s.Buckets["le_inf"] = cum
	s.Count = h.count.Load()
	s.SumFamilies = h.sumFam.Load()
	return s
}

// prometheusLines renders the histogram in the Prometheus text format. The
// metric NAME is owned by the exporter in telemetry.go.
func (h *familiesMaxHist) prometheusLines(name string) []string {
	s := h.snapshot()
	lines := make([]string, 0, len(familiesMaxBounds)+3)
	for _, ub := range familiesMaxBounds {
		lines = append(lines, fmt.Sprintf("%s_bucket{le=\"%d\"} %d", name, ub, s.Buckets[fmt.Sprintf("le_%d", ub)]))
	}
	lines = append(lines, fmt.Sprintf("%s_bucket{le=\"+Inf\"} %d", name, s.Buckets["le_inf"]))
	lines = append(lines, fmt.Sprintf("%s_sum %d", name, s.SumFamilies))
	lines = append(lines, fmt.Sprintf("%s_count %d", name, s.Count))
	return lines
}

// familiesMaxHistSnapshot is the JSON rendering of the per-match families-max
// histogram.
type familiesMaxHistSnapshot struct {
	Count       uint64            `json:"count"`
	SumFamilies uint64            `json:"sum_families"`
	Overflow    uint64            `json:"overflow_over_4"`
	Buckets     map[string]uint64 `json:"buckets"`
}
