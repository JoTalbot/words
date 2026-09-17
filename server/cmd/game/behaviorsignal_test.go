package main

import (
	"testing"
	"time"

	"github.com/JoTalbot/words/server/internal/match"
)

// deterministic clock helpers: every test drives observe with explicit times
// so the cadence detector sees exact gaps.

func behaviorAt(base time.Time, i int, gap time.Duration) time.Time {
	return base.Add(time.Duration(i) * gap)
}

func TestRejectionStreakFiresOncePerEpisode(t *testing.T) {
	var tr behaviorTracker
	key := seatWindowKey{matchID: 7, seat: 3}
	base := time.Unix(1700000000, 0)
	count := func(res match.WordResult, from, to int) int {
		n := 0
		for i := from; i <= to; i++ {
			for _, s := range tr.observe(key, behaviorAt(base, i, 300*time.Millisecond), res) {
				if s == signalRejectionStreak {
					n++
				}
			}
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
	tr.observe(key, behaviorAt(base, 31, 300*time.Millisecond), match.ResultAccepted)
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
		for _, s := range tr.observe(key, clock, match.ResultMatchNotActive) {
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
		for _, s := range tr.observe(key, behaviorAt(base, i, 400*time.Millisecond), match.ResultAccepted) {
			if s == signalMetronomic {
				fired++
			}
		}
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
		for _, s := range tr.observe(key, clock, match.ResultAccepted) {
			t.Fatalf("jittered cadence fired %q", s)
		}
	}
}

func TestBurstCadenceBelongsToRateLimiterNotBehavior(t *testing.T) {
	var tr behaviorTracker
	key := seatWindowKey{matchID: 4, seat: 1}
	base := time.Unix(1700000000, 0)
	for i := 1; i <= 40; i++ {
		for _, s := range tr.observe(key, behaviorAt(base, i, 50*time.Millisecond), match.ResultAccepted) {
			t.Fatalf("burst cadence fired %q (rate limiter's domain)", s)
		}
	}
}

func TestIdleCadenceNotAMetronome(t *testing.T) {
	var tr behaviorTracker
	key := seatWindowKey{matchID: 5, seat: 0}
	base := time.Unix(1700000000, 0)
	for i := 1; i <= 20; i++ {
		for _, s := range tr.observe(key, behaviorAt(base, i, 30*time.Second), match.ResultAccepted) {
			t.Fatalf("idle cadence fired %q", s)
		}
	}
}

func TestClearResetsSeatStateButNotCounters(t *testing.T) {
	var tr behaviorTracker
	key := seatWindowKey{matchID: 11, seat: 4}
	base := time.Unix(1700000000, 0)
	for i := 1; i < rejectionStreakSignalAt; i++ {
		tr.observe(key, behaviorAt(base, i, 300*time.Millisecond), match.ResultBlockedByRule)
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
		for _, s := range tr.observe(key, clock, match.ResultBlockedByRule) {
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
