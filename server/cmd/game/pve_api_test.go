package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// M2 batch 32E: a practice match must be playable by ONE client, the opponent
// must be visibly a bot on every surface, and the match must never be
// rating-eligible.

// createPvE asks for a practice match and returns its id, tokens and raw body.
func createPvE(t *testing.T, srv *httptest.Server) (uint64, []string, string) {
	t.Helper()
	code, body := postRaw(t, srv, "/v1/matches", `{"language":"en","seed":1512,"pve":true}`, nil)
	if code != http.StatusCreated {
		t.Fatalf("create practice match: status %d body %s", code, body)
	}
	var resp struct {
		MatchID uint64   `json:"match_id"`
		Tokens  []string `json:"tokens"`
		UserIDs []uint64 `json:"user_ids"`
	}
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("decode create response: %v (%s)", err, body)
	}
	if len(resp.UserIDs) != 2 || len(resp.Tokens) != 2 {
		t.Fatalf("a practice match must have exactly two seats, got %d/%d (%s)", len(resp.UserIDs), len(resp.Tokens), body)
	}
	return resp.MatchID, resp.Tokens, body
}

func TestPvEDeclaresAndDisclosesTheOpponent(t *testing.T) {
	api, srv := botAPI(t)
	matchID, tokens, _ := createPvE(t, srv)

	// Declared, not inferred: the room knows which seat is simulated before
	// any client connects, and the durable outcome is fixed at creation time.
	room := mustRoom(t, api, matchID)
	if seats := room.BotSeats(); len(seats) != 1 || seats[0] != 1 {
		t.Fatalf("a practice match should declare exactly seat 1 as simulated, got %v", seats)
	}
	if !room.HasBot() {
		t.Fatal("the room does not know it contains a bot")
	}

	// The player's own client must see the label on the live stream.
	c := dial(t, srv, matchID, tokens[0])
	defer c.close()
	snap := c.readSnapshot(t)
	bots, humans := 0, 0
	var botUser uint64
	for _, p := range snap.Players {
		if p.GetIsBot() {
			bots++
			botUser = p.GetUserId()
		} else {
			humans++
		}
	}
	if bots != 1 || humans != 1 {
		t.Fatalf("a practice match must disclose one bot and one human, got %d/%d (%v)", bots, humans, snap.Players)
	}
	// Seat 1's user id is the one the create response assigned to the
	// opponent, so the label is on the seat that does not belong to the caller.
	for _, p := range snap.Players {
		if p.GetUserId() == botUser && !p.GetIsBot() {
			t.Fatal("the bot label moved off the opponent seat")
		}
	}

}

// TestPvEOpponentActuallyPlays is the point of the feature: with no second
// client and no external tooling, the board must move on its own.
func TestPvEOpponentActuallyPlays(t *testing.T) {
	_, srv := botAPI(t)
	matchID, tokens, _ := createPvE(t, srv)

	// One client, no second player anywhere: the only thing that can move seat
	// 1 is the server's own driver.
	c := dial(t, srv, matchID, tokens[0])
	defer c.close()

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		snap := c.readSnapshot(t)
		for _, p := range snap.Players {
			// The caller's own seat never scores here: nobody is playing it.
			if p.GetIsBot() && p.GetScore() > 0 {
				return
			}
		}
	}
	t.Fatal("the server never drove the practice opponent: a solo player would face a statue")
}

func TestPvEIsRefusedWithoutTheSwitch(t *testing.T) {
	t.Setenv("WORDARENA_ALLOW_BOT_SEATS", "false")
	api := NewAPI()
	t.Cleanup(api.Stop)
	srv := httptest.NewServer(api.Routes())
	t.Cleanup(srv.Close)

	code, body := postRaw(t, srv, "/v1/matches", `{"language":"en","seed":1512,"pve":true}`, nil)
	if code != http.StatusBadRequest {
		t.Fatalf("pve was accepted on a deployment that has it switched off: status %d body %s", code, body)
	}
	if !strings.Contains(body, "WORDARENA_ALLOW_BOT_SEATS") {
		t.Fatalf("the refusal does not name the switch that would enable it: %s", body)
	}
}

func TestPvEIsOneVersusOneOnly(t *testing.T) {
	_, srv := botAPI(t)
	code, body := postRaw(t, srv, "/v1/matches", `{"language":"en","seed":1512,"seats":4,"pve":true}`, nil)
	if code != http.StatusBadRequest {
		t.Fatalf("pve accepted non-1v1 seats: status %d body %s", code, body)
	}
	if !strings.Contains(body, "1v1") {
		t.Fatalf("the refusal does not explain that practice is 1v1 only: %s", body)
	}
}

// TestPvEOpponentIsNotRatingEligible checks the durable record after the match
// finishes through the ordinary persist path.
func TestPvEOpponentIsNotRatingEligible(t *testing.T) {
	api, srv := botAPI(t)
	matchID, _, _ := createPvE(t, srv)

	room := mustRoom(t, api, matchID)
	for i := 0; i < 4*60*60 && !room.IsOver(); i++ {
		room.Match().AdvanceTicks(1)
	}
	if !room.IsOver() {
		t.Fatal("the match never finished")
	}
	api.recordResult(room)

	res := mustResult(t, api, matchID)
	if !res.BotPresent || len(res.Bots) != 1 || res.Bots[0] != 1 {
		t.Fatalf("the practice opponent is missing from the durable result: %+v", res)
	}
	if res.RatingEligible {
		t.Fatalf("a practice match was recorded as rating-eligible: %+v", res)
	}
}
