package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/JoTalbot/words/server/internal/match"
	"github.com/JoTalbot/words/server/internal/matchroom"
	"github.com/JoTalbot/words/server/internal/pve"
)

// M2 batch 35B: the practice opponent has named difficulty presets. The
// default must stay exactly the shipped 32E opponent (no existing practice
// match moves), and a named difficulty must change what the opponent plays -
// a knob that only relabels is not a knob.

// pveCreate provisions a practice match, appending extra (a JSON fragment
// like `,"pve_difficulty":"hard"` or "") to the standard body, and returns
// the match id and the raw response.
func pveCreate(t *testing.T, srv *httptest.Server, extra string) (uint64, string) {
	t.Helper()
	body := fmt.Sprintf(`{"language":"en","seed":1512,"pve":true%s}`, extra)
	code, raw := postRaw(t, srv, "/v1/matches", body, nil)
	if code != http.StatusCreated {
		t.Fatalf("create pve %s: status %d body %s", extra, code, raw)
	}
	var resp struct {
		MatchID uint64 `json:"match_id"`
	}
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		t.Fatalf("decode: %v (%s)", err, raw)
	}
	return resp.MatchID, raw
}

func pvePolicyOf(t *testing.T, api *API, id uint64) pve.Policy {
	t.Helper()
	api.pveMu.Lock()
	opp, ok := api.pve[id]
	api.pveMu.Unlock()
	if !ok {
		t.Fatalf("no pve opponent is attached to match %d", id)
	}
	return opp.opponent.Policy()
}

func TestPvEDifficultyDefaultIsEasy(t *testing.T) {
	api, srv := botAPI(t)
	id, _ := pveCreate(t, srv, "")
	got := pvePolicyOf(t, api, id)
	want := pve.DefaultPolicy()
	if got != want {
		t.Fatalf("the default practice opponent changed: got %+v, want the shipped 32E %+v", got, want)
	}
}

func TestPvEDifficultiesAreAccepted(t *testing.T) {
	api, srv := botAPI(t)
	cases := []struct {
		name string
		want pve.Policy
	}{
		{"easy", pve.DefaultPolicy()},
		{"normal", pve.DifficultyNormal.Policy()},
		{"hard", pve.DifficultyHard.Policy()},
	}
	for _, c := range cases {
		id, _ := pveCreate(t, srv, fmt.Sprintf(`,"pve_difficulty":%q`, c.name))
		got := pvePolicyOf(t, api, id)
		if got != c.want {
			t.Fatalf("difficulty %q: policy %+v, want %+v", c.name, got, c.want)
		}
	}
}

func TestPvEDifficultyRejectsUnknownNames(t *testing.T) {
	_, srv := botAPI(t)
	code, raw := postRaw(t, srv, "/v1/matches",
		`{"language":"en","seed":1512,"pve":true,"pve_difficulty":"nightmare"}`, nil)
	if code != http.StatusBadRequest {
		t.Fatalf("unknown difficulty accepted: status %d body %s", code, raw)
	}
}

func TestPvEDifficultyRequiresPvE(t *testing.T) {
	_, srv := botAPI(t)
	code, raw := postRaw(t, srv, "/v1/matches",
		`{"language":"en","seed":1512,"pve_difficulty":"hard"}`, nil)
	if code != http.StatusBadRequest {
		t.Fatalf("difficulty without pve accepted: status %d body %s", code, raw)
	}
}

// TestPvEDifficultyChangesTheOpponent is the point of the feature: same
// seed, same player, different difficulty - the match must actually come out
// differently, or the preset is a relabel. The opponent here is driven
// directly through Room.Submit (the same door the API's real-time driver
// uses, minus the wall clock): the wiring that ATTACHES a preset policy is
// the job of TestPvEDifficultiesAreAccepted, and this test measures what the
// preset POLICIES do in a match.
func TestPvEDifficultyChangesTheOpponent(t *testing.T) {
	run := func(difficulty pve.Difficulty) (words int, score int64) {
		ids := []uint64{1, 2}
		room, err := matchroom.New(matchroom.Config{MatchID: 1, Seed: 1512, Language: "en", SeatUserIDs: ids})
		if err != nil {
			t.Fatalf("room: %v", err)
		}
		// The player (seat 0) plays the shipped polite policy - the shape a
		// human practice player has. An aggressive player hogs the 12-cell
		// board behind locks and starves a no-steal opponent out of every
		// cell, which would test locks, not difficulty. The opponent (seat
		// 1) plays the preset under test. One seat acts per tick in a
		// rotating order (the same non-degenerate driver the calibration
		// harness uses, see docs/M2-ANTI-SNOWBALL.md).
		player, _ := pve.New("en", pve.DefaultPolicy())
		opponent, err := pve.New("en", difficulty.Policy())
		if err != nil {
			t.Fatalf("opponent: %v", err)
		}
		for i := 0; i < 4*60*60 && !room.IsOver(); i++ {
			m := room.Match()
			seat := match.Seat(i % 2)
			var opp *pve.Opponent
			if seat == 0 {
				opp = player
			} else {
				opp = opponent
			}
			if path := opp.Intent(m.Snapshot(), m.Tick(), seat); path != nil {
				if _, err := room.Submit(seat, path); err != nil {
					t.Fatalf("submit: %v", err)
				}
			}
			room.Tick()
		}
		if !room.IsOver() {
			t.Fatal("the driven match never finished")
		}
		for _, ev := range room.Match().Events() {
			if ev.Result == match.ResultAccepted && ev.Seat == 1 {
				words++
			}
		}
		return words, room.Match().Score(1)
	}

	easyWords, easyScore := run(pve.DifficultyEasy)
	hardWords, hardScore := run(pve.DifficultyHard)
	if easyWords == 0 || hardWords == 0 {
		t.Fatalf("the opponent never played (easy words=%d, hard words=%d)", easyWords, hardWords)
	}
	if hardWords <= easyWords {
		t.Fatalf("hard played %d words, no more than easy's %d: the preset does not change the opponent", hardWords, easyWords)
	}
	if easyScore == hardScore {
		t.Fatalf("easy and hard finished %d:%d identical: the preset is a relabel", easyScore, hardScore)
	}
	t.Logf("seed 1512: easy %d words / %d pts, hard %d words / %d pts",
		easyWords, easyScore, hardWords, hardScore)
}
