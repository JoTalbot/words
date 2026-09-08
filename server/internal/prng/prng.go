// Package prng provides the deterministic PRNG used for all match content.
//
// Word Arena matches must be reproducible across processes, platforms and
// time. No wall-clock randomness, map-iteration randomness or concurrency
// scheduling may influence game content. This package fixes the algorithm
// and version so that replays and golden tests stay stable.
package prng

import "encoding/binary"

// Version identifies the deterministic stream algorithm. Bump only with a
// protocol-level content-generation change and keep replay compatibility
// analysis attached.
const Version = 1

// splitmix64 is the well-defined SplitMix64 finalizer (Steele, Vigna).
func splitmix64(seed uint64) uint64 {
	seed += 0x9E3779B97F4A7C15
	z := seed
	z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9
	z = (z ^ (z >> 27)) * 0x94D049BB133111EB
	return z ^ (z >> 31)
}

// splitmix64Stream generates the 256-bit state seed for xoshiro256**.
func splitmix64Stream(seed uint64) [4]uint64 {
	var s [4]uint64
	for i := range s {
		seed += 0x9E3779B97F4A7C15
		z := seed
		z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9
		z = (z ^ (z >> 27)) * 0x94D049BB133111EB
		s[i] = z ^ (z >> 31)
	}
	return s
}

// Source is a deterministic xoshiro256** stream. The zero value is NOT a
// valid source; construct via New or NewFromKey.
type Source struct {
	s [4]uint64
}

// New seeds a fresh source from a single uint64.
func New(seed uint64) *Source {
	return NewFromKey(seed, 0, 0, 0)
}

// NewFromKey derives a stream from a key tuple. Domain separation (match id,
// language, wave, component) prevents correlated outputs across uses.
func NewFromKey(k0, k1, k2, k3 uint64) *Source {
	s := splitmix64Stream(k0 ^ 0x9E3779B97F4A7C15)
	s[0] ^= k1
	s[1] ^= k2
	s[2] ^= k3
	// xoshiro state must not be all zero; remix if needed.
	if s[0]|s[1]|s[2]|s[3] == 0 {
		s = splitmix64Stream(0xD1B54A32D192ED03)
	}
	return &Source{s: s}
}

func rotl(x uint64, k uint) uint64 { return (x << k) | (x >> (64 - k)) }

// Uint64 returns the next value in the stream.
func (src *Source) Uint64() uint64 {
	s := &src.s
	result := rotl(s[1]*5, 7) * 9
	t := s[1] << 17
	s[2] ^= s[0]
	s[3] ^= s[1]
	s[1] ^= s[2]
	s[0] ^= s[3]
	s[2] ^= t
	s[3] = rotl(s[3], 45)
	return result
}

// Intn returns a uniform value in [0, n). n must be > 0.
func (src *Source) Intn(n int) int {
	if n <= 0 {
		panic("prng: Intn(n) requires n > 0")
	}
	// Lemire's nearly-divisionless method keeps the mapping uniform and fast.
	limit := uint64(n)
	v := src.Uint64()
	lo := uint64(uint32(v)) * limit
	if uint32(lo) >= limit {
		return int(lo >> 32)
	}
	hi := v >> 32
	for hi < limit {
		v = src.Uint64()
		hi = uint64(uint32(v)) * limit
		lo = uint32(v) * limit // recompute with fresh low bits
		if uint32(lo) >= limit {
			return int(lo >> 32)
		}
		hi = v >> 32
	}
	return int(lo >> 32)
}

// SeedBytes writes a deterministic 32-byte seed derived from the current
// state, usable for stdlib math/rand if a caller needs it.
func (src *Source) SeedBytes(dst []byte) {
	if len(dst) != 32 {
		panic("prng: SeedBytes requires len 32")
	}
	var tmp [4]uint64
	for i := range tmp {
		tmp[i] = src.Uint64()
	}
	for i := range tmp {
		binary.LittleEndian.PutUint64(dst[i*8:], tmp[i])
	}
}
