package matchroom

import "sync"

// SharedPayload is a wire encoding computed at most once and reused by every
// subscriber of the same frame (batch 31B).
//
// A canonical snapshot is identical for all seats, but the transport used to
// re-render and re-marshal it once per connection. At 1v1 that waste is
// invisible; at 60 seats it is 60x the CPU and 60x the garbage for bytes that
// are byte-for-byte the same. Measured on the dev sandbox, fanning one
// 60-seat snapshot out to 60 subscribers cost ~880 us and ~8 280 allocations
// when encoded per subscriber, versus ~15 us and ~138 allocations when encoded
// once - a 59x reduction (docs/LOAD-BASELINE.md).
//
// The room stays protocol-agnostic: it carries the lazy box, and the transport
// supplies the encoder. The first subscriber to ask pays; the rest read the
// cached slice.
//
// The returned bytes are shared and MUST be treated as read-only.
type SharedPayload struct {
	once  sync.Once
	bytes []byte
	err   error
}

// NewSharedPayload returns an empty box; the encoder runs on first use.
func NewSharedPayload() *SharedPayload { return &SharedPayload{} }

// Bytes returns the encoding, running encode exactly once across all callers.
// A nil receiver is valid and simply encodes every time, so a caller that was
// handed no box still works.
func (p *SharedPayload) Bytes(encode func() ([]byte, error)) ([]byte, error) {
	if p == nil {
		return encode()
	}
	p.once.Do(func() { p.bytes, p.err = encode() })
	return p.bytes, p.err
}
