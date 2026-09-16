package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/JoTalbot/words/server/internal/match"
	"github.com/JoTalbot/words/server/internal/pve"
)

// M2 batch 35A (rebuild of 34B): the catch-up rule is opt-in per match and
// reachable from the HTTP surface (docs/M2-ANTI-SNOWBALL.md). Off by default,
// so no M0/M1 baseline, replay or device assertion moves; on, the rule runs
// with the shipped 25/2/15 constants.

// antiSnowballPolicy is the driver the tests use to move a provisioned room.
// It is the pve opponent in its aggressive shape: long enough words and a
// short enough interval that a score gap actually opens inside one match,
// which is the precondition for the rule to fire at all. It is a pure
// function of (snapshot, tick, dictionary, policy), so the driven match is
// deterministic for a fixed seed (PD-003).
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
// what the provisioned room actually scores. Both seats are driven by the
// same deterministic policy over the same seed, once with the rule on and
// once with it off: with the rule on some accepted word must carry a catch-up
// bonus, and the final scores must differ from the rule-off run.
func TestAntiSnowballAwardsBonusThroughProvisionedRoom(t *testing.T) {
	api, srv := botAPI(t)

	run := func(withRule bool) (id uint64, tokens []string) {
		body := fmt.Sprintf(`{"language":"en","seed":4131,"anti_snowball":%v}`, withRule)
		code, raw := postRaw(t, srv, "/v1/matches", body, nil)
		if code != http.StatusCreated {
			t.Fatalf("create (rule=%v): status %d body %s", withRule, code, raw)
		}
		var resp struct {
			MatchID uint64   `json:"match_id"`
			Tokens  []string `json:"tokens"`
		}
		if err := json.Unmarshal([]byte(raw), &resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return resp.MatchID, resp.Tokens
	}

	drive := func(id uint64) (events []match.Event, scores [2]int64, bonusEvents int, bonusTotal int64) {
		room := mustRoom(t, api, id)
		ops := [2]*pve.Opponent{}
		for seat := 0; seat < 2; seat++ {
			opp, err := pve.New("en", antiSnowballPolicy())
			if err != nil {
				t.Fatalf("opponent: %v", err)
			}
			ops[seat] = opp
		}
		for i := 0; i < 4*60*60 && !room.IsOver(); i++ {
			m := room.Match()
			snap := m.Snapshot()
			tick := m.Tick()
			for seat := match.Seat(0); int(seat) < 2; seat++ {
				if m.IsEliminated(seat) {
					continue
				}
				if path := ops[seat].Intent(snap, tick, seat); path != nil {
					if _, err := room.Submit(seat, path); err != nil {
						t.Fatalf("submit: %v", err)
					}
				}
			}
			room.Tick()
		}
		if !room.IsOver() {
			t.Fatal("the driven match never finished")
		}
		events = room.Match().Events()
		for _, ev := range events {
			if ev.Result == match.ResultAccepted && ev.CatchUpBonus > 0 {
				bonusEvents++
				bonusTotal += ev.CatchUpBonus
			}
		}
		scores = [2]int64{room.Match().Score(0), room.Match().Score(1)}
		return events, scores, bonusEvents, bonusTotal
	}

	idOn, _ := run(true)
	_, scoresOn, bonusEvents, bonusTotal := drive(idOn)
	if bonusEvents == 0 {
		t.Fatal("the rule was on for the whole match and no catch-up bonus was ever awarded")
	}
	if bonusTotal <= 0 {
		t.Fatalf("bonuses were awarded but add to %d points", bonusTotal)
	}
	idOff, _ := run(false)
	_, scoresOff, eventsOffBonus, _ := drive(idOff)
	if eventsOffBonus != 0 {
		t.Fatalf("the rule was off but %d bonuses were awarded", eventsOffBonus)
	}
	if scoresOn == scoresOff {
		t.Fatalf("the flag changed nothing: both runs finished %v", scoresOn)
	}
	t.Logf("seed 4131: rule-off %v, rule-on %v (%d bonus events, +%d points)",
		scoresOff, scoresOn, bonusEvents, bonusTotal)
}

// TestAntiSnowballBonusesAreCappedAtTheShippedConstants: through the public
// surface, with only the flag set, every bonus must obey the 25/2/15 set -
// in particular the per-word cap - because that set is what the HTTP surface
// ships when a caller does not (and cannot) tune the constants.
func TestAntiSnowballBonusesAreCappedAtTheShippedConstants(t *testing.T) {
	api, srv := botAPI(t)
	id, tokens, _ := createAntiSnowball(t, srv, 4131)

	room := mustRoom(t, api, id)
	ops := [2]*pve.Opponent{}
	for seat := 0; seat < 2; seat++ {
		opp, err := pve.New("en", antiSnowballPolicy())
		if err != nil {
			t.Fatalf("opponent: %v", err)
		}
		ops[seat] = opp
	}
	for i := 0; i < 4*60*60 && !room.IsOver(); i++ {
		m := room.Match()
		snap := m.Snapshot()
		tick := m.Tick()
		for seat := match.Seat(0); int(seat) < 2; seat++ {
			if m.IsEliminated(seat) {
				continue
			}
			if path := ops[seat].Intent(snap, tick, seat); path != nil {
				if _, err := room.Submit(seat, path); err != nil {
					t.Fatalf("submit: %v", err)
				}
			}
		}
		room.Tick()
	}
	if !room.IsOver() {
		t.Fatal("the driven match never finished")
	}
	sawBonus := false
	for _, ev := range room.Match().Events() {
		if ev.Result != match.ResultAccepted || ev.CatchUpBonus == 0 {
			continue
		}
		sawBonus = true
		if ev.CatchUpBonus > match.CatchUpMaxBonus {
			t.Fatalf("bonus %d exceeds the shipped cap %d (word %q)",
				ev.CatchUpBonus, match.CatchUpMaxBonus, ev.Word)
		}
	}
	if !sawBonus {
		t.Fatal("no bonus was observed; the test needs a seed where the rule fires")
	}
	_ = tokens
}
