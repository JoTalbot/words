package main

import (
	"context"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	wordarenav1 "github.com/JoTalbot/words/server/gen/wordarena/net/v1"
	"github.com/JoTalbot/words/server/internal/match"
)

// Batch 30D: the roster work of 30A-30C is only real if a large roster survives
// the actual transport. These tests drive the live HTTP + WebSocket surface,
// not the simulation in isolation.

// TestNWayEndToEndSharedBoard connects every seat of an 8-seat match over the
// real WebSocket route and asserts the three properties a shared board must
// have regardless of roster size: one identical board, every seat visible to
// every other seat, and a word scored for exactly the seat that played it.
func TestNWayEndToEndSharedBoard(t *testing.T) {
	const seats = 8
	api := NewAPI()
	defer api.Stop()
	srv := httptest.NewServer(api.Routes())
	defer srv.Close()

	seed := uint64(e2eSeed)
	info, err := api.createRoomN("en", &seed, nil, seats, false)
	if err != nil {
		t.Fatalf("createRoomN: %v", err)
	}

	clients := make([]*testClient, seats)
	for i := 0; i < seats; i++ {
		c := dial(t, srv, info.ID, info.Tokens[i])
		defer c.close()
		c.seat, c.user = uint64(i), info.UserIDs[i]
		clients[i] = c
	}

	// 1. Every seat receives the same deterministic board as seat 0, and the
	//    board is a prefix-compatible extension of the 1v1 board this seed
	//    pins (Q10: the board grows with the roster, but the letter at cell
	//    i never depends on how many players are seated).
	first := clients[0].readSnapshot(t)
	board := ""
	for _, c := range first.Cells {
		board += c.Letter
	}
	if want := match.BoardCells(seats); len(first.Cells) != want {
		t.Fatalf("%d-seat board has %d cells, want %d", seats, len(first.Cells), want)
	}
	if !strings.HasPrefix(board, "taandsvtgcod") {
		t.Fatalf("seed %d board %q is not an extension of the 1v1 board for this seed", e2eSeed, board)
	}
	for i := 1; i < seats; i++ {
		snap := clients[i].readSnapshot(t)
		if snap.MatchId != info.ID {
			t.Fatalf("seat %d joined match %d, want %d", i, snap.MatchId, info.ID)
		}
		if len(snap.Cells) != len(first.Cells) {
			t.Fatalf("seat %d sees %d cells, seat 0 sees %d", i, len(snap.Cells), len(first.Cells))
		}
		for j := range snap.Cells {
			if snap.Cells[j].Letter != first.Cells[j].Letter {
				t.Fatalf("seat %d disagrees with seat 0 on cell %d", i, j)
			}
		}
		// 2. Every seat is on the wire for every observer: a client cannot
		//    render a scoreboard for players it never receives.
		if len(snap.Players) != seats {
			t.Fatalf("seat %d sees %d players, want %d", i, len(snap.Players), seats)
		}
	}

	// 3. A word played by a middle seat is attributed to that seat alone and
	//    is observed identically by every other seat.
	const actor = 5
	clients[actor].submit(t, info.ID, []uint32{9, 1, 0}, 1) // c,a,t
	for i, c := range clients {
		ev := c.readWordEvent(t)
		if ev.Result != wordarenav1.WordResult_ACCEPTED || ev.NormalizedWord != "cat" {
			t.Fatalf("seat %d observed %v %q, want ACCEPTED cat", i, ev.Result, ev.NormalizedWord)
		}
		if ev.UserId != info.UserIDs[actor] {
			t.Fatalf("seat %d attributes the word to user %d, want seat %d (user %d)",
				i, ev.UserId, actor, info.UserIDs[actor])
		}
	}

	// 4. After live ticking, the scoreboard credits only the actor and every
	//    seat agrees on the same canonical version.
	time.Sleep(1600 * time.Millisecond)
	ref := clients[0].readSnapshot(t)
	for i, p := range ref.Players {
		want := uint32(0)
		if i == actor {
			want = 5
		}
		if p.Score != want {
			t.Fatalf("seat %d score %d, want %d", i, p.Score, want)
		}
		if p.UserId != info.UserIDs[i] {
			t.Fatalf("scoreboard row %d carries user %d, want %d", i, p.UserId, info.UserIDs[i])
		}
	}
	for i := 1; i < seats; i++ {
		snap := clients[i].readSnapshot(t)
		if snap.StateVersion != ref.StateVersion || snap.ServerTick != ref.ServerTick {
			t.Fatalf("seat %d diverged: ver %d/%d tick %d/%d",
				i, snap.StateVersion, ref.StateVersion, snap.ServerTick, ref.ServerTick)
		}
	}
}

// TestNWayRoomRejectsAForeignToken makes sure widening the roster did not widen
// authentication: a token minted for a different match must not open a socket,
// and every seat token must be single-purpose.
func TestNWayRoomRejectsAForeignToken(t *testing.T) {
	api := NewAPI()
	defer api.Stop()
	srv := httptest.NewServer(api.Routes())
	defer srv.Close()

	a, err := api.createRoomN("en", nil, nil, 6, false)
	if err != nil {
		t.Fatalf("createRoomN: %v", err)
	}
	b, err := api.createRoomN("en", nil, nil, 6, false)
	if err != nil {
		t.Fatalf("createRoomN: %v", err)
	}
	url := "ws" + strings.TrimPrefix(srv.URL, "http") +
		fmt.Sprintf("/v1/match/ws?match_id=%d&token=%s", a.ID, b.Tokens[3])
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if conn, _, err := websocket.Dial(ctx, url, nil); err == nil {
		conn.Close(websocket.StatusNormalClosure, "test")
		t.Fatal("a token from another match opened a socket on this match")
	}
}
