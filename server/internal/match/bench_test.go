package match

import (
	"fmt"
	"runtime"
	"testing"
	"time"
)

// BenchmarkSubmit measures deterministic word submissions (intent handling)
// on one match — the core action cost of the simulation.
func BenchmarkSubmit(b *testing.B) {
	m, err := New(testCfg())
	if err != nil {
		b.Fatal(err)
	}
	path := []int{0, 1, 2, 3, 4, 5}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.Submit(Seat(i%2), path)
	}
}

// BenchmarkTick measures a full 30 Hz simulation step (lock expiry, wave
// bookkeeping, snapshot render every 30 ticks).
func BenchmarkTick(b *testing.B) {
	m, err := New(testCfg())
	if err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.AdvanceTicks(1)
	}
}

// TestLoadBaseline reports measured throughput and memory per active match
// (docs/M0.md acceptance 8). Numbers are engineering baselines measured on
// the dev host; recorded in docs/LOAD-BASELINE.md on each significant
// hardware or algorithm change.
func TestLoadBaseline(t *testing.T) {
	if testing.Short() {
		t.Skip("load baseline skipped in -short")
	}
	const matches = 32
	ms := make([]*Match, matches)
	for i := range ms {
		m, err := New(Config{MatchID: uint64(i + 1), Seed: uint64(1000 + i), Lang: "en"})
		if err != nil {
			t.Fatal(err)
		}
		ms[i] = m
	}
	path := []int{0, 1, 2, 3, 4, 5}
	const subsPerMatch = 2000

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	start := time.Now()
	for i := 0; i < subsPerMatch; i++ {
		for _, m := range ms {
			m.Submit(Seat(i%2), path)
			if i%30 == 0 {
				m.AdvanceTicks(1)
			}
		}
	}
	elapsed := time.Since(start)
	runtime.ReadMemStats(&after)

	subs := matches * subsPerMatch
	rate := float64(subs) / elapsed.Seconds()
	heapDelta := after.HeapAlloc - before.HeapAlloc
	if heapDelta < 0 {
		heapDelta = 0
	}
	t.Logf("load baseline: %d matches, %d submissions, %.1fs elapsed", matches, subs, elapsed.Seconds())
	t.Logf("submissions/sec total  : %.0f", rate)
	t.Logf("submissions/sec/match  : %.0f", rate/float64(matches))
	t.Logf("heap delta             : %d KiB total (%.1f KiB/match)",
		heapDelta/1024, float64(heapDelta)/(1024*float64(matches)))
	t.Logf("host logical CPUs      : %d", runtime.NumCPU())
	fmt.Printf("BASELINE subs_per_sec_total=%.0f per_match=%.1f heap_kib_per_match=%.1f cpus=%d\n",
		rate, rate/float64(matches), float64(heapDelta)/(1024*float64(matches)), runtime.NumCPU())
}
