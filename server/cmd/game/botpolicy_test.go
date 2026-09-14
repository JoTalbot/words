package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/JoTalbot/words/server/internal/match"
	"github.com/JoTalbot/words/server/internal/matchroom"
)

// Q5 tests: simulated players must not silently impersonate humans.
//
// The policy (docs/M2-BOT-POLICY.md, register Q5, decided 2026-09-14) has three
// enforceable clauses, and each one is a test below rather than a paragraph:
//
//  1. a bot is DECLARED, never inferred - an absent profile does not make a
//     seat a bot, and nothing in the server guesses;
//  2. the declaration is DISCLOSED on every surface a client can observe, so a
//     bot cannot pass for a human anywhere;
//  3. a match that involved a bot is recorded as rating- and reward-ineligible
//     in the authoritative result.
//
// The negative cases matter as much as the positive ones: a disclosure that can
// be switched off by a caller is not a disclosure, and a server that "helpfully"
// detects bots it was not told about is exactly the failure Q5 names.

// botAPI returns an API whose deployment permits declared bot seats, plus its
// test server.
func botAPI(t *testing.T) (*API, *httptest.Server) {
	t.Helper()
	api := NewAPI()
	api.allowBotSeats = true
	// These tests need rosters larger than 1v1 as well, so the deployment cap
	// is opened to the game's own maximum; the cap itself is tested in
	// seats_test.go and is not what this batch is about.
	api.maxSeats = match.MaxSeats
	t.Cleanup(api.Stop)
	srv := httptest.NewServer(api.Routes())
	t.Cleanup(srv.Close)
	return api, srv
}

// TestHumanMatchDisclosesNoBots is the compatibility statement: a match nobody
// declared a bot in is byte-identically bot-free, and stays rating eligible.
func TestHumanMatchDisclosesNoBots(t *testing.T) {
	api, srv := botAPI(t)
	seed := uint64(e2eSeed)
	id, tokens, _ := createMatch(t, srv, "en", &seed)

	c := dial(t, srv, id, tokens[0])
	defer c.close()
	snap := c.readSnapshot(t)
	for _, p := range snap.Players {
		if p.GetIsBot() {
			t.Fatalf("an undeclared seat (user %d) was disclosed as a bot", p.UserId)
		}
	}

	room := mustRoom(t, api, id)
	if room.HasBot() || len(room.BotSeats()) != 0 {
		t.Fatalf("a human match reports bots: %v", room.BotSeats())
	}
	api.recordResult(room)
	res := mustResult(t, api, id)
	if res.BotPresent || len(res.Bots) != 0 {
		t.Fatalf("human result discloses bots: bots=%v present=%v", res.Bots, res.BotPresent)
	}
	if !res.RatingEligible {
		t.Fatal("a match with no bots was recorded as rating-ineligible")
	}
}

// TestDeclaredBotIsVisibleToEverySeat is clause 2. Every observer - including
// the bot's opponent and the bot's own seat - must see the disclosure, and the
// debug/tooling HTTP view must agree with the live stream.
func TestDeclaredBotIsVisibleToEverySeat(t *testing.T) {
	api, srv := botAPI(t)
	const seats = 4
	seed := uint64(e2eSeed)

	body := fmt.Sprintf(`{"language":"en","seed":%d,"seats":%d,"bot_seats":[1,3]}`, seed, seats)
	code, out, msg := postCreate(t, srv, body)
	if code != http.StatusCreated {
		t.Fatalf("create = %d %s", code, msg)
	}
	if len(out.Tokens) != seats {
		t.Fatalf("create returned %d tokens, want %d", len(out.Tokens), seats)
	}

	// Seat 2 is a human seat; it must be told that seats 1 and 3 are bots.
	c := dial(t, srv, out.MatchID, out.Tokens[2])
	defer c.close()
	snap := c.readSnapshot(t)

	got := map[uint64]bool{}
	for _, p := range snap.Players {
		got[p.UserId] = p.GetIsBot()
	}
	if len(got) != seats {
		t.Fatalf("snapshot carries %d players, want %d", len(got), seats)
	}
	for i, userID := range out.UserIDs {
		want := i == 1 || i == 3
		if got[userID] != want {
			t.Fatalf("seat %d (user %d) disclosed is_bot=%v, want %v", i, userID, got[userID], want)
		}
	}

	// The HTTP state view is a second observable surface; a disclosure only
	// one surface carries is a disclosure a client can bypass.
	code, snapBody, _ := getRaw(t, srv, fmt.Sprintf("/v1/match/%d/snapshot", out.MatchID), map[string]string{
		"Authorization": "Bearer " + out.Tokens[2],
	})
	if code != http.StatusOK {
		t.Fatalf("snapshot view = %d", code)
	}
	var view struct {
		Players []struct {
			UserID uint64 `json:"user_id"`
			IsBot  bool   `json:"is_bot"`
		} `json:"players"`
	}
	if err := json.Unmarshal([]byte(snapBody), &view); err != nil {
		t.Fatalf("decode snapshot view: %v (%s)", err, snapBody)
	}
	if len(view.Players) != seats {
		t.Fatalf("HTTP view carries %d players, want %d", len(view.Players), seats)
	}
	for i := range out.UserIDs {
		want := i == 1 || i == 3
		if view.Players[i].IsBot != want {
			t.Fatalf("HTTP view disagrees on seat %d: is_bot=%v, want %v",
				i, view.Players[i].IsBot, want)
		}
	}

	// And the declaration reaches the room's own state, not only the encoder.
	room := mustRoom(t, api, out.MatchID)
	if gotSeats := room.BotSeats(); len(gotSeats) != 2 || gotSeats[0] != 1 || gotSeats[1] != 3 {
		t.Fatalf("room bot seats = %v, want [1 3]", gotSeats)
	}
}

// TestBotMatchIsNotRatingEligible is clause 3: the disqualifying fact is in the
// authoritative result, which is where a future rating or reward job reads it.
func TestBotMatchIsNotRatingEligible(t *testing.T) {
	api, srv := botAPI(t)
	seed := uint64(e2eSeed)
	body := fmt.Sprintf(`{"language":"en","seed":%d,"bot_seats":[0]}`, seed)
	code, out, msg := postCreate(t, srv, body)
	if code != http.StatusCreated {
		t.Fatalf("create = %d %s", code, msg)
	}

	room := mustRoom(t, api, out.MatchID)
	if !room.HasBot() {
		t.Fatal("the room does not report the declared bot")
	}
	api.recordResult(room)
	res := mustResult(t, api, out.MatchID)

	if !res.BotPresent {
		t.Fatal("result does not flag a bot match")
	}
	if len(res.Bots) != 1 || res.Bots[0] != 0 {
		t.Fatalf("result bots = %v, want [0]", res.Bots)
	}
	if res.RatingEligible {
		t.Fatal("a match containing a bot was recorded as rating-eligible")
	}
}

// TestBotDeclarationIsRefusedByDefault is clause 1's teeth: on a stock
// deployment nobody can assert bot-ness at all, so the capability cannot be
// used to mislabel a human on a system that never expected it.
func TestBotDeclarationIsRefusedByDefault(t *testing.T) {
	api := NewAPI()
	defer api.Stop()
	srv := httptest.NewServer(api.Routes())
	defer srv.Close()

	if api.allowBotSeats {
		t.Fatal("a stock deployment permits bot declarations by default")
	}
	code, _, msg := postCreate(t, srv, `{"language":"en","bot_seats":[0]}`)
	if code != http.StatusBadRequest {
		t.Fatalf("stock deployment accepted a bot declaration: %d", code)
	}
	if !strings.Contains(msg, "WORDARENA_ALLOW_BOT_SEATS") {
		t.Fatalf("refusal %q does not name the deployment switch", msg)
	}
}

func TestBotDeclarationIsValidated(t *testing.T) {
	_, srv := botAPI(t)
	for _, tc := range []struct {
		name string
		body string
	}{
		{"outside a 1v1 roster", `{"language":"en","bot_seats":[2]}`},
		{"negative seat", `{"language":"en","bot_seats":[-1]}`},
		{"duplicate seat", `{"language":"en","bot_seats":[0,0]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if code, _, msg := postCreate(t, srv, tc.body); code != http.StatusBadRequest {
				t.Fatalf("accepted %s: %d (%s)", tc.name, code, msg)
			}
		})
	}
}

// TestBotnessIsNeverInferred is the clause that has no positive test by design.
// Anonymous seats, profile-bound seats and tooling seats are all humans as far
// as disclosure is concerned; only an explicit declaration makes a bot.
func TestBotnessIsNeverInferred(t *testing.T) {
	api, srv := botAPI(t)
	seed := uint64(e2eSeed)

	code, out, msg := postCreate(t, srv, fmt.Sprintf(`{"language":"en","seed":%d,"seats":6}`, seed))
	if code != http.StatusCreated {
		t.Fatalf("create = %d %s", code, msg)
	}
	room := mustRoom(t, api, out.MatchID)
	if room.HasBot() {
		t.Fatalf("six anonymous seats were treated as bots: %v", room.BotSeats())
	}
	c := dial(t, srv, out.MatchID, out.Tokens[5])
	defer c.close()
	for _, p := range c.readSnapshot(t).Players {
		if p.GetIsBot() {
			t.Fatalf("anonymous seat user=%d was disclosed as a bot", p.UserId)
		}
	}
}

// TestQueueBotMustDeclareItself covers the live masquerade path: the headless
// bot and the device-smoke partner join through the queue, so a bot that does
// not declare itself there would be indistinguishable from a human in the one
// place it actually happens today.
func TestQueueBotMustDeclareItself(t *testing.T) {
	api, srv := botAPI(t)

	// A declared bot and a human pair up.
	human := queueJoin(t, srv, `{"language":"en"}`)
	bot := queueJoin(t, srv, `{"language":"en","bot":true}`)
	if bot.IsBot != true {
		t.Fatalf("the queue entry does not echo its own declaration: %+v", bot)
	}
	if human.IsBot {
		t.Fatal("a human queue entry was marked as a bot")
	}

	matched := waitMatched(t, srv, bot.QueueID)
	if matched.MatchID == 0 {
		t.Fatal("the bot queue entry never paired")
	}
	room := mustRoom(t, api, matched.MatchID)
	bots := room.BotSeats()
	if len(bots) != 1 {
		t.Fatalf("room bot seats = %v, want exactly the declared bot", bots)
	}

	// The human in that match is told.
	hm := waitMatched(t, srv, human.QueueID)
	ch := dial(t, srv, hm.MatchID, hm.Token)
	defer ch.close()
	sawBot := false
	for _, p := range ch.readSnapshot(t).Players {
		if p.GetIsBot() {
			sawBot = true
		}
	}
	if !sawBot {
		t.Fatal("the human player is not told there is a bot in the match")
	}

	// A bot may not bind a human's profile, and the deployment switch gates the
	// whole capability.
	if code, _ := postRaw(t, srv, "/v1/queue", `{"language":"en","bot":true,"player_id":1}`, nil); code != http.StatusBadRequest {
		t.Fatalf("a bot queue entry bound a player_id: %d", code)
	}

	strict := NewAPI()
	defer strict.Stop()
	strictSrv := httptest.NewServer(strict.Routes())
	defer strictSrv.Close()
	if code, _ := postRaw(t, strictSrv, "/v1/queue", `{"language":"en","bot":true}`, nil); code != http.StatusBadRequest {
		t.Fatalf("a stock deployment accepted a bot queue entry: %d", code)
	}
}

// TestMatchmakerNeverInventsBots is the rule the roadmap row was really about:
// an under-filled lobby starts short-handed (batch 31C) rather than being
// topped up with opponents nobody asked for.
func TestMatchmakerNeverInventsBots(t *testing.T) {
	api, srv := botAPI(t)

	a := queueJoin(t, srv, `{"language":"en"}`)
	b := queueJoin(t, srv, `{"language":"en"}`)
	_ = a
	ma := waitMatched(t, srv, a.QueueID)
	mb := waitMatched(t, srv, b.QueueID)
	if ma.MatchID == 0 || ma.MatchID != mb.MatchID {
		t.Fatalf("two humans did not pair into one match: %d / %d", ma.MatchID, mb.MatchID)
	}
	room := mustRoom(t, api, ma.MatchID)
	if room.HasBot() {
		t.Fatalf("the matchmaker filled a human lobby with bots: %v", room.BotSeats())
	}
}

// TestUnknownSeatUserIDIsNotABot keeps the disclosure tied to the declaration
// rather than to a missing row: seating a user id outside the roster is a
// defensive path, not a bot.
func TestBotConfigLongerThanRosterIsIgnoredNotFatal(t *testing.T) {
	ids := make([]uint64, 2)
	for i := range ids {
		ids[i] = uint64(i + 1)
	}
	room, err := matchroom.New(matchroom.Config{
		MatchID: 1, Seed: 1, Language: "en", SeatUserIDs: ids,
		BotSeats: []bool{true, false, true},
	})
	if err != nil {
		t.Fatalf("an over-long bot declaration was fatal: %v", err)
	}
	if got := room.BotSeats(); len(got) != 1 || got[0] != 0 {
		t.Fatalf("bot seats = %v, want [0] (the declaration past the roster is ignored)", got)
	}
}

// mustRoom fetches a live room, failing the test when it is gone.
func mustRoom(t *testing.T, api *API, id uint64) *matchroom.Room {
	t.Helper()
	api.mu.Lock()
	room, ok := api.rooms[id]
	api.mu.Unlock()
	if !ok {
		t.Fatalf("room %d not found", id)
	}
	return room
}

// queueView is the subset of a queue entry a test needs.
type queueView struct {
	QueueID string `json:"queue_id"`
	IsBot   bool   `json:"is_bot"`
	Status  string `json:"status"`
	MatchID uint64 `json:"match_id"`
	Token   string `json:"token"`
}

// queueJoin enqueues one player and returns their entry.
func queueJoin(t *testing.T, srv *httptest.Server, body string) queueView {
	t.Helper()
	code, out := postRaw(t, srv, "/v1/queue", body, nil)
	if code != http.StatusAccepted {
		t.Fatalf("queue join = %d %s", code, out)
	}
	var v queueView
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("decode queue entry: %v (%s)", err, out)
	}
	if v.QueueID == "" {
		t.Fatalf("queue entry has no id: %s", out)
	}
	return v
}

// waitMatched polls a queue entry until it is paired. Pairing is synchronous on
// enqueue, so this normally succeeds on the first poll; the loop exists only so
// a slower CI box cannot make the test flaky.
func waitMatched(t *testing.T, srv *httptest.Server, queueID string) queueView {
	t.Helper()
	for i := 0; i < 50; i++ {
		code, out, _ := getRaw(t, srv, "/v1/queue/"+queueID, nil)
		if code == http.StatusOK {
			var v queueView
			if err := json.Unmarshal([]byte(out), &v); err != nil {
				t.Fatalf("decode poll: %v (%s)", err, out)
			}
			if v.Status == "matched" {
				return v
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("queue entry %s never matched", queueID)
	return queueView{}
}

// mustResult reads a recorded result from the in-memory cache.
func mustResult(t *testing.T, api *API, id uint64) matchResult {
	t.Helper()
	res, ok := api.lookupResult(id)
	if !ok {
		t.Fatalf("no result recorded for match %d", id)
	}
	return res
}
