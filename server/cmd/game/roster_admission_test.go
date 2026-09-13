package main

import (
	"testing"

	"github.com/JoTalbot/words/server/internal/match"
)

// Batch 30C: room provisioning and queue admission are roster-shaped. These
// tests pin the two things that could silently break: the 1v1 behaviour every
// current client depends on, and the N-way path the Royale mode needs.

func TestCreateRoomKeepsTheOneVsOneUserIDFormula(t *testing.T) {
	// The live deployment and existing fixtures rely on (id*2+1, id*2+2).
	// 30C generalized the formula to id*seats+i+1, which must reduce to the
	// old one for two seats - otherwise every stored match result changes
	// meaning.
	a := NewAPI()
	defer a.Stop()
	id, _, tokens, userIDs, access, err := a.createRoom("en", nil, nil, false)
	if err != nil {
		t.Fatalf("createRoom: %v", err)
	}
	if userIDs[0] != id*2+1 || userIDs[1] != id*2+2 {
		t.Fatalf("user ids %v for match %d, want (%d,%d)", userIDs, id, id*2+1, id*2+2)
	}
	if tokens[0] == "" || tokens[1] == "" || tokens[0] == tokens[1] {
		t.Fatalf("tokens %v must be two distinct non-empty credentials", tokens)
	}
	if access.Code == "" || access.ReadCap == "" {
		t.Fatal("match code and read capability must both be issued")
	}
}

func TestCreateRoomNProvisionsALargeRoster(t *testing.T) {
	a := NewAPI()
	defer a.Stop()
	const seats = 12
	info, err := a.createRoomN("en", nil, nil, seats, false)
	if err != nil {
		t.Fatalf("createRoomN(%d): %v", seats, err)
	}
	if len(info.Tokens) != seats || len(info.UserIDs) != seats {
		t.Fatalf("got %d tokens / %d user ids, want %d of each", len(info.Tokens), len(info.UserIDs), seats)
	}
	seen := map[string]bool{}
	for i, tok := range info.Tokens {
		if tok == "" {
			t.Fatalf("seat %d has an empty token", i)
		}
		if seen[tok] {
			t.Fatalf("seat %d reuses a token issued to another seat", i)
		}
		seen[tok] = true
	}
	ids := map[uint64]bool{}
	for i, uid := range info.UserIDs {
		if uid == 0 {
			t.Fatalf("seat %d has user id 0; ids must stay non-zero on the wire", i)
		}
		if ids[uid] {
			t.Fatalf("seat %d duplicates user id %d", i, uid)
		}
		ids[uid] = true
	}

	a.mu.Lock()
	room, ok := a.rooms[info.ID]
	a.mu.Unlock()
	if !ok {
		t.Fatal("the provisioned room is not registered")
	}
	if room.Seats() != seats {
		t.Fatalf("room hosts %d seats, want %d", room.Seats(), seats)
	}
	for i, tok := range info.Tokens {
		seat, ok := room.SeatForToken(tok)
		if !ok || int(seat) != i {
			t.Fatalf("token %d authenticates seat %v (ok=%v), want seat %d", i, seat, ok, i)
		}
	}
}

func TestCreateRoomNRejectsAnImpossibleRoster(t *testing.T) {
	a := NewAPI()
	defer a.Stop()
	for _, seats := range []int{0, 1, match.MaxSeats + 1} {
		if _, err := a.createRoomN("en", nil, nil, seats, false); err == nil {
			t.Errorf("seats=%d was accepted; the bound must be enforced before allocating", seats)
		}
	}
	if _, err := a.createRoomN("en", nil, []uint64{1, 2, 3}, 2, false); err == nil {
		t.Error("more player_ids than seats must be rejected rather than truncated")
	}
}

func TestCreateRoomNBindsProfilesPositionally(t *testing.T) {
	a := NewAPI()
	defer a.Stop()
	info, err := a.createRoomN("en", nil, []uint64{0, 4242, 0, 777}, 4, false)
	if err != nil {
		t.Fatalf("createRoomN: %v", err)
	}
	if info.UserIDs[1] != 4242 || info.UserIDs[3] != 777 {
		t.Fatalf("bound seats have ids %d and %d, want 4242 and 777", info.UserIDs[1], info.UserIDs[3])
	}
	for _, seat := range []int{0, 2} {
		if info.UserIDs[seat] == 0 {
			t.Fatalf("anonymous seat %d must keep a non-zero synthetic id", seat)
		}
		if info.UserIDs[seat] == 4242 || info.UserIDs[seat] == 777 {
			t.Fatalf("anonymous seat %d took a bound profile id", seat)
		}
	}
}

func TestMatchmakerFormsAnNWayLobby(t *testing.T) {
	const seats = 5
	mm := newMatchmakerWithSeats(seats)
	var created int
	mk := func(_ string, pids []uint64) (roomInfo, error) {
		if len(pids) != seats {
			t.Fatalf("factory received %d player ids, want %d (its length is the roster size)", len(pids), seats)
		}
		created++
		tokens := make([]string, seats)
		users := make([]uint64, seats)
		for i := range tokens {
			tokens[i] = string(rune('a' + i))
			users[i] = uint64(100 + i)
		}
		return roomInfo{ID: 9, Seed: 5, Tokens: tokens, UserIDs: users,
			Access: matchAccess{Code: "code-9", ReadCap: "cap-9"}}, nil
	}

	entries := make([]*queueEntry, 0, seats)
	for i := 0; i < seats-1; i++ {
		e := mm.enqueue("en", 0, mk)
		if e.Status != "waiting" {
			t.Fatalf("entry %d matched with only %d players queued", i, i+1)
		}
		entries = append(entries, e)
	}
	if created != 0 {
		t.Fatalf("a room was created before the lobby was full (%d)", created)
	}
	entries = append(entries, mm.enqueue("en", 0, mk))
	if created != 1 {
		t.Fatalf("rooms created = %d, want exactly 1 once the lobby filled", created)
	}
	for i, e := range entries {
		if e.Status != "matched" {
			t.Fatalf("seat %d status %q, want matched", i, e.Status)
		}
		if e.MatchID != 9 || e.Seed != 5 {
			t.Fatalf("seat %d joined match %d seed %d, want 9/5", i, e.MatchID, e.Seed)
		}
		if e.UserID != uint64(100+i) || e.Token != string(rune('a'+i)) {
			t.Fatalf("seat %d got token %q / user %d; FIFO seat order was not preserved", i, e.Token, e.UserID)
		}
		if e.MatchCode != "code-9" || e.ReadCapability != "cap-9" {
			t.Fatalf("seat %d lost the read handle (%q/%q)", i, e.MatchCode, e.ReadCapability)
		}
	}
	if got := len(mm.waiting["en"]); got != 0 {
		t.Fatalf("%d players left queued after the lobby formed", got)
	}
}

func TestMatchmakerLeavesPlayersQueuedWhenTheFactoryUnderdelivers(t *testing.T) {
	// A factory that returns fewer seats than requested must not hand out
	// seats that do not exist; the players stay queued for the next attempt.
	mm := newMatchmakerWithSeats(4)
	mk := func(string, []uint64) (roomInfo, error) {
		return roomInfo{ID: 1, Tokens: []string{"a", "b"}, UserIDs: []uint64{1, 2}}, nil
	}
	for i := 0; i < 4; i++ {
		mm.enqueue("en", 0, mk)
	}
	if got := len(mm.waiting["en"]); got != 4 {
		t.Fatalf("%d players queued, want all 4 still waiting", got)
	}
	for _, e := range mm.waiting["en"] {
		if e.Status != "waiting" {
			t.Fatalf("entry status %q, want waiting", e.Status)
		}
	}
}

func TestDefaultMatchmakerIsStillOneVsOne(t *testing.T) {
	mm := newMatchmaker()
	if mm.seats != 2 {
		t.Fatalf("default matchmaker roster %d, want 2 (the M1 vertical slice is 1v1)", mm.seats)
	}
	if got := newMatchmakerWithSeats(1).seats; got != 2 {
		t.Fatalf("a one-player roster was accepted (%d); a match of one is not a match", got)
	}
}
