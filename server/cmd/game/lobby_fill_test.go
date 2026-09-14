package main

import (
	"testing"
	"time"
)

// Batch 31C: a Royale lobby must not depend on sixty people queueing at the
// same moment. These tests pin the short-handed start and, just as important,
// that 1v1 behaviour is untouched.

// fakeClock is an injectable time source so the fill timeout is tested without
// sleeping.
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func stubFactory(t *testing.T, created *int) createRoomFn {
	t.Helper()
	return func(_ string, pids []uint64, _ []bool) (roomInfo, error) {
		*created++
		n := len(pids)
		tokens := make([]string, n)
		users := make([]uint64, n)
		for i := range tokens {
			tokens[i] = string(rune('a' + i))
			users[i] = uint64(100 + i)
		}
		return roomInfo{ID: 7, Seed: 3, Tokens: tokens, UserIDs: users,
			Access: matchAccess{Code: "c", ReadCap: "r"}}, nil
	}
}

func TestPartialRoyaleLobbyStartsAfterTheFillWait(t *testing.T) {
	clk := &fakeClock{t: time.Now()}
	mm := newMatchmakerWithSeats(30)
	mm.now = clk.now
	created := 0
	mk := stubFactory(t, &created)

	// Five players queue; nowhere near the 30-seat roster.
	entries := make([]*queueEntry, 0, 5)
	for i := 0; i < 5; i++ {
		entries = append(entries, mm.enqueue("en", 0, mk))
	}
	for i, e := range entries {
		if e.Status != "waiting" {
			t.Fatalf("entry %d started immediately with only 5 of 30 seats filled", i)
		}
	}
	if created != 0 {
		t.Fatalf("%d rooms created before the fill wait elapsed", created)
	}

	// Before the deadline, a periodic sweep must still not start it.
	clk.advance(lobbyFillWait - time.Second)
	mm.tryFormLobbies(mk)
	if created != 0 {
		t.Fatalf("lobby started %s early", time.Second)
	}

	// Once the oldest player has waited it out, the lobby starts with five.
	clk.advance(2 * time.Second)
	mm.tryFormLobbies(mk)
	if created != 1 {
		t.Fatalf("%d rooms created after the fill wait, want 1", created)
	}
	for i, e := range entries {
		if e.Status != "matched" {
			t.Fatalf("seat %d status %q, want matched", i, e.Status)
		}
		if e.Token != string(rune('a'+i)) || e.UserID != uint64(100+i) {
			t.Fatalf("seat %d got token %q user %d; FIFO order was not preserved", i, e.Token, e.UserID)
		}
	}
	if got := len(mm.waiting["en"]); got != 0 {
		t.Fatalf("%d players left queued", got)
	}
}

func TestFullRosterStillStartsImmediately(t *testing.T) {
	clk := &fakeClock{t: time.Now()}
	mm := newMatchmakerWithSeats(4)
	mm.now = clk.now
	created := 0
	mk := stubFactory(t, &created)

	for i := 0; i < 4; i++ {
		mm.enqueue("en", 0, mk)
	}
	// No clock advance: a full roster must not wait for the fill timeout.
	if created != 1 {
		t.Fatalf("%d rooms created for a full roster, want 1 immediately", created)
	}
}

func TestLobbyNeverStartsWithASinglePlayer(t *testing.T) {
	clk := &fakeClock{t: time.Now()}
	mm := newMatchmakerWithSeats(30)
	mm.now = clk.now
	created := 0
	mk := stubFactory(t, &created)

	e := mm.enqueue("en", 0, mk)
	clk.advance(10 * lobbyFillWait) // far past any deadline
	mm.tryFormLobbies(mk)

	if created != 0 {
		t.Fatal("a lone player was put into a match against nobody")
	}
	if e.Status != "waiting" {
		t.Fatalf("lone player status %q, want waiting", e.Status)
	}
}

func TestOneVsOneNeverStartsShortHanded(t *testing.T) {
	clk := &fakeClock{t: time.Now()}
	mm := newMatchmaker() // 2 seats
	mm.now = clk.now
	created := 0
	mk := stubFactory(t, &created)

	if mm.fillWait != 0 {
		t.Fatalf("1v1 matchmaker has fillWait %s, want short-handed starts disabled", mm.fillWait)
	}
	e := mm.enqueue("en", 0, mk)
	clk.advance(10 * lobbyFillWait)
	mm.tryFormLobbies(mk)
	if created != 0 {
		t.Fatal("a 1v1 queue created a match with one player")
	}
	if e.Status != "waiting" {
		t.Fatalf("status %q, want waiting", e.Status)
	}

	// The opponent arrives: normal pairing, no timeout involved.
	e2 := mm.enqueue("en", 0, mk)
	if created != 1 || e.Status != "matched" || e2.Status != "matched" {
		t.Fatalf("1v1 pairing broke: created=%d statuses %q/%q", created, e.Status, e2.Status)
	}
}

func TestFillWaitIsMeasuredFromTheLongestWaitingPlayer(t *testing.T) {
	// Keying off the newest arrival would let a trickle of players postpone
	// the start forever, so the oldest entry is what counts.
	clk := &fakeClock{t: time.Now()}
	mm := newMatchmakerWithSeats(30)
	mm.now = clk.now
	created := 0
	mk := stubFactory(t, &created)

	mm.enqueue("en", 0, mk) // the long-waiting player
	clk.advance(lobbyFillWait - time.Second)
	mm.enqueue("en", 0, mk) // a late arrival
	clk.advance(2 * time.Second)

	mm.tryFormLobbies(mk)
	if created != 1 {
		t.Fatalf("%d rooms created; a late arrival delayed the start of a lobby that had already waited",
			created)
	}
}

func TestShortHandedStartsAreIsolatedPerLanguage(t *testing.T) {
	clk := &fakeClock{t: time.Now()}
	mm := newMatchmakerWithSeats(30)
	mm.now = clk.now
	created := 0
	mk := stubFactory(t, &created)

	mm.enqueue("en", 0, mk)
	mm.enqueue("en", 0, mk)
	uk := mm.enqueue("uk", 0, mk) // alone in its language
	clk.advance(lobbyFillWait + time.Second)
	mm.tryFormLobbies(mk)

	if created != 1 {
		t.Fatalf("%d rooms created, want exactly the en lobby", created)
	}
	if uk.Status != "waiting" {
		t.Fatalf("the lone uk player was matched (%q); languages must not be mixed", uk.Status)
	}
}
