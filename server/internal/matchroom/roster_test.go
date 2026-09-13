package matchroom

import (
	"testing"

	"github.com/JoTalbot/words/server/internal/match"
)

// Batch 30B: the room hosts a roster of any size. The 1v1 config fields are
// the two-seat spelling of the same thing, so there is exactly one room
// implementation and no second code path to keep in sync.

func TestOneVsOneConfigStillYieldsTwoSeats(t *testing.T) {
	r, err := New(Config{MatchID: 1, Seed: 1512, Language: "en",
		UserIDs: [2]uint64{11, 22}, Token0: "t0", Token1: "t1"})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if r.Seats() != 2 {
		t.Fatalf("seats %d, want 2", r.Seats())
	}
	if r.UserID(0) != 11 || r.UserID(1) != 22 {
		t.Fatalf("user ids %d/%d, want 11/22", r.UserID(0), r.UserID(1))
	}
	for tok, want := range map[string]match.Seat{"t0": 0, "t1": 1} {
		got, ok := r.SeatForToken(tok)
		if !ok || got != want {
			t.Fatalf("token %q -> seat %v ok=%v, want %v", tok, got, ok, want)
		}
	}
}

func TestRosterConfigHostsManySeats(t *testing.T) {
	users := make([]uint64, 0, 12)
	tokens := make([]string, 0, 12)
	for i := 0; i < 12; i++ {
		users = append(users, uint64(100+i))
		tokens = append(tokens, string(rune('a'+i)))
	}
	r, err := New(Config{MatchID: 2, Seed: 7, Language: "en",
		SeatUserIDs: users, SeatTokens: tokens})
	if err != nil {
		t.Fatalf("new roster room: %v", err)
	}
	if r.Seats() != 12 {
		t.Fatalf("seats %d, want 12", r.Seats())
	}
	for i := range users {
		if r.UserID(match.Seat(i)) != users[i] {
			t.Fatalf("seat %d user id %d, want %d", i, r.UserID(match.Seat(i)), users[i])
		}
		seat, ok := r.SeatForToken(tokens[i])
		if !ok || int(seat) != i {
			t.Fatalf("token %q -> seat %v ok=%v, want %d", tokens[i], seat, ok, i)
		}
	}
	if got := len(r.Snapshot().Players); got != 12 {
		t.Fatalf("snapshot players %d, want 12", got)
	}
	if got := len(r.UserIDs()); got != 12 {
		t.Fatalf("UserIDs() length %d, want 12", got)
	}
}

func TestPartialTokensAreAllowed(t *testing.T) {
	// Seats can join later (the matchmaker fills a Royale lobby over time),
	// so a short token slice must not be an error.
	r, err := New(Config{MatchID: 3, Seed: 7, Language: "en",
		SeatUserIDs: []uint64{1, 2, 3, 4}, SeatTokens: []string{"only-first"}})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if _, ok := r.SeatForToken("only-first"); !ok {
		t.Fatal("the provided token must authenticate seat 0")
	}
	r.SetToken(3, "late-join")
	seat, ok := r.SeatForToken("late-join")
	if !ok || seat != 3 {
		t.Fatalf("late token -> seat %v ok=%v, want 3", seat, ok)
	}
}

func TestTokenBeyondTheRosterIsRejected(t *testing.T) {
	_, err := New(Config{MatchID: 4, Seed: 7, Language: "en",
		SeatUserIDs: []uint64{1, 2}, SeatTokens: []string{"a", "b", "c"}})
	if err == nil {
		t.Fatal("a token for a seat outside the roster must be rejected, not silently dropped")
	}
}

func TestOutOfRangeSeatUserIDIsZeroNotAPanic(t *testing.T) {
	r, err := New(Config{MatchID: 5, Seed: 7, Language: "en", UserIDs: [2]uint64{1, 2}})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if got := r.UserID(9); got != 0 {
		t.Fatalf("out-of-range seat user id %d, want 0", got)
	}
}

// TestSubmitAcceptsEverySeatInTheRoster pins the 30D fix: the submit path used
// to reject any seat above 1, so in a Royale roster every player except the
// first two was silently unable to play. The bound is the roster size.
func TestSubmitAcceptsEverySeatInTheRoster(t *testing.T) {
	const seats = 7
	ids := make([]uint64, seats)
	for i := range ids {
		ids[i] = uint64(i + 1)
	}
	r, err := New(Config{MatchID: 1, Seed: 1, Language: "en", SeatUserIDs: ids})
	if err != nil {
		t.Fatalf("new room: %v", err)
	}
	defer r.Close()
	for seat := 0; seat < seats; seat++ {
		// An empty path is a legal intent that the simulation rejects on
		// content; what matters here is that the room does not refuse the
		// seat itself.
		if _, err := r.Submit(match.Seat(seat), nil); err != nil {
			t.Fatalf("seat %d cannot submit: %v", seat, err)
		}
	}
	if _, err := r.Submit(match.Seat(seats), nil); err == nil {
		t.Fatalf("seat %d is outside the roster and must be refused", seats)
	}
	if _, err := r.Submit(match.Seat(-1), nil); err == nil {
		t.Fatal("a negative seat must be refused")
	}
}
