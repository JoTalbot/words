package main

import (
	"fmt"
	"sync/atomic"
)

// probeMaxBounds are the upper bounds (exclusive, rejection count) of the
// fixed buckets for the per-match maximum word-probe episode depth histogram
// (M3 batch 46A). The probe signal fires at wordProbeRepeatAt (5) identical
// rejections of one word, so:
//
//   - le_2..le_4: matches whose deepest probe episode stayed BELOW the signal
//     threshold (a seat rejected some word 2-4 times, then moved on - normal
//     retyping, not oracle probing);
//   - le_5: matches whose deepest episode exactly reached the threshold;
//   - le_6 and up: matches whose deepest episode EXCEEDED it - the signal
//     threshold fires at 5 and the episode keeps deepening while the seat
//     keeps hammering the same word.
//
// The bounds are FIXED (not caller-configurable) so two /metrics scrapes
// taken days apart stay directly comparable - the same rule every histogram
// in this package follows.
var probeMaxBounds = [...]int64{2, 3, 4, 5, 6, 8, 10}

// probeMaxHist is a fixed-bucket histogram of per-match maximum word-probe
// episode depths, built from atomics: it is written only from the room-close
// path (clear), but a /metrics scrape may read it concurrently with a close,
// and the buckets stay lock-free and allocation-free like every other
// histogram here.
type probeMaxHist struct {
	buckets [len(probeMaxBounds) + 1]atomic.Uint64
	count   atomic.Uint64
	sumDep  atomic.Uint64
}

// record adds one observation: the maximum word-probe episode depth (max over
// seats) a finished match exhibited. Callers record only non-zero maxima, so
// a clean match stays quiet exactly like the 42B aggregates.
func (h *probeMaxHist) record(maxDepth int) {
	d := maxDepth
	if d < 0 {
		d = 0
	}
	for i, ub := range probeMaxBounds {
		if int64(d) < ub {
			h.buckets[i].Add(1)
			h.count.Add(1)
			h.sumDep.Add(uint64(d))
			return
		}
	}
	h.buckets[len(h.buckets)-1].Add(1)
	h.count.Add(1)
	h.sumDep.Add(uint64(d))
}

// snapshot copies the histogram without locking (atomics only). Bucket keys
// are "le_<bound>" and CUMULATIVE (the Prometheus convention), matching the
// other histogram snapshots.
func (h *probeMaxHist) snapshot() probeMaxHistSnapshot {
	s := probeMaxHistSnapshot{Buckets: make(map[string]uint64, len(probeMaxBounds)+1)}
	var cum uint64
	for i, ub := range probeMaxBounds {
		cum += h.buckets[i].Load()
		s.Buckets[fmt.Sprintf("le_%d", ub)] = cum
	}
	s.Overflow = h.buckets[len(h.buckets)-1].Load()
	cum += s.Overflow
	s.Buckets["le_inf"] = cum
	s.Count = h.count.Load()
	s.SumDepth = h.sumDep.Load()
	return s
}

// prometheusLines renders the histogram in the Prometheus text format. The
// metric NAME is owned by the exporter in telemetry.go, like every other
// metric name.
func (h *probeMaxHist) prometheusLines(name string) []string {
	s := h.snapshot()
	lines := make([]string, 0, len(probeMaxBounds)+3)
	for _, ub := range probeMaxBounds {
		lines = append(lines, fmt.Sprintf("%s_bucket{le=\"%d\"} %d", name, ub, s.Buckets[fmt.Sprintf("le_%d", ub)]))
	}
	lines = append(lines, fmt.Sprintf("%s_bucket{le=\"+Inf\"} %d", name, s.Buckets["le_inf"]))
	lines = append(lines, fmt.Sprintf("%s_sum %d", name, s.SumDepth))
	lines = append(lines, fmt.Sprintf("%s_count %d", name, s.Count))
	return lines
}

// probeMaxHistSnapshot is the JSON rendering of the per-match probe-max
// histogram, keyed like the other histogram snapshots.
type probeMaxHistSnapshot struct {
	Count    uint64            `json:"count"`
	SumDepth uint64            `json:"sum_depth"`
	Overflow uint64            `json:"overflow_over_10"`
	Buckets  map[string]uint64 `json:"buckets"`
}
