package match

import (
	"fmt"
	"runtime"
	"testing"
	"time"
)

// Batch 30F: the cost model of the simulation was only ever measured at two
// seats. Before Royale sizing can be decided, we need to know how tick and
// snapshot cost actually scale with the roster - whether a 60-seat match is a
// small multiple of a 1v1 or a different order of magnitude.

func benchRosterTick(b *testing.B, seats int) {
	m, err := New(Config{MatchID: 1, Seed: 0xABCD, Lang: "en", Seats: seats})
	if err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.AdvanceTicks(1)
	}
}

func BenchmarkTickSeats2(b *testing.B)  { benchRosterTick(b, 2) }
func BenchmarkTickSeats16(b *testing.B) { benchRosterTick(b, 16) }
func BenchmarkTickSeats60(b *testing.B) { benchRosterTick(b, 60) }

func benchRosterSnapshot(b *testing.B, seats int) {
	m, err := New(Config{MatchID: 1, Seed: 0xABCD, Lang: "en", Seats: seats})
	if err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = m.Snapshot()
	}
}

func BenchmarkSnapshotSeats2(b *testing.B)  { benchRosterSnapshot(b, 2) }
func BenchmarkSnapshotSeats16(b *testing.B) { benchRosterSnapshot(b, 16) }
func BenchmarkSnapshotSeats60(b *testing.B) { benchRosterSnapshot(b, 60) }

// TestRosterLoadBaseline reports how a full-roster match behaves against the
// 30 Hz budget. It is a measurement, not a threshold test, except for one
// guard: a single 60-seat match must still simulate far faster than real time
// on one core, otherwise Royale is not viable on the current host at all.
func TestRosterLoadBaseline(t *testing.T) {
	if testing.Short() {
		t.Skip("roster load baseline skipped in -short")
	}
	const ticks = 3000 // 100 seconds of match time at 30 Hz
	type row struct {
		seats            int
		tickNs, snapNs   float64
		heapKiB          float64
		realtimeHeadroom float64
	}
	var rows []row

	for _, seats := range []int{2, 8, 16, 30, 60} {
		m, err := New(Config{MatchID: 1, Seed: 0xABCD, Lang: "en", Seats: seats})
		if err != nil {
			t.Fatalf("%d seats: %v", seats, err)
		}
		var before, after runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&before)

		start := time.Now()
		for i := 0; i < ticks; i++ {
			m.AdvanceTicks(1)
		}
		tickElapsed := time.Since(start)

		const snaps = 2000
		start = time.Now()
		for i := 0; i < snaps; i++ {
			_ = m.Snapshot()
		}
		snapElapsed := time.Since(start)
		runtime.ReadMemStats(&after)

		heap := float64(0)
		if after.HeapAlloc > before.HeapAlloc {
			heap = float64(after.HeapAlloc-before.HeapAlloc) / 1024
		}
		tickNs := float64(tickElapsed.Nanoseconds()) / ticks
		// One tick has 1/30 s of wall clock; how many times faster than real
		// time can one core drive this match?
		headroom := (float64(time.Second) / TicksPerSecond) / tickNs
		rows = append(rows, row{seats, tickNs, float64(snapElapsed.Nanoseconds()) / snaps, heap, headroom})
	}

	t.Log("roster load baseline (one match, one core):")
	for _, r := range rows {
		t.Logf("  seats=%-3d tick=%8.0f ns  snapshot=%8.0f ns  heap=%7.1f KiB  realtime_headroom=x%.0f",
			r.seats, r.tickNs, r.snapNs, r.heapKiB, r.realtimeHeadroom)
		fmt.Printf("ROSTER-BASELINE seats=%d tick_ns=%.0f snapshot_ns=%.0f heap_kib=%.1f headroom=%.0f\n",
			r.seats, r.tickNs, r.snapNs, r.heapKiB, r.realtimeHeadroom)
	}
	t.Logf("  host logical CPUs: %d", runtime.NumCPU())

	last := rows[len(rows)-1]
	if last.realtimeHeadroom < 50 {
		t.Fatalf("a %d-seat match runs only x%.1f faster than real time on one core; "+
			"Royale would not fit the 30 Hz budget with any concurrency",
			last.seats, last.realtimeHeadroom)
	}
	// Scaling shape: snapshot cost is expected to grow with the roster (it
	// renders every player), tick cost should stay dominated by the board.
	first := rows[0]
	if last.tickNs > first.tickNs*30 {
		t.Fatalf("tick cost grew from %.0f ns at %d seats to %.0f ns at %d seats - "+
			"the per-tick path is scaling with the roster, which the board-sized design does not expect",
			first.tickNs, first.seats, last.tickNs, last.seats)
	}
}
