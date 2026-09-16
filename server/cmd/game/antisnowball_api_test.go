package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/JoTalbot/words/server/internal/match"
	"github.com/JoTalbot/words/server/internal/matchroom"
	"github.com/JoTalbot/words/server/internal/pve"
)

// M2 batch 35A (rebuild of 34B): the catch-up rule is opt-in per match and
// reachable from the HTTP surface (docs/M2-ANTI-SNOWBALL.md). Off by default,
// so no M0/M1 baseline, replay or device assertion moves; on, the rule runs
// with the shipped 25/2/15 constants.

// antiSnowballPolicy is the driver the tests use to move a room. It is the
// pve opponent in its aggressive shape: long enough words and a short enough
// interval that a score gap actually opens inside one match, which is the
// precondition for the rule to fire at all. It is a pure function of
// (snapshot, tick, dictionary, policy), so the driven match is deterministic
// for a fixed seed (PD-003).
func antiSnowballPolicy() pve.Policy {
	return pve.Policy{MinLen: match.MinWordLength, MaxLen: 6, Interval: 15, AllowSteal: true}
}

// createAntiSnowball provisions a room with the catch-up rule on and returns
// its id, the seat tokens and the raw create response.
func createAntiSnowball(t *testing.T, srv *httptest.Server, seed int) (uint64, []string, string) {
	t.Helper()
	code, body := postRaw(t, srv, "/v1/matches",
		fmt.Sprintf(`{"language":"en","seed":%d,"anti_snowball":true}`, seed), nil)
	if code != http.StatusCreated {
		t.Fatalf("create with anti_snowball: status %d body %s", code, body)
	}
	var resp struct {
		MatchID uint64   `json:"match_id"`
		Tokens  []string `json:"tokens"`
	}
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("decode create response: %v (%s)", err, body)
	}
	return resp.MatchID, resp.Tokens, body
}

// snapshotView fetches the seat-authorized state view of a live match.
func snapshotView(t *testing.T, srv *httptest.Server, id uint64, token string) map[string]any {
	t.Helper()
	req, err := http.NewRequest("GET", fmt.Sprintf("%s/v1/match/%d/snapshot", srv.URL, id), nil)
	if err != nil {
		t.Fatalf("build snapshot request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("snapshot request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("snapshot view: status %d", resp.StatusCode)
	}
	var view map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&view); err != nil {
		t.Fatalf("decode snapshot view: %v", err)
	}
	return view
}

// driveSeed plays a full fast-forwarded match for seed 101, both seats on
// the aggressive policy in a rotating one-seat-per-tick order (the same
// non-degenerate driver the calibration harness uses), and returns the final
// scores, the accepted-event log, and the bonus totals.
//
// The room is built directly with matchroom.New - the exact config the HTTP
// surface provisions for the flag - rather than a room the API created: the
// API starts a per-room real-time ticker that is the room's only Tick writer,
// and a test fast-forwarding the same room races it (the race detector
// proved it on 2026-09-16). The surface link is asserted separately: the
// flag is accepted, echoed, and set on the provisioned room.
func driveSeed(t *testing.T, matchID uint64, withRule bool) (scores [2]int64, events []match.Event, bonusEvents int, bonusTotal int64) {
	t.Helper()
	scores = [2]int64{}
	rm, err := matchroom.New(matchroom.Config{
		MatchID:      matchID,
		Seed:         101,
		Language:     "en",
		SeatUserIDs:  []uint64{1, 2},
		AntiSnowball: withRule,
	})
	if err != nil {
		t.Fatalf("room: %v", err)
	}
	ops := [2]*pve.Opponent{}
	for seat := 0; seat < 2; seat++ {
		ops[seat], err = pve.New("en", antiSnowballPolicy())
		if err != nil {
			t.Fatalf("opponent: %v", err)
		}
	}
	for i := 0; i < 4*60*60 && !rm.IsOver(); i++ {
		m := rm.Match()
		seat := match.Seat(i % 2)
		if path := ops[seat].Intent(rm.Snapshot(), m.Tick(), seat); path != nil {
			if _, err := rm.Submit(seat, path); err != nil {
				t.Fatalf("submit: %v", err)
			}
		}
		rm.Tick()
	}
	if !rm.IsOver() {
		t.Fatal("the driven match never finished")
	}
	events = rm.Match().Events()
	for _, ev := range events {
		if ev.Result == match.ResultAccepted && ev.CatchUpBonus > 0 {
			bonusEvents++
			bonusTotal += ev.CatchUpBonus
		}
	}
	scores = [2]int64{rm.Match().Score(0), rm.Match().Score(1)}
	return scores, events, bonusEvents, bonusTotal
}

// TestAntiSnowballOffByDefault: the M0/M1 contract is untouched when the flag
// is absent. The response carries the default, the live state view shows the
// rule off, and the room runs without it.
func TestAntiSnowballOffByDefault(t *testing.T) {
	api, srv := botAPI(t)
	code, body := postRaw(t, srv, "/v1/matches", `{"language":"en","seed":1}`, nil)
	if code != http.StatusCreated {
		t.Fatalf("create: status %d body %s", code, body)
	}
	if !strings.Contains(body, `"anti_snowball":false`) {
		t.Fatalf("the create response must carry the rule's default, got: %s", body)
	}
	var resp struct {
		MatchID uint64   `json:"match_id"`
		Tokens  []string `json:"tokens"`
	}
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	room := mustRoom(t, api, resp.MatchID)
	if room.Match().AntiSnowball() {
		t.Fatal("a room created without the flag runs the catch-up rule")
	}
	view := snapshotView(t, srv, resp.MatchID, resp.Tokens[0])
	if v, _ := view["anti_snowball"].(bool); v {
		t.Fatal("the state view shows the catch-up rule on for a default match")
	}
}

// TestAntiSnowballAcceptedOnCreate: the flag is a per-match option, like
// sudden_death - accepted for any roster, echoed by the response, visible in
// the live state view, and actually set on the room.
func TestAntiSnowballAcceptedOnCreate(t *testing.T) {
	api, srv := botAPI(t)
	for _, seats := range []int{2, 8} {
		body := fmt.Sprintf(`{"language":"en","seed":7,"seats":%d,"anti_snowball":true}`, seats)
		code, raw := postRaw(t, srv, "/v1/matches", body, nil)
		if code != http.StatusCreated {
			t.Fatalf("create seats=%d with anti_snowball: status %d body %s", seats, code, raw)
		}
		if !strings.Contains(raw, `"anti_snowball":true`) {
			t.Fatalf("the create response must echo the flag, got: %s", raw)
		}
		var resp struct {
			MatchID uint64   `json:"match_id"`
			Tokens  []string `json:"tokens"`
		}
		if err := json.Unmarshal([]byte(raw), &resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		room := mustRoom(t, api, resp.MatchID)
		if !room.Match().AntiSnowball() {
			t.Fatalf("seats=%d: the room does not have the catch-up rule it was created with", seats)
		}
		view := snapshotView(t, srv, resp.MatchID, resp.Tokens[0])
		if v, _ := view["anti_snowball"].(bool); !v {
			t.Fatalf("seats=%d: the state view does not show the rule on", seats)
		}
	}
}

// TestAntiSnowballAwardsBonusThroughProvisionedRoom: the flag must change
// what a provisioned room actually scores. The surface link (accepted,
// echoed, set on the room) is asserted over HTTP; the scoring link is
// asserted by driving the same config once with the rule on and once off
// over the same seed: with the rule on some accepted word must carry a
// catch-up bonus, and the final scores must differ from the rule-off run.
func TestAntiSnowballAwardsBonusThroughProvisionedRoom(t *testing.T) {
	api, srv := botAPI(t)
	code, raw := postRaw(t, srv, "/v1/matches", `{"language":"en","seed":101,"anti_snowball":true}`, nil)
	if code != http.StatusCreated {
		t.Fatalf("create: status %d body %s", code, raw)
	}
	if !strings.Contains(raw, `"anti_snowball":true`) {
		t.Fatalf("the create response must echo the flag, got: %s", raw)
	}
	var resp struct {
		MatchID uint64 `json:"match_id"`
	}
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if r := mustRoom(t, api, resp.MatchID); !r.Match().AntiSnowball() {
		t.Fatal("the provisioned room does not have the rule its creation set")
	}

	scoresOn, _, bonusEvents, bonusTotal := driveSeed(t, 1, true)
	if bonusEvents == 0 {
		t.Fatal("the rule was on for the whole match and no catch-up bonus was ever awarded")
	}
	if bonusTotal <= 0 {
		t.Fatalf("bonuses were awarded but add to %d points", bonusTotal)
	}
	scoresOff, _, eventsOffBonus, _ := driveSeed(t, 2, false)
	if eventsOffBonus != 0 {
		t.Fatalf("the rule was off but %d bonuses were awarded", eventsOffBonus)
	}
	if scoresOn == scoresOff {
		t.Fatalf("the flag changed nothing: both runs finished %v", scoresOn)
	}
	t.Logf("seed 101: rule-off %v, rule-on %v (%d bonus events, +%d points)",
		scoresOff, scoresOn, bonusEvents, bonusTotal)
}

// TestAntiSnowballBonusesAreCappedAtTheShippedConstants: through the public
// surface, with only the flag set, every bonus must obey the 25/2/15 set -
// in particular the per-word cap - because that set is what the HTTP surface
// ships when a caller does not (and cannot) tune the constants.
func TestAntiSnowballBonusesAreCappedAtTheShippedConstants(t *testing.T) {
	api, srv := botAPI(t)
	id, _, _ := createAntiSnowball(t, srv, 101)
	if r := mustRoom(t, api, id); !r.Match().AntiSnowball() {
		t.Fatal("the provisioned room does not have the rule its creation set")
	}

	_, events, bonusEvents, _ := driveSeed(t, 1, true)
	if bonusEvents == 0 {
		t.Fatal("no bonus was observed; the test needs a seed where the rule fires")
	}
	for _, ev := range events {
		if ev.Result != match.ResultAccepted || ev.CatchUpBonus == 0 {
			continue
		}
		if ev.CatchUpBonus > match.CatchUpMaxBonus {
			t.Fatalf("bonus %d exceeds the shipped cap %d (word %q)",
				ev.CatchUpBonus, match.CatchUpMaxBonus, ev.Word)
		}
	}
}
