// Batch 38A regression test: the 37B soak proved (pprof: +2 goroutines per
// closed websocket client; 224 handlers stacked at api.go:1959 + 224 writers
// parked in their select) that a client-initiated disconnect deadlocked
// BOTH handleWS goroutines forever: the handler waited on <-done while the
// writer waited on r.Context().Done(), which is only cancelled when the
// handler returns. The fix runs the writer on a handler-cancelled context
// and cancels it BEFORE waiting on <-done.
//
// Expected post-fix shape: each cycle below abandons one never-finished
// match, whose room stays resumable by SeatTTL and therefore keeps exactly
// ONE legitimate goroutine (its runRoomTicker). Any further growth is a
// connection leak: pre-fix the delta was 5 per cycle (4 handler goroutines
// + 1 ticker), post-fix it must be 1.
package main

import (
	"net/http/httptest"
	"runtime"
	"testing"
	"time"
)

func TestWSGoroutinesReapedOnClientDisconnect(t *testing.T) {
	api := NewAPI()
	srv := httptest.NewServer(api.Routes())
	defer srv.Close()

	cycle := func() {
		seed := uint64(e2eSeed)
		id, tokens, _ := createMatch(t, srv, "en", &seed)
		c0 := dial(t, srv, id, tokens[0])
		c1 := dial(t, srv, id, tokens[1])
		_ = c0.readSnapshot(t)
		_ = c1.readSnapshot(t)
		c0.close()
		c1.close()
	}
	settle := func() int {
		// Let the room ticker epilogue (over+3s grace) play out, then wait
		// for the count to stop moving.
		time.Sleep(1100 * time.Millisecond)
		last := runtime.NumGoroutine()
		for i := 0; i < 30; i++ {
			time.Sleep(200 * time.Millisecond)
			if n := runtime.NumGoroutine(); n == last {
				return n
			} else {
				last = n
			}
		}
		return last
	}

	cycle() // warm-up
	c1 := settle()
	cycle()
	c2 := settle()
	cycle()
	c3 := settle()
	d1, d2 := c2-c1, c3-c2
	if d1 > 1 || d2 > 1 {
		t.Fatalf("per-connection goroutine leak: deltas %d,%d between cycles (want <=1 = one room ticker)", d1, d2)
	}
}
