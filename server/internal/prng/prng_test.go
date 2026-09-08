package prng

import "testing"

// Golden values below were produced by an independent reference
// implementation of splitmix64-seeded xoshiro256** (Python), not by this
// code. Any change to the algorithm breaks these tests.

func TestGoldenStreamNew(t *testing.T) {
	src := New(0x1234567890ABCDEF)
	want := []uint64{
		0x241BD74540251D1F,
		0x3D7E052B82547EEF,
		0x57D14F77B1F54F23,
		0x1C340EF5BCEB9A6C,
		0x2FFD083634A6457C,
	}
	for i, w := range want {
		if got := src.Uint64(); got != w {
			t.Fatalf("stream[%d] = 0x%016X, want 0x%016X", i, got, w)
		}
	}
}

func TestGoldenStreamFromKey(t *testing.T) {
	src := NewFromKey(7, 1, 2, 0)
	want := []uint64{
		0x277ADEF528817DE5,
		0xDF9EAB5B42F59A78,
		0x044B861D01B7DB81,
		0x42EADF3A25975AC2,
		0x49A5A0454EC7DC17,
	}
	for i, w := range want {
		if got := src.Uint64(); got != w {
			t.Fatalf("stream[%d] = 0x%016X, want 0x%016X", i, got, w)
		}
	}
}

func TestGoldenIntnSequence(t *testing.T) {
	src := New(99)
	want := []int{10, 1, 0, 7, 8, 3, 6, 6, 9, 4}
	for i, w := range want {
		if got := src.Intn(12); got != w {
			t.Fatalf("Intn(12)[%d] = %d, want %d", i, got, w)
		}
	}
}

// TestIntnBoundsAndSpread checks Intn stays in range and is roughly uniform.
func TestIntnBoundsAndSpread(t *testing.T) {
	const n, draws = 12, 200000
	src := New(0xC0FFEE)
	counts := make([]int, n)
	for i := 0; i < draws; i++ {
		v := src.Intn(n)
		if v < 0 || v >= n {
			t.Fatalf("Intn(%d) = %d out of range", n, v)
		}
		counts[v]++
	}
	expected := draws / n
	// generous band: reference run gave 16430..16836 around 16667
	lo, hi := expected*95/100, expected*105/100+1
	for v, c := range counts {
		if c < lo || c > hi {
			t.Fatalf("bucket %d count %d outside [%d,%d]", v, c, lo, hi)
		}
	}
}

// TestDeterministicReproducibility: two identical keys produce identical
// streams; different keys diverge (domain separation smoke).
func TestDeterministicReproducibility(t *testing.T) {
	a1, a2 := NewFromKey(1, 2, 3, 4), NewFromKey(1, 2, 3, 4)
	b := NewFromKey(1, 2, 3, 5)
	for i := 0; i < 1000; i++ {
		x, y, z := a1.Uint64(), a2.Uint64(), b.Uint64()
		if x != y {
			t.Fatalf("same key diverged at %d", i)
		}
		_ = z
	}
	// different keys must diverge within the first values with overwhelming
	// probability; check the first output differs across a few probes.
	same := 0
	for i := 0; i < 8; i++ {
		a := NewFromKey(uint64(i), 0, 0, 0)
		if a.Uint64() == b.Uint64() {
			same++
		}
		b.Uint64()
	}
	if same > 0 {
		t.Fatalf("expected key separation, %d/8 first values collided", same)
	}
}
