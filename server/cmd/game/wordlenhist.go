package main

import (
	"fmt"
	"sync/atomic"

	"github.com/JoTalbot/words/server/internal/match"
)

// wordLenBounds are the upper bounds (exclusive, cells) of the fixed buckets
// for the intent word-length histogram. A submitted path of L cells lands in
// "le_N" for the smallest N > L; a path of 10 or more cells lands in the
// overflow bucket. The bounds are FIXED (not caller-configurable) so two
// /metrics scrapes taken days apart stay directly comparable - the same rule
// the intent processing histogram already follows (M2 batch 35C). Boards run
// from 12 cells (1v1) to 60 cells (Royale), and the shortest admitted words
// are two letters, so the interesting band for the future cell-path signal is
// 2..10 cells: how often a seat submits implausibly long words, split by
// outcome.
var wordLenBounds = [...]int64{3, 4, 5, 6, 7, 8, 9, 10}

// wordHist is a fixed-bucket histogram of path lengths (cells) for one
// outcome class, built from atomics: the intent path is hot (every word every
// seat submits, 30 Hz rooms), so recording must be lock-free and
// allocation-free.
type wordHist struct {
	buckets  [len(wordLenBounds) + 1]atomic.Uint64
	count    atomic.Uint64
	sumCells atomic.Uint64
}

// record adds one observation: a submitted path of cells length that ended in
// this histogram's outcome class.
func (h *wordHist) record(cells int) {
	if cells < 0 {
		cells = 0
	}
	for i, ub := range wordLenBounds {
		if int64(cells) < ub {
			h.buckets[i].Add(1)
			h.count.Add(1)
			h.sumCells.Add(uint64(cells))
			return
		}
	}
	h.buckets[len(h.buckets)-1].Add(1)
	h.count.Add(1)
	h.sumCells.Add(uint64(cells))
}

// snapshot copies the histogram without locking (atomics only). Bucket keys
// are "le_<bound>" and CUMULATIVE (the Prometheus convention), matching the
// intent processing histogram's rendering.
func (h *wordHist) snapshot() wordHistSnapshot {
	s := wordHistSnapshot{Buckets: make(map[string]uint64, len(wordLenBounds)+1)}
	var cum uint64
	for i, ub := range wordLenBounds {
		cum += h.buckets[i].Load()
		s.Buckets[fmt.Sprintf("le_%d", ub)] = cum
	}
	s.Overflow = h.buckets[len(h.buckets)-1].Load()
	cum += s.Overflow
	s.Buckets["le_inf"] = cum
	s.Count = h.count.Load()
	s.SumCells = h.sumCells.Load()
	return s
}

// prometheus renders the histogram in the Prometheus text format into the
// lines a caller appends to the export. Kept as a method so the bucket list
// lives in exactly one file; the metric NAME is owned by the exporter in
// telemetry.go, like every other metric name.
func (h *wordHist) prometheusLines(name string) []string {
	s := h.snapshot()
	lines := make([]string, 0, len(wordLenBounds)+4)
	for _, ub := range wordLenBounds {
		lines = append(lines, fmt.Sprintf("%s_bucket{le=\"%d\"} %d", name, ub, s.Buckets[fmt.Sprintf("le_%d", ub)]))
	}
	lines = append(lines, fmt.Sprintf("%s_bucket{le=\"+Inf\"} %d", name, s.Buckets["le_inf"]))
	lines = append(lines, fmt.Sprintf("%s_sum %d", name, s.SumCells))
	lines = append(lines, fmt.Sprintf("%s_count %d", name, s.Count))
	return lines
}

// wordLenHist holds one wordHist per authoritative outcome class. Outcomes
// that do not decompile a word (parse failures before SubmitWithSeq) never
// reach this, and MATCH_NOT_ACTIVE is deliberately NOT a bucket: it is the
// designed post-over refusal and its length distribution is not a word
// signal, so recording it would only muddy the short-word buckets.
type wordLenHist struct {
	accepted          wordHist
	rejectedNotInDict wordHist
	blockedByRule     wordHist
	invalidInput      wordHist
}

// record routes one authoritative outcome (frame.Result) plus the submitted
// path length (cells) to its class histogram. Nothing here influences the
// seat, the room or the outcome - measurement only, like every M3 signal.
func (w *wordLenHist) record(res match.WordResult, cells int) {
	switch res {
	case match.ResultAccepted:
		w.accepted.record(cells)
	case match.ResultRejectedNotInDict:
		w.rejectedNotInDict.record(cells)
	case match.ResultBlockedByRule:
		w.blockedByRule.record(cells)
	case match.ResultInvalidInput:
		w.invalidInput.record(cells)
	}
}

// wordHistSnapshot is the JSON rendering of one wordHist.
type wordHistSnapshot struct {
	Count    uint64            `json:"count"`
	SumCells uint64            `json:"sum_cells"`
	Overflow uint64            `json:"overflow_over_10_cells"`
	Buckets  map[string]uint64 `json:"buckets"`
}

// wordLenSnapshot is the JSON rendering of the whole wordLenHist, keyed by
// outcome class.
type wordLenSnapshot struct {
	Accepted          wordHistSnapshot `json:"accepted"`
	RejectedNotInDict wordHistSnapshot `json:"rejected_not_in_dict"`
	BlockedByRule     wordHistSnapshot `json:"blocked_by_rule"`
	InvalidInput      wordHistSnapshot `json:"invalid_input"`
}

func (w *wordLenHist) snapshot() wordLenSnapshot {
	return wordLenSnapshot{
		Accepted:          w.accepted.snapshot(),
		RejectedNotInDict: w.rejectedNotInDict.snapshot(),
		BlockedByRule:     w.blockedByRule.snapshot(),
		InvalidInput:      w.invalidInput.snapshot(),
	}
}
