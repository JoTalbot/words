package main

import (
	"fmt"
	"testing"
	"time"

	"github.com/JoTalbot/words/server/internal/match"
)

// deterministic clock helpers: every test drives observe with explicit times
// so the cadence detector sees exact gaps.

func behaviorAt(base time.Time, i int, gap time.Duration) time.Time {
	return base.Add(time.Duration(i) * gap)
}

func countSignals(signals []string, want string) int {
	n := 0
	for _, s := range signals {
		if s == want {
			n++
		}
	}
	return n
}

func TestRejectionStreakFiresOncePerEpisode(t *testing.T) {
	var tr behaviorTracker
	key := seatWindowKey{matchID: 7, seat: 3}
	base := time.Unix(1700000000, 0)
	count := func(res match.WordResult, from, to int) int {
		n := 0
		for i := from; i <= to; i++ {
			// Jittered times + single-cell submits keep the cadence and flash
			// detectors quiet; this test is about the streak edge semantics.
			clock := behaviorAt(base, i, 300*time.Millisecond+time.Duration(i%5)*37*time.Millisecond)
			n += countSignals(tr.observe(key, clock, res, "word", 1), signalRejectionStreak)
		}
		return n
	}

	if got := count(match.ResultRejectedNotInDict, 1, rejectionStreakSignalAt-1); got != 0 {
		t.Fatalf("streak signal fired before threshold: %d times", got)
	}
	if got := count(match.ResultRejectedNotInDict, rejectionStreakSignalAt, 30); got != 1 {
		t.Fatalf("streak signal at/after threshold: want exactly 1, got %d", got)
	}
	if got := tr.maxRejectionStreak.Load(); got != 30 {
		t.Fatalf("max rejection streak gauge: want 30, got %d", got)
	}

	// An accepted word re-arms the detector: the next full episode fires again.
	tr.observe(key, behaviorAt(base, 31, 300*time.Millisecond), match.ResultAccepted, "word", 1)
	if got := count(match.ResultRejectedNotInDict, 32, 31+rejectionStreakSignalAt); got != 1 {
		t.Fatalf("streak signal after re-arm: want exactly 1, got %d", got)
	}
	// Counters are bumped by observe itself (the only entry point), so any
	// future caller gets the same bookkeeping automatically.
	if tr.streakEvents.Load() != 2 {
		t.Fatalf("streak events counter: want 2, got %d", tr.streakEvents.Load())
	}
}

func TestRejectionStreakIgnoresMatchNotActive(t *testing.T) {
	var tr behaviorTracker
	key := seatWindowKey{matchID: 9, seat: 1}
	base := time.Unix(1700000000, 0)
	// Jittered times so the cadence detector stays quiet; the point of this
	// test is that post-over refusals never advance the streak.
	gaps := []time.Duration{220, 480, 310, 750, 260, 540, 330, 690, 415, 285}
	clock := base
	for i := 0; i < 3*rejectionStreakSignalAt; i++ {
		clock = clock.Add(gaps[i%len(gaps)]*time.Millisecond + time.Duration(i%7)*time.Millisecond)
		for _, s := range tr.observe(key, clock, match.ResultMatchNotActive, "word", 1) {
			t.Fatalf("MATCH_NOT_ACTIVE must not fire signals, got %q", s)
		}
	}
	if got := tr.maxRejectionStreak.Load(); got != 0 {
		t.Fatalf("MATCH_NOT_ACTIVE advanced the streak: max=%d", got)
	}
}

func TestMetronomicCadenceFiresOncePerSeat(t *testing.T) {
	var tr behaviorTracker
	key := seatWindowKey{matchID: 2, seat: 0}
	base := time.Unix(1700000000, 0)
	fired := 0
	for i := 1; i <= 30; i++ {
		fired += countSignals(tr.observe(key, behaviorAt(base, i, 400*time.Millisecond), match.ResultAccepted, "word", 1), signalMetronomic)
	}
	if fired != 1 {
		t.Fatalf("metronomic signal: want exactly 1 fire, got %d", fired)
	}
	if tr.metronomicEvents.Load() != 1 {
		t.Fatalf("metronomic events counter: want 1, got %d", tr.metronomicEvents.Load())
	}
}

func TestHumanJitterNeverFlagsMetronomic(t *testing.T) {
	var tr behaviorTracker
	key := seatWindowKey{matchID: 3, seat: 2}
	base := time.Unix(1700000000, 0)
	gaps := []time.Duration{180, 320, 540, 260, 710, 410, 230, 640, 390, 505}
	clock := base
	for i := 0; i < 100; i++ {
		clock = clock.Add(gaps[i%len(gaps)] * time.Millisecond)
		for _, s := range tr.observe(key, clock, match.ResultAccepted, "word", 1) {
			t.Fatalf("jittered cadence fired %q", s)
		}
	}
}

func TestBurstCadenceBelongsToRateLimiterNotBehavior(t *testing.T) {
	var tr behaviorTracker
	key := seatWindowKey{matchID: 4, seat: 1}
	base := time.Unix(1700000000, 0)
	for i := 1; i <= 40; i++ {
		for _, s := range tr.observe(key, behaviorAt(base, i, 50*time.Millisecond), match.ResultAccepted, "word", 1) {
			t.Fatalf("burst cadence fired %q (rate limiter's domain)", s)
		}
	}
}

func TestIdleCadenceNotAMetronome(t *testing.T) {
	var tr behaviorTracker
	key := seatWindowKey{matchID: 5, seat: 0}
	base := time.Unix(1700000000, 0)
	for i := 1; i <= 20; i++ {
		for _, s := range tr.observe(key, behaviorAt(base, i, 30*time.Second), match.ResultAccepted, "word", 1) {
			t.Fatalf("idle cadence fired %q", s)
		}
	}
}

func TestClearResetsSeatStateButNotCounters(t *testing.T) {
	var tr behaviorTracker
	key := seatWindowKey{matchID: 11, seat: 4}
	base := time.Unix(1700000000, 0)
	for i := 1; i < rejectionStreakSignalAt; i++ {
		tr.observe(key, behaviorAt(base, i, 300*time.Millisecond), match.ResultBlockedByRule, "word", 1)
	}
	tr.clear(11, 6)
	// Fresh seat state: another threshold-1 REJECTIONS fire no streak signal.
	// Times are jittered on purpose - the fresh cadence detector would
	// legitimately fire metronomic_cadence on exact intervals again, and this
	// test is about the streak episode not surviving clear.
	gaps := []time.Duration{220, 480, 310, 750, 260, 540, 330, 690, 415, 285}
	clock := base
	for i := 0; i < rejectionStreakSignalAt-1; i++ {
		clock = clock.Add(gaps[i%len(gaps)] * time.Millisecond)
		for _, s := range tr.observe(key, clock, match.ResultBlockedByRule, "word", 1) {
			if s == signalRejectionStreak {
				t.Fatalf("streak state survived clear: fired %q", s)
			}
		}
	}
	if got := tr.maxRejectionStreak.Load(); got != rejectionStreakSignalAt-1 {
		t.Fatalf("max streak after clear: want %d (process-lifetime), got %d", rejectionStreakSignalAt-1, got)
	}
	if tr.streakEvents.Load() != 0 {
		t.Fatalf("streak events after only sub-threshold episodes: want 0, got %d", tr.streakEvents.Load())
	}
}

// ---- 41A: word probe ----

func TestWordProbeFiresAtFifthIdenticalRejection(t *testing.T) {
	var tr behaviorTracker
	key := seatWindowKey{matchID: 21, seat: 2}
	base := time.Unix(1700000000, 0)
	fired := 0
	// Jittered times so cadence stays quiet; single cells so flash stays quiet.
	gaps := []time.Duration{220, 480, 310, 750, 260, 540, 330, 690, 415, 285}
	clock := base
	for i := 1; i <= 7; i++ {
		clock = clock.Add(gaps[i%len(gaps)] * time.Millisecond)
		fired += countSignals(tr.observe(key, clock, match.ResultRejectedNotInDict, "QEAEZ", 3), signalWordProbe)
	}
	if fired != 1 {
		t.Fatalf("word probe: want exactly 1 fire at the threshold-th repeat, got %d", fired)
	}
	if tr.wordProbeEvents.Load() != 1 {
		t.Fatalf("word probe counter: want 1, got %d", tr.wordProbeEvents.Load())
	}
	// A different word is its own episode.
	for i := 8; i <= 12; i++ {
		clock = clock.Add(gaps[i%len(gaps)] * time.Millisecond)
		fired += countSignals(tr.observe(key, clock, match.ResultRejectedNotInDict, "ZZZZQ", 3), signalWordProbe)
	}
	if fired != 2 || tr.wordProbeEvents.Load() != 2 {
		t.Fatalf("second word episode: want exactly one more fire (2 total), got %d", fired)
	}
}

func TestWordProbeIgnoresAcceptedAndMatchNotActive(t *testing.T) {
	var tr behaviorTracker
	key := seatWindowKey{matchID: 22, seat: 0}
	base := time.Unix(1700000000, 0)
	gaps := []time.Duration{220, 480, 310, 750, 260, 540, 330, 690, 415, 285}
	clock := base
	// 4 rejections, then the same word accepted (deterministic dictionary
	// would never allow this flip-flop, but the counter must not be
	// perturbed by other outcomes either way), then more rejections.
	for i := 1; i <= 4; i++ {
		clock = clock.Add(gaps[i%len(gaps)] * time.Millisecond)
		tr.observe(key, clock, match.ResultRejectedNotInDict, "QEAEZ", 2)
	}
	clock = clock.Add(500 * time.Millisecond)
	tr.observe(key, clock, match.ResultAccepted, "QEAEZ", 2)
	clock = clock.Add(500 * time.Millisecond)
	if got := countSignals(tr.observe(key, clock, match.ResultMatchNotActive, "QEAEZ", 2), signalWordProbe); got != 0 {
		t.Fatalf("MATCH_NOT_ACTIVE fed the probe counter")
	}
	// The next REJECTION is the 5th of the word: the two neutral outcomes in
	// between must not have advanced or reset the counter, so the episode
	// fires exactly here.
	clock = clock.Add(500 * time.Millisecond)
	if got := countSignals(tr.observe(key, clock, match.ResultRejectedNotInDict, "QEAEZ", 2), signalWordProbe); got != 1 {
		t.Fatalf("probe episode: want the fire on the 5th rejection, got %d", got)
	}
	if tr.wordProbeEvents.Load() != 1 {
		t.Fatalf("word probe counter: want 1, got %d", tr.wordProbeEvents.Load())
	}
}

// ---- 41A: flash path ----

func TestFlashPathFiresOnceForFastMultiCellSubmits(t *testing.T) {
	var tr behaviorTracker
	key := seatWindowKey{matchID: 31, seat: 1}
	base := time.Unix(1700000000, 0)
	fired := 0
	for i := 1; i <= 12; i++ {
		// Exact 200 ms gaps with 5-cell paths: fast multi-cell spam.
		fired += countSignals(tr.observe(key, behaviorAt(base, i, 200*time.Millisecond), match.ResultRejectedNotInDict, "word", 5), signalFlashPath)
	}
	if fired != 1 {
		t.Fatalf("flash path: want exactly 1 fire per seat per match, got %d", fired)
	}
	if tr.flashPathEvents.Load() != 1 {
		t.Fatalf("flash path counter: want 1, got %d", tr.flashPathEvents.Load())
	}
}

// ---- 42A: multi signal ----

// multiFires drives one seat past a target signal with jittered single-cell
// submits (cells=1, irregular gaps) so ONLY that signal's family bit lands.
// rejections are ResultRejectedNotInDict; everything else is ResultAccepted.
func multiFires(t *testing.T, tr *behaviorTracker, key seatWindowKey, base time.Time, target string) {
	t.Helper()
	gaps := []time.Duration{220, 480, 310, 750, 260, 540, 330, 690, 415, 285}
	clock := base
	total := 0
	switch target {
	case signalRejectionStreak:
		// A distinct word each rejection so the probe detector (same string
		// five times) stays quiet: this case is about the streak family only.
		for i := 1; i <= rejectionStreakSignalAt; i++ {
			clock = clock.Add(gaps[i%len(gaps)]*time.Millisecond + time.Duration(i%7)*time.Millisecond)
			total += countSignals(tr.observe(key, clock, match.ResultRejectedNotInDict, fmt.Sprintf("qzx%d", i), 1), signalRejectionStreak)
		}
	case signalMetronomic:
		for i := 1; i <= 30; i++ {
			total += countSignals(tr.observe(key, behaviorAt(base, i, 400*time.Millisecond), match.ResultAccepted, "word", 1), signalMetronomic)
		}
	case signalWordProbe:
		for i := 1; i <= 5; i++ {
			clock = clock.Add(gaps[i%len(gaps)]*time.Millisecond + time.Duration(i%7)*time.Millisecond)
			total += countSignals(tr.observe(key, clock, match.ResultRejectedNotInDict, "QEAEZ", 1), signalWordProbe)
		}
	case signalFlashPath:
		for i := 1; i <= 2; i++ {
			total += countSignals(tr.observe(key, behaviorAt(base, i, 200*time.Millisecond), match.ResultAccepted, "word", 5), signalFlashPath)
		}
	default:
		t.Fatalf("multiFires: unknown target %q", target)
	}
	if total != 1 {
		t.Fatalf("multiFires(%s): must fire exactly once, got %d", target, total)
	}
}

func TestMultiSignalFiresOnSecondDistinctFamily(t *testing.T) {
	for i, tc := range []struct{ first, second string }{
		{signalRejectionStreak, signalMetronomic},
		{signalWordProbe, signalFlashPath},
		{signalFlashPath, signalRejectionStreak},
		{signalMetronomic, signalWordProbe},
	} {
		var tr behaviorTracker
		key := seatWindowKey{matchID: uint64(1000 + i), seat: i}
		base := time.Unix(1700000000, 0)
		multiFires(t, &tr, key, base, tc.first)
		multiFires(t, &tr, key, base.Add(2*time.Minute), tc.second)
		if got := countSignals(tr.observe(key, base.Add(3*time.Minute), match.ResultAccepted, "word", 1), signalMulti); got != 0 {
			t.Fatalf("pair %v: multi_signal must not fire on the observe AFTER it already fired", tc)
		}
		if tr.multiSignalEvents.Load() != 1 {
			t.Fatalf("pair %v: multiSignalEvents counter: want 1, got %d", tc, tr.multiSignalEvents.Load())
		}
	}
}

func TestMultiSignalNeedsDistinctFamiliesNotRepeats(t *testing.T) {
	// Two INDEPENDENT rejection streaks are one family - the join must stay
	// quiet. (The second streak needs an accepted word to re-arm, or it is
	// one long streak.)
	var tr behaviorTracker
	key := seatWindowKey{matchID: 2001, seat: 0}
	base := time.Unix(1700000000, 0)
	multiFires(t, &tr, key, base, signalRejectionStreak)
	tr.observe(key, base.Add(time.Minute), match.ResultAccepted, "word", 1) // re-arm
	multiFires(t, &tr, key, base.Add(2*time.Minute), signalRejectionStreak)
	if got := countSignals(tr.observe(key, base.Add(3*time.Minute), match.ResultAccepted, "word", 1), signalMulti); got != 0 {
		t.Fatalf("repeated single family must not fire multi_signal")
	}
	if tr.multiSignalEvents.Load() != 0 {
		t.Fatalf("multiSignalEvents counter: want 0, got %d", tr.multiSignalEvents.Load())
	}
}

func TestMultiSignalClearResetsFamiliesButNotCounters(t *testing.T) {
	var tr behaviorTracker
	key := seatWindowKey{matchID: 3001, seat: 2}
	base := time.Unix(1700000000, 0)
	multiFires(t, &tr, key, base, signalRejectionStreak)
	multiFires(t, &tr, key, base.Add(time.Minute), signalMetronomic)
	tr.clear(3001, 4)

	// A fresh seat (same key after clear) with only one family again: no join.
	multiFires(t, &tr, key, base.Add(2*time.Minute), signalFlashPath)
	if got := countSignals(tr.observe(key, base.Add(3*time.Minute), match.ResultAccepted, "word", 1), signalMulti); got != 0 {
		t.Fatalf("family state survived clear: multi_signal fired")
	}
	if tr.multiSignalEvents.Load() != 1 {
		t.Fatalf("multiSignalEvents counter is process-lifetime: want 1, got %d", tr.multiSignalEvents.Load())
	}
}

// ---- 42B: longitudinal aggregates at room close ----

func TestClosedAggregatesRecordOnClear(t *testing.T) {
	var tr behaviorTracker
	base := time.Unix(1700000000, 0)
	// seat 0: one family (streak).
	multiFires(t, &tr, seatWindowKey{matchID: 4001, seat: 0}, base, signalRejectionStreak)
	// seat 1: two families (streak + metronomic).
	multiFires(t, &tr, seatWindowKey{matchID: 4001, seat: 1}, base, signalRejectionStreak)
	multiFires(t, &tr, seatWindowKey{matchID: 4001, seat: 1}, base.Add(2*time.Minute), signalMetronomic)
	// seat 2: clean.

	tr.clear(4001, 3)
	if got := tr.closedWithSignals.Load(); got != 1 {
		t.Fatalf("closedWithSignals: want 1, got %d", got)
	}
	if got := tr.closedFlaggedSeats.Load(); got != 2 {
		t.Fatalf("closedFlaggedSeats: want 2, got %d", got)
	}
	if got := tr.closedFamilySeats.Load(); got != 3 {
		t.Fatalf("closedFamilySeats: want 3, got %d", got)
	}
}

func TestClosedAggregatesStayQuietForCleanMatch(t *testing.T) {
	var tr behaviorTracker
	base := time.Unix(1700000000, 0)
	// Two seats, sub-threshold noise only (human jitter, no family fires).
	gaps := []time.Duration{180, 320, 540, 260, 710, 410, 230, 640, 390, 505}
	for seat := 0; seat < 2; seat++ {
		key := seatWindowKey{matchID: 5001, seat: seat}
		clock := base
		for i := 0; i < 15; i++ {
			clock = clock.Add(gaps[i%len(gaps)] * time.Millisecond)
			tr.observe(key, clock, match.ResultAccepted, "word", 1)
		}
	}
	tr.clear(5001, 2)
	if tr.closedWithSignals.Load() != 0 || tr.closedFlaggedSeats.Load() != 0 || tr.closedFamilySeats.Load() != 0 {
		t.Fatalf("clean match must not advance the closed aggregates")
	}
}

func TestClosedScanUsesFinalFamilyMaskNotEventCounts(t *testing.T) {
	// A seat that fires the SAME family twice still closes with exactly one
	// family incidence - the final mask, not the event-time counter, is the
	// longitudinal fact.
	var tr behaviorTracker
	base := time.Unix(1700000000, 0)
	key := seatWindowKey{matchID: 6001, seat: 0}
	multiFires(t, &tr, key, base, signalRejectionStreak)
	tr.observe(key, base.Add(time.Minute), match.ResultAccepted, "word", 1) // re-arm
	multiFires(t, &tr, key, base.Add(2*time.Minute), signalRejectionStreak)
	tr.clear(6001, 1)
	if got := tr.closedFamilySeats.Load(); got != 1 {
		t.Fatalf("repeated family must close as ONE incidence, got %d", got)
	}
	if got := tr.closedFlaggedSeats.Load(); got != 1 {
		t.Fatalf("closedFlaggedSeats: want 1, got %d", got)
	}
	if got := tr.closedWithSignals.Load(); got != 1 {
		t.Fatalf("closedWithSignals: want 1, got %d", got)
	}
}

func TestFlashPathIgnoresShortPathsAndSlowSubmits(t *testing.T) {
	var tr behaviorTracker
	base := time.Unix(1700000000, 0)
	// Fast but short paths (a held-down tap): not a flash. (Exact intervals
	// legitimately fire the 40A metronomic detector - only the flash signal
	// is asserted here.)
	k1 := seatWindowKey{matchID: 32, seat: 0}
	for i := 1; i <= 12; i++ {
		if got := countSignals(tr.observe(k1, behaviorAt(base, i, 200*time.Millisecond), match.ResultAccepted, "word", 3), signalFlashPath); got != 0 {
			t.Fatalf("short fast path fired flash_path %d times", got)
		}
	}
	// Slow multi-cell submits: reading the board takes time, fine.
	k2 := seatWindowKey{matchID: 33, seat: 0}
	for i := 1; i <= 12; i++ {
		if got := countSignals(tr.observe(k2, behaviorAt(base, i, 2*time.Second), match.ResultAccepted, "word", 8), signalFlashPath); got != 0 {
			t.Fatalf("slow multi-cell path fired flash_path %d times", got)
		}
	}
	if tr.flashPathEvents.Load() != 0 {
		t.Fatalf("flash path counter: want 0, got %d", tr.flashPathEvents.Load())
	}
}
