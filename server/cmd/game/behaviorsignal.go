package main

// Behavioral anti-cheat signals (M3 batches 40A + 41A) - MEASUREMENT ONLY.
//
// Nothing in this file may reject, delay, score, rank or otherwise influence
// a request, a seat or a match outcome. These are per-seat rolling signals
// for a FUTURE multi-signal risk assessment: AGENTS.md fixes the rule that
// anti-cheat is multi-signal risk assessment, never an automatic ban from a
// single heuristic. Any enforcement that consumes these signals is a
// separate, owner-gated decision and must weigh them together, in context,
// against a declared policy (docs/M3-ANTI-CHEAT.md owns that boundary).
//
// Signals, in the order they landed:
//
// 40A rejection streak - a seat whose consecutive rejected intents
// (not-in-dictionary, blocked-by-rule, invalid input) reach a high threshold.
// Fired exactly once per streak episode; an accepted word re-arms it.
// MATCH_NOT_ACTIVE refusals are the designed post-over close (batch 38C) and
// do not count - hammering after the match ended is rate-limiter business.
//
// 40A metronomic cadence - inter-submit gaps whose coefficient of variation
// stays machine-tiny while the mean sits in human-plausible territory.
// Bursts under the mean floor are excluded on purpose: faster-than-200ms
// cadences are the per-seat intent rate limiter's domain and already surface
// as intent_rate_limited.
//
// 41A word probe - the SAME word string rejected repeatedly within one
// match. A typo varies; probing the dictionary oracle repeats. Fired once
// per (seat, word) at the threshold-th identical rejection, and only on the
// three rejection classes - an accepted word cannot be a probe by
// definition, and MATCH_NOT_ACTIVE is a designed refusal, not an answer.
//
// 41A flash path - a multi-cell path submitted back-to-back faster than a
// person can read the board and gesture it. Fired once per seat per match.
// Single-cell and short paths are excluded (a held-down tap is not a
// pattern), and so are slower multi-cell submits - the signal is the
// COMBINATION of path length and machine cadence.
//
// 42A multi signal - a seat that has fired TWO OR MORE DISTINCT signal
// families in one match. No new heuristic of its own: it records the join,
// which is the measurement half of the enforcement boundary's "weigh
// multiple signals together" rule (docs/M3-ANTI-CHEAT.md) - a policy day
// can only weigh signals it has observed co-occurring. Rate-limited closes
// are a transport guard and are deliberately not a family.
//
// Concurrency model: each seat has exactly ONE writer - the websocket reader
// goroutine that calls observe. Per-seat scalars, the probe map and the gap
// ring are therefore single-writer, read by any /metrics scrape through
// atomics (a scrape may see a half-updated ring; that is acceptable for a
// signal and never a correctness concern, because nothing consumes it
// authoritatively). The seat->state map is a sync.Map: one LoadOrStore per
// seat per match, deleted per finished match alongside the rate-limit
// windows.

import (
	"math"
	"math/bits"
	"sync"
	"sync/atomic"
	"time"

	"github.com/JoTalbot/words/server/internal/match"
)

const (
	// Signal names, used in behavior_signal telemetry events and in tests.
	signalRejectionStreak = "rejection_streak"
	signalMetronomic      = "metronomic_cadence"
	signalWordProbe       = "word_probe"
	signalFlashPath       = "flash_path"
	signalMulti           = "multi_signal"

	// multiSignalArity is the number of distinct signal families a seat must
	// fire before multi_signal records the join. Arity 2 is the minimum that
	// can mean "more than one thing is wrong with this seat", and families
	// are edge-triggered, so the join is a statement about cohort evidence,
	// not a new heuristic.
	multiSignalArity = 2

	// Signal family bits for the multi_signal join. Families are distinct
	// behavioral categories, not individual thresholds: a metronomic burst
	// that also flashes is metronomic + flash, not a new family. Explicit
	// powers of two so the values never shift if the const block grows.
	famRejectionStreak = 1
	famMetronomic      = 2
	famWordProbe       = 4
	famFlashPath       = 8

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

	// wordProbeRepeatAt: the same word string rejected this many times in one
	// match fires the probe signal once. Five identical failures is already
	// oracle probing; a struggling player varies their attempts.
	wordProbeRepeatAt = 5

	// flashPathCells / flashPathGapMs: a path of at least this many cells
	// submitted within this many milliseconds of the seat's previous intent
	// fires the flash signal once per seat. Reading a board and selecting
	// four cells takes a person longer than a fraction of a second; scripts
	// submit 4-cell paths back-to-back all day.
	flashPathCells = 4
	flashPathGapMs = 400
)

type seatBehavior struct {
	lastSubmit atomic.Int64                  // unix nanos of the last observed intent (0 = none)
	streak     atomic.Int64                  // consecutive rejected intents (accepted resets)
	flagged    atomic.Bool                   // metronomic signal already fired for this seat
	flashFlag  atomic.Bool                   // flash-path signal already fired for this seat
	gaps       [cadenceRingSize]atomic.Int64 // most recent inter-submit gaps, nanos
	gapCount   atomic.Int64                  // total gaps appended (ring index = (n-1) mod size)

	// probeCounts is written only by the seat's reader goroutine, so a plain
	// map is safe here; it dies with the seat state on clear().
	probeCounts map[string]int

	// multiFam is a bitmask of the signal families this seat has fired in
	// the current match, written only by the seat's reader goroutine (an
	// int is not atomic, but the single-writer guarantee plus a same-key
	// LoadOrStore handoff makes that safe - the same model probeCounts uses).
	// multiFlag records that the multi_signal join has already fired once,
	// so the counter and the event are per seat per match.
	multiFam  int
	multiFlag bool
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
	wordProbeEvents    atomic.Uint64
	flashPathEvents    atomic.Uint64
	multiSignalEvents  atomic.Uint64
	rateLimited        atomic.Uint64
	maxRejectionStreak atomic.Int64
}

// observe records one submitted intent for a seat and returns the names of
// any signals that fired at this instant (edge-triggered; an empty result is
// the common case). It also bumps the process-lifetime event counters, so
// every caller gets the same bookkeeping. now is a parameter so tests can
// drive deterministic cadences; word is the submitted word (may be empty);
// cells is the submitted path length.
func (t *behaviorTracker) observe(key seatWindowKey, now time.Time, res match.WordResult, word string, cells int) []string {
	v, _ := t.seats.LoadOrStore(key, &seatBehavior{})
	b := v.(*seatBehavior)
	if b.probeCounts == nil {
		b.probeCounts = make(map[string]int)
	}
	var signals []string

	// --- cadence + flash path (any result; a submit is a submit) -----------
	prev := b.lastSubmit.Swap(now.UnixNano())
	if prev > 0 {
		gap := now.UnixNano() - prev
		if gap > 0 {
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
		// Flash path: fast back-to-back MULTI-CELL submits. Once per seat.
		if cells >= flashPathCells && gap > 0 &&
			gap < int64(flashPathGapMs)*int64(time.Millisecond) &&
			b.flashFlag.CompareAndSwap(false, true) {
			t.flashPathEvents.Add(1)
			signals = append(signals, signalFlashPath)
		}
	}

	// --- rejection streak + word probe (designed refusals excluded) --------
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
		// Word probe: the same string failing over and over in one match.
		if word != "" {
			b.probeCounts[word]++
			if b.probeCounts[word] == wordProbeRepeatAt {
				t.wordProbeEvents.Add(1)
				signals = append(signals, signalWordProbe)
			}
		}
	}

	// --- multi signal (42A): the join of distinct families, once per seat ---
	// No heuristic of its own: families are recorded only as the individual
	// edge-triggered signals fire (so the family bit is set exactly once per
	// category), and arity >= 2 is the first instant the seat shows more than
	// one kind of machine behaviour in the same match. This is the observable
	// half of "weigh multiple signals together" - the policy that would weigh
	// them cannot exist yet, but the evidence it needs can be recorded now.
	var fam int
	for _, s := range signals {
		fam |= famBit(s)
	}
	if fam != 0 && !b.multiFlag {
		if added := fam &^ b.multiFam; added != 0 {
			b.multiFam |= added
			if bits.OnesCount(uint(b.multiFam)) >= multiSignalArity {
				t.multiSignalEvents.Add(1)
				b.multiFlag = true
				signals = append(signals, signalMulti)
			}
		}
	}
	return signals
}

// famBit maps a fired family signal name to its multi_signal family bit.
// signalMulti itself is the join and has no family bit of its own.
func famBit(signal string) int {
	switch signal {
	case signalRejectionStreak:
		return famRejectionStreak
	case signalMetronomic:
		return famMetronomic
	case signalWordProbe:
		return famWordProbe
	case signalFlashPath:
		return famFlashPath
	default:
		return 0
	}
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
