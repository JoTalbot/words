package main

import (
	"fmt"
	"sync/atomic"
)

// streakMaxBounds are the upper bounds (exclusive, rejection count) of the
// fixed buckets for the per-match maximum consecutive-rejection streak
// histogram (M3 batch 45A). The bands bracket what an operator actually needs
// to read from this signal:
//
//   - le_2..le_16: the genuinely human band (a slap, a stuck word, a bad
//     board) — the per-match max of a struggling human rarely passes a
//     handful of consecutive rejections;
//   - le_20: every match whose max stayed UNDER the 40A signal threshold
//     (rejectionStreakSignalAt = 20) — i.e. matches where no seat fired the
//     streak signal;
//   - le_40 and overflow: matches whose max streak REACHED or exceeded the
//     signal threshold. A submitted streak of exactly 20 lands in le_40
//     (bounds are exclusive upper, cumulative), so le_40 + le_inf together
//     count the matches that exhibited signal-grade streak behaviour.
//
// The bounds are FIXED (not caller-configurable) so two /metrics scrapes
// taken days apart stay directly comparable — the same rule the intent
// processing (35C) and word-length (42C) histograms already follow.
var streakMaxBounds = [...]int64{2, 4, 8, 12, 16, 20, 40}

// streakMaxHist is a fixed-bucket histogram of per-match maximum
// consecutive-rejection streaks, built from atomics: it is written only from
// the room-close path (clear), which is far off the 30 Hz intent hot path,
// but a /metrics scrape may read it concurrently with a close, and the
// buckets must stay lock-free and allocation-free like every other
// histogram in this package.
type streakMaxHist struct {
	buckets    [len(streakMaxBounds) + 1]atomic.Uint64
	count      atomic.Uint64
	sumStreaks atomic.Uint64
}

// record adds one observation: the maximum consecutive-rejection streak (max
// over seats) a finished match exhibited. Only matches with a non-zero max
// are recorded, exactly like the 42B longitudinal aggregates stay quiet for
// a clean match: the count is the number of closed matches that had at least
// one rejected intent, and sum/count is the average per-match streak depth.
func (h *streakMaxHist) record(maxStreak int) {
	s := maxStreak
	if s < 0 {
		s = 0
	}
	for i, ub := range streakMaxBounds {
		if int64(s) < ub {
			h.buckets[i].Add(1)
			h.count.Add(1)
			h.sumStreaks.Add(uint64(s))
			return
		}
	}
	h.buckets[len(h.buckets)-1].Add(1)
	h.count.Add(1)
	h.sumStreaks.Add(uint64(s))
}

// snapshot copies the histogram without locking (atomics only). Bucket keys
// are "le_<bound>" and CUMULATIVE (the Prometheus convention), matching the
// word-length histogram's rendering.
func (h *streakMaxHist) snapshot() streakMaxHistSnapshot {
	s := streakMaxHistSnapshot{Buckets: make(map[string]uint64, len(streakMaxBounds)+1)}
	var cum uint64
	for i, ub := range streakMaxBounds {
		cum += h.buckets[i].Load()
		s.Buckets[fmt.Sprintf("le_%d", ub)] = cum
	}
	s.Overflow = h.buckets[len(h.buckets)-1].Load()
	cum += s.Overflow
	s.Buckets["le_inf"] = cum
	s.Count = h.count.Load()
	s.SumStreaks = h.sumStreaks.Load()
	return s
}

// prometheusLines renders the histogram in the Prometheus text format into
// the lines a caller appends to the export. Kept as a method so the bucket
// list lives in exactly one file; the metric NAME is owned by the exporter
// in telemetry.go, like every other metric name.
func (h *streakMaxHist) prometheusLines(name string) []string {
	s := h.snapshot()
	lines := make([]string, 0, len(streakMaxBounds)+3)
	for _, ub := range streakMaxBounds {
		lines = append(lines, fmt.Sprintf("%s_bucket{le=\"%d\"} %d", name, ub, s.Buckets[fmt.Sprintf("le_%d", ub)]))
	}
	lines = append(lines, fmt.Sprintf("%s_bucket{le=\"+Inf\"} %d", name, s.Buckets["le_inf"]))
	lines = append(lines, fmt.Sprintf("%s_sum %d", name, s.SumStreaks))
	lines = append(lines, fmt.Sprintf("%s_count %d", name, s.Count))
	return lines
}

// streakMaxHistSnapshot is the JSON rendering of the per-match streak-max
// histogram, keyed like the other histogram snapshots.
type streakMaxHistSnapshot struct {
	Count      uint64            `json:"count"`
	SumStreaks uint64            `json:"sum_streaks"`
	Overflow   uint64            `json:"overflow_over_40"`
	Buckets    map[string]uint64 `json:"buckets"`
}
