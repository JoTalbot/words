package dictionary

import "testing"

// BenchmarkContains measures the raw in-process validation cost per word —
// the M0 performance gate (docs/M0.md: benchmark in-process lookup, not
// end-to-end latency). Includes normalization.
func BenchmarkContains(b *testing.B) {
	snap, err := LoadSnapshot(En)
	if err != nil {
		b.Fatal(err)
	}
	words := []string{"CAT", "table", "stone", "running", "qzx", "проверка"}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = snap.Contains(words[i%len(words)])
	}
}

// BenchmarkLoadSnapshot measures one-time cold load of the full en snapshot
// (process start / cache fill).
func BenchmarkLoadSnapshot(b *testing.B) {
	for i := 0; i < b.N; i++ {
		snap, err := LoadSnapshot(Uk)
		if err != nil {
			b.Fatal(err)
		}
		_ = snap
	}
}
