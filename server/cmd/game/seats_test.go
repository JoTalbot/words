package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/JoTalbot/words/server/internal/match"
)

// Batch 32B: the roster size reaches the HTTP surface. Before this, a 60-seat
// match existed in the simulation, the room, the matchmaker and the transport,
// was proven end to end - and could not be requested by any client, because
// POST /v1/matches hard-coded two seats.
//
// The tests below pin three separate things that are easy to conflate: what a
// client may ask for, what this deployment actually offers, and what the game
// can represent at all.

func postCreate(t *testing.T, srv *httptest.Server, body string) (int, createMatchResponse, string) {
	t.Helper()
	resp, err := http.Post(srv.URL+"/v1/matches", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer resp.Body.Close()
	var out createMatchResponse
	raw := ""
	if resp.StatusCode == http.StatusCreated {
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatalf("decode: %v", err)
		}
	} else {
		var buf strings.Builder
		if _, err := fmt.Fprint(&buf, ""); err != nil {
			t.Fatal(err)
		}
		var payload struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&payload)
		raw = payload.Error
	}
	return resp.StatusCode, out, raw
}

// TestCreateMatchWithoutSeatsIsStillOneVsOne is the compatibility statement:
// every existing client sends no seats field, and must keep getting exactly
// the match it always got.
func TestCreateMatchWithoutSeatsIsStillOneVsOne(t *testing.T) {
	api := NewAPI()
	defer api.Stop()
	srv := httptest.NewServer(api.Routes())
	defer srv.Close()

	code, out, msg := postCreate(t, srv, `{"language":"en"}`)
	if code != http.StatusCreated {
		t.Fatalf("create = %d %s", code, msg)
	}
	if out.Seats != 2 {
		t.Fatalf("default create returned seats=%d, want 2", out.Seats)
	}
	if len(out.Tokens) != 2 || len(out.UserIDs) != 2 {
		t.Fatalf("default create returned %d tokens / %d user ids, want 2 of each",
			len(out.Tokens), len(out.UserIDs))
	}
	if out.Tokens[0] == out.Tokens[1] {
		t.Fatal("both seats were issued the same token")
	}
}

// TestCreateMatchSeatsIsGatedByDeploymentDefault pins the safe default: the
// roster path is implemented and tested, but an unauthenticated caller cannot
// spin up 60 WebSocket seats on a stock deployment. The error has to say which
// of the two limits was hit, because one is a policy an operator can change
// and the other is a property of the game.
func TestCreateMatchSeatsIsGatedByDeploymentDefault(t *testing.T) {
	api := NewAPI()
	defer api.Stop()
	srv := httptest.NewServer(api.Routes())
	defer srv.Close()

	if api.maxSeats != match.MinSeats {
		t.Fatalf("default maxSeats = %d, want %d (1v1)", api.maxSeats, match.MinSeats)
	}

	for _, seats := range []int{3, 8, 60} {
		code, _, msg := postCreate(t, srv, fmt.Sprintf(`{"language":"en","seats":%d}`, seats))
		if code != http.StatusBadRequest {
			t.Fatalf("seats=%d on a stock deployment returned %d, want 400", seats, code)
		}
		if !strings.Contains(msg, "WORDARENA_MAX_SEATS") {
			t.Fatalf("seats=%d was refused with %q, which does not name the deployment limit", seats, msg)
		}
	}
}

// TestCreateMatchSeatsBeyondTheGameAreRefusedSeparately keeps the two bounds
// distinct: a roster the game cannot represent is a client error regardless of
// what the deployment offers.
func TestCreateMatchSeatsBeyondTheGameAreRefusedSeparately(t *testing.T) {
	api := NewAPI()
	defer api.Stop()
	api.maxSeats = match.MaxSeats
	srv := httptest.NewServer(api.Routes())
	defer srv.Close()

	for _, seats := range []int{-1, 1, match.MaxSeats + 1, 1000} {
		code, _, msg := postCreate(t, srv, fmt.Sprintf(`{"language":"en","seats":%d}`, seats))
		if code != http.StatusBadRequest {
			t.Fatalf("seats=%d returned %d, want 400", seats, code)
		}
		if strings.Contains(msg, "WORDARENA_MAX_SEATS") {
			t.Fatalf("seats=%d is outside the game's own range but was refused as a deployment limit (%q)",
				seats, msg)
		}
	}
}

// TestCreateMatchRosterIsReal proves the HTTP path reaches the roster-shaped
// provisioning rather than only counting tokens: every seat gets its own
// credential, every credential opens a socket, and the room really hosts the
// requested number of players on a board sized for them.
func TestCreateMatchRosterIsReal(t *testing.T) {
	const seats = 8
	api := NewAPI()
	defer api.Stop()
	api.maxSeats = seats
	srv := httptest.NewServer(api.Routes())
	defer srv.Close()

	seed := uint64(e2eSeed)
	code, out, msg := postCreate(t, srv, fmt.Sprintf(`{"language":"en","seed":%d,"seats":%d}`, seed, seats))
	if code != http.StatusCreated {
		t.Fatalf("create = %d %s", code, msg)
	}

	if out.Seats != seats || len(out.Tokens) != seats || len(out.UserIDs) != seats {
		t.Fatalf("create reported seats=%d with %d tokens / %d user ids, want %d of each",
			out.Seats, len(out.Tokens), len(out.UserIDs), seats)
	}
	seenT := map[string]bool{}
	seenU := map[uint64]bool{}
	for i := range out.Tokens {
		if out.Tokens[i] == "" {
			t.Fatalf("seat %d has an empty token", i)
		}
		if seenT[out.Tokens[i]] {
			t.Fatalf("token for seat %d is a duplicate", i)
		}
		seenT[out.Tokens[i]] = true
		if out.UserIDs[i] == 0 {
			t.Fatalf("seat %d has a zero user id", i)
		}
		if seenU[out.UserIDs[i]] {
			t.Fatalf("user id for seat %d is a duplicate", i)
		}
		seenU[out.UserIDs[i]] = true
	}

	// The last seat is the interesting one: a two-seat implementation that
	// grew a loop would still fail here.
	c := dial(t, srv, out.MatchID, out.Tokens[seats-1])
	defer c.close()
	snap := c.readSnapshot(t)
	if got := len(snap.Players); got != seats {
		t.Fatalf("seat %d sees %d players, want %d", seats-1, got, seats)
	}
	if got, want := len(snap.Cells), match.BoardCells(seats); got != want {
		t.Fatalf("seat %d sees %d cells on a %d-seat board, want %d", seats-1, got, seats, want)
	}
	if snap.Players[seats-1].UserId != out.UserIDs[seats-1] {
		t.Fatalf("scoreboard row %d carries user %d, want %d",
			seats-1, snap.Players[seats-1].UserId, out.UserIDs[seats-1])
	}
}

// TestCreateMatchRosterAtTheGameMaximum is the boundary of the whole feature:
// the largest roster the game defines is creatable when the deployment allows
// it, so nothing between 2 and 60 is silently unreachable.
func TestCreateMatchRosterAtTheGameMaximum(t *testing.T) {
	api := NewAPI()
	defer api.Stop()
	api.maxSeats = match.MaxSeats
	srv := httptest.NewServer(api.Routes())
	defer srv.Close()

	code, out, msg := postCreate(t, srv, fmt.Sprintf(`{"language":"en","seats":%d}`, match.MaxSeats))
	if code != http.StatusCreated {
		t.Fatalf("create %d seats = %d %s", match.MaxSeats, code, msg)
	}
	if out.Seats != match.MaxSeats || len(out.Tokens) != match.MaxSeats {
		t.Fatalf("create reported seats=%d with %d tokens, want %d", out.Seats, len(out.Tokens), match.MaxSeats)
	}

	c := dial(t, srv, out.MatchID, out.Tokens[0])
	defer c.close()
	snap := c.readSnapshot(t)
	if got := len(snap.Players); got != match.MaxSeats {
		t.Fatalf("a %d-seat match reports %d players", match.MaxSeats, got)
	}
}

// TestCreateMatchSeatsWithPlayerIDsIsRefused says no rather than half-yes:
// profile binding is positional and 1v1-only in this release, and a client
// that asked for both would otherwise get a match silently missing half its
// request.
func TestCreateMatchSeatsWithPlayerIDsIsRefused(t *testing.T) {
	api := NewAPI()
	defer api.Stop()
	api.maxSeats = 8
	srv := httptest.NewServer(api.Routes())
	defer srv.Close()

	code, _, msg := postCreate(t, srv, `{"language":"en","seats":8,"player_ids":[1,2]}`)
	if code != http.StatusBadRequest {
		t.Fatalf("seats with player_ids returned %d, want 400", code)
	}
	if !strings.Contains(msg, "player_ids") {
		t.Fatalf("refusal %q does not mention player_ids", msg)
	}
}
