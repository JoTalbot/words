package matchroom

import (
	"fmt"
	"sync"
	"testing"

	"github.com/JoTalbot/words/server/internal/match"
)

// Batch 31B: the shared payload is what stops snapshot fan-out from scaling
// with the roster. These tests pin the contract the transport relies on.

func TestSharedPayloadEncodesOnce(t *testing.T) {
	p := NewSharedPayload()
	calls := 0
	enc := func() ([]byte, error) {
		calls++
		return []byte("payload"), nil
	}
	for i := 0; i < 60; i++ {
		b, err := p.Bytes(enc)
		if err != nil {
			t.Fatalf("subscriber %d: %v", i, err)
		}
		if string(b) != "payload" {
			t.Fatalf("subscriber %d got %q", i, b)
		}
	}
	if calls != 1 {
		t.Fatalf("encoded %d times for 60 subscribers, want exactly 1", calls)
	}
}

func TestSharedPayloadEncodesOnceUnderConcurrency(t *testing.T) {
	// Subscribers are separate goroutines in the transport, so the
	// once-only guarantee has to hold under the race detector too.
	p := NewSharedPayload()
	var mu sync.Mutex
	calls := 0
	var wg sync.WaitGroup
	for i := 0; i < 60; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			b, err := p.Bytes(func() ([]byte, error) {
				mu.Lock()
				calls++
				mu.Unlock()
				return []byte("x"), nil
			})
			if err != nil || string(b) != "x" {
				t.Errorf("bad payload %q err=%v", b, err)
			}
		}()
	}
	wg.Wait()
	if calls != 1 {
		t.Fatalf("encoded %d times concurrently, want 1", calls)
	}
}

func TestSharedPayloadPropagatesTheEncodeError(t *testing.T) {
	p := NewSharedPayload()
	want := fmt.Errorf("boom")
	for i := 0; i < 3; i++ {
		if _, err := p.Bytes(func() ([]byte, error) { return nil, want }); err != want {
			t.Fatalf("call %d returned %v, want the encoder error", i, err)
		}
	}
}

func TestNilSharedPayloadStillEncodes(t *testing.T) {
	// A frame built without a box (older callers, tests) must keep working.
	var p *SharedPayload
	b, err := p.Bytes(func() ([]byte, error) { return []byte("ok"), nil })
	if err != nil || string(b) != "ok" {
		t.Fatalf("nil box returned %q / %v", b, err)
	}
}

// TestBroadcastSharesOneEncodingAcrossTheRoster is the end of the argument:
// every seat of a large room must receive the SAME box, because that identity
// is what makes the encoding happen once.
func TestBroadcastSharesOneEncodingAcrossTheRoster(t *testing.T) {
	const seats = 30
	ids := make([]uint64, seats)
	for i := range ids {
		ids[i] = uint64(i + 1)
	}
	r, err := New(Config{MatchID: 1, Seed: 1, Language: "en", SeatUserIDs: ids})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	subs := make([]*Subscription, seats)
	for i := range subs {
		subs[i] = r.Subscribe(match.Seat(i))
	}
	// Drive the room to a snapshot boundary (one per second).
	for i := 0; i < match.TicksPerSecond; i++ {
		r.Tick()
	}

	var box *SharedPayload
	for i, s := range subs {
		select {
		case f := <-s.Snapshots:
			if f.Encoded == nil {
				t.Fatalf("seat %d received a frame with no shared encoding", i)
			}
			if box == nil {
				box = f.Encoded
				continue
			}
			if f.Encoded != box {
				t.Fatalf("seat %d received a different encoding box; the snapshot would be marshalled per connection", i)
			}
		default:
			t.Fatalf("seat %d received no snapshot", i)
		}
	}

	// And the box really does encode only once across all of them.
	calls := 0
	for range subs {
		_, _ = box.Bytes(func() ([]byte, error) { calls++; return []byte("s"), nil })
	}
	if calls != 1 {
		t.Fatalf("the shared box encoded %d times for %d seats", calls, seats)
	}
}
