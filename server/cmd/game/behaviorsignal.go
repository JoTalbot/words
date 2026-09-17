package main

// Behavioral anti-cheat signals (M3 batch 40A) - MEASUREMENT ONLY.
//
// Nothing in this file may reject, delay, score, rank or otherwise influence
// a request, a seat or a match outcome. These are per-seat rolling signals
// for a FUTURE multi-signal risk assessment: AGENTS.md fixes the rule that
// anti-cheat is multi-signal risk assessment, never an automatic ban from a
// single heuristic. Any enforcement that consumes these signals is a
// separate, owner-gated decision and must weigh them together, in context,
// against a declared policy (docs/M3-ANTI-CHEAT.md owns that boundary).
//
// Signal: rejection streak. A seat whose consecutive rejected intents
// (not-in-dictionary, blocked-by-rule, invalid input) reach a high threshold.
// Fired exactly once per streak episode; an accepted word re-arms it.
// MATCH_NOT_ACTIVE refusals are the designed post-over close (batch 38C) and
// do not count - hammering after the match ended is rate-limiter business.
//
// Signal: metronomic cadence. Inter-submit gaps whose coefficient of
// variation stays machine-tiny while the mean sits in human-plausible
// territory. Humans jitter; clock-like regularity over a window of gaps is
// machine behaviour. Bursts under the mean floor are excluded on purpose:
// faster-than-200ms cadences are the per-seat intent rate limiter's domain
// and already surface as intent_rate_limited.
//
// Concurrency model: each seat has exactly ONE writer - the websocket reader
// goroutine that calls observe. Per-seat scalars and the gap ring are
// therefore single-writer, read by any /metrics scrape through atomics (a
// scrape may see a half-updated ring; that is acceptable for a signal and
// never a correctness concern, because nothing consumes it authoritatively).
// The seat->state map is a sync.Map: one LoadOrStore per seat per match,
// deleted per finished match alongside the rate-limit windows.

import (
	"math"
	"sync"
	"sync/atomic"
	"time"

	"github.com/JoTalbot/words/server/internal/match"
)

const (
	// signalRejectionStreak and signalMetronomic name the signals in
	// behavior_signal telemetry events and in tests.
	signalRejectionStreak = "rejection_streak"
	signalMetronomic      = "metronomic_cadence"

	// rejectionStreakSignalAt is the consecutive-rejection count that fires
	// the streak signal once per episode. 20 in a row has no human-plausible
	// reading: even a weak player re-arming after an accidental accepted word
	// resets the counter, so the episode has to be pure failure.
	rejectionStreakSignalAt = 20

	// cadenceRingSize bounds the gap window; cadenceMinGaps is the minimum
	// number of gaps before the detector runs at all.
	cadenceRingSize = 16
	cadenceMinGaps  = 8

	// cadenceCVThreshold: coefficient of variation (stddev/mean) below which
	// the cadence reads as machine-regular. Human tapping varies by tens of
	// percent over eight or more gaps; staying under 5% is not a person
	// keeping time.
	cadenceCVThreshold = 0.05

	// cadenceMinMeanMs excludes bursts (the rate limiter's domain, and a
	// metronome there is that guard's finding, not a behavioral one);
	// cadenceMaxMeanMs excludes idle stretches, which are not a cadence.
	cadenceMinMeanMs = 200
	cadenceMaxMeanMs = 10000
)

type seatBehavior struct {
	lastSubmit atomic.Int64                  // unix nanos of the last observed intent (0 = none)
	streak     atomic.Int64                  // consecutive rejected intents (accepted resets)
	flagged    atomic.Bool                   // metronomic signal already fired for this seat
	gaps       [cadenceRingSize]atomic.Int64 // most recent inter-submit gaps, nanos
	gapCount   atomic.Int64                  // total gaps appended (ring index = (n-1) mod size)
}

// behaviorTracker holds the per-seat behavioral signal state plus the
// process-lifetime event counters exported at /metrics. The counters are
// cumulative for the life of the process and are NOT reset by match end -
// they measure how often signals fire, which is the operational fact an
// operator needs.
type behaviorTracker struct {
	seats sync.Map // seatWindowKey -> *seatBehavior

	metronomicEvents   atomic.Uint64
	streakEvents       atomic.Uint64
	rateLimited        atomic.Uint64
	maxRejectionStreak atomic.Int64
}

// observe records one submitted intent for a seat and returns the names of
// any signals that fired at this instant (edge-triggered; an empty result is
// the common case). It also bumps the process-lifetime event counters, so
// every caller gets the same bookkeeping. now is a parameter so tests can
// drive deterministic cadences.
func (t *behaviorTracker) observe(key seatWindowKey, now time.Time, res match.WordResult) []string {
	v, _ := t.seats.LoadOrStore(key, &seatBehavior{})
	b := v.(*seatBehavior)
	var signals []string

	// --- cadence (any result; a submit is a submit) ------------------------
	prev := b.lastSubmit.Swap(now.UnixNano())
	if prev > 0 {
		if gap := now.UnixNano() - prev; gap > 0 {
			n := b.gapCount.Add(1)
			b.gaps[int(n-1)%cadenceRingSize].Store(gap)
		}
		if b.gapCount.Load() >= cadenceMinGaps && !b.flagged.Load() {
			if mean, cv := cadenceStats(b); cv >= 0 &&
				mean >= float64(cadenceMinMeanMs)*float64(time.Millisecond) &&
				mean <= float64(cadenceMaxMeanMs)*float64(time.Millisecond) &&
				cv < cadenceCVThreshold {
				if b.flagged.CompareAndSwap(false, true) {
					t.metronomicEvents.Add(1)
					signals = append(signals, signalMetronomic)
				}
			}
		}
	}

	// --- rejection streak (designed refusals excluded) ---------------------
	switch res {
	case match.ResultAccepted:
		b.streak.Store(0)
	case match.ResultRejectedNotInDict, match.ResultBlockedByRule, match.ResultInvalidInput:
		n := b.streak.Add(1)
		for {
			cur := t.maxRejectionStreak.Load()
			if n <= cur || t.maxRejectionStreak.CompareAndSwap(cur, n) {
				break
			}
		}
		if n == rejectionStreakSignalAt {
			t.streakEvents.Add(1)
			signals = append(signals, signalRejectionStreak)
		}
	}
	return signals
}

// cadenceStats reads the gap ring (atomically; a concurrent writer may make
// the window slightly mixed, which a signal tolerates). cv < 0 means "not
// computable" - too few gaps or a degenerate mean.
func cadenceStats(b *seatBehavior) (mean float64, cv float64) {
	n := b.gapCount.Load()
	k := cadenceRingSize
	if n < int64(k) {
		k = int(n)
	}
	if k < cadenceMinGaps {
		return 0, -1
	}
	var sum float64
	for i := 0; i < k; i++ {
		sum += float64(b.gaps[i].Load())
	}
	mean = sum / float64(k)
	if mean <= 0 {
		return mean, -1
	}
	var vsum float64
	for i := 0; i < k; i++ {
		d := float64(b.gaps[i].Load()) - mean
		vsum += d * d
	}
	cv = math.Sqrt(vsum/float64(k)) / mean
	return mean, cv
}

// clear drops the per-seat state for a finished match, exactly like
// clearSeatWindows does for the rate-limit windows. Counters survive: they
// are process-lifetime facts.
func (t *behaviorTracker) clear(matchID uint64, seats int) {
	for seat := 0; seat < seats; seat++ {
		t.seats.Delete(seatWindowKey{matchID: matchID, seat: seat})
	}
}
