// Package pve holds the first player-versus-environment content: an opponent
// the SERVER drives, so a single player can play a real match alone.
//
// Why this exists now: product decision Q5 (docs/M2-BOT-POLICY.md) made a
// labelled bot mode safe to build. A PvE opponent is a declared bot, is
// disclosed in every snapshot, and the match it plays in is recorded as
// non-rating-eligible - so it cannot be confused with a ranked opponent, and
// nothing about the disclosure had to be weakened to ship it.
//
// Design constraints, in order of importance:
//
//  1. DETERMINISM (PD-003). Intent is a pure function of (snapshot, tick, the
//     loaded dictionary). No wall clock, no randomness, no map iteration order.
//     Two runs of the same policies over the same seed produce identical event
//     logs, which is what makes a practice match replayable and debuggable like
//     any other match.
//
//  2. NO SPECIAL AUTHORITY. The opponent submits through exactly the same door
//     a human does (Room.Submit), so the server validates its intents with the
//     same rules. There is no "bot path" in the simulation and no bypass to
//     keep in sync - a bug in the opponent produces rejected intents, not an
//     illegal board.
//
//  3. POLITE BY DEFAULT. The first PvE content exists to teach the loop, not to
//     punish: it takes only free cells (so it never steals from the player) and
//     thinks for a beat between words. Stealing is a policy knob, not a hidden
//     behaviour - a harder opponent is a later feature with its own evidence.
package pve

import (
	"fmt"
	"sort"
	"strings"

	"github.com/JoTalbot/words/server/internal/dictionary"
	"github.com/JoTalbot/words/server/internal/match"
)

// Policy is the shape of the opponent. Zero values are not useful; use
// DefaultPolicy and override.
type Policy struct {
	// MinLen and MaxLen bound the word lengths the opponent will consider.
	MinLen int
	MaxLen int
	// Interval is the number of ticks the opponent waits between words. At
	// 30 Hz, 45 ticks is 1.5 s - enough for a new player to read the board.
	Interval int
	// AllowSteal lets the opponent take cells a player already owns and is
	// still holding. Off by default: the first content teaches claiming before
	// it teaches defending.
	AllowSteal bool
}

// DefaultPolicy is the shipped practice opponent: short words, unhurried,
// never steals.
func DefaultPolicy() Policy {
	return Policy{MinLen: match.MinWordLength, MaxLen: 4, Interval: 45}
}

// Opponent is a server-driven player for one seat of one match.
type Opponent struct {
	dict        *dictionary.Snapshot
	candidates  []string // dictionary words in MinLen..MaxLen, sorted
	policy      Policy
	lastActTick int
}

// New loads the dictionary for lang and precomputes the candidate word list.
//
// The candidate list is built once, sorted, and never mutated: iterating a map
// or the loader's internal ordering would make the opponent's choices depend on
// something other than the match state.
func New(lang string, policy Policy) (*Opponent, error) {
	if policy.MinLen < match.MinWordLength {
		policy.MinLen = match.MinWordLength
	}
	if policy.MaxLen < policy.MinLen {
		return nil, fmt.Errorf("pve: max length %d is below min length %d", policy.MaxLen, policy.MinLen)
	}
	if policy.Interval < 1 {
		return nil, fmt.Errorf("pve: interval %d must be at least one tick", policy.Interval)
	}
	snap, err := dictionary.LoadSnapshot(dictionary.Language(lang))
	if err != nil {
		return nil, fmt.Errorf("pve: load dictionary %s: %w", lang, err)
	}
	var words []string
	for _, w := range snap.Words() {
		if n := len([]rune(w)); n >= policy.MinLen && n <= policy.MaxLen {
			words = append(words, w)
		}
	}
	sort.Strings(words)
	return &Opponent{
		dict:        snap,
		candidates:  words,
		policy:      policy,
		lastActTick: -1 << 30, // act on the first opportunity
	}, nil
}

// Policy exposes the effective policy (after New clamped it).
func (o *Opponent) Policy() Policy { return o.policy }

// CandidateCount reports how many words the opponent can choose from in this
// language and policy. It is exported for the tests and for diagnostics: an
// opponent that can spell nothing is a configuration problem, not a quiet loss.
func (o *Opponent) CandidateCount() int { return len(o.candidates) }

// Intent returns the cell path the opponent wants to play at this tick, or nil
// when it is still thinking or has nothing legal to say.
//
// It is a pure function of (snapshot, tick): the same board and the same tick
// always produce the same answer, which is what PD-003 requires and what
// TestIntentIsADeterministicFunctionOfState asserts.
func (o *Opponent) Intent(snap match.Snapshot, tick int, seat match.Seat) []int {
	if tick-o.lastActTick < o.policy.Interval {
		return nil
	}
	path := o.choose(snap, seat)
	if path == nil {
		return nil
	}
	o.lastActTick = tick
	return path
}

// choose finds the word to play: shortest first, then lexicographic, over the
// cells this policy allows. Choosing the shortest legal word keeps the board
// open for the player - the same reasoning the device-smoke partner bot uses.
//
// The usable set mirrors the server's own availability rule: a cell counts only
// if it can still SCORE for this seat. The seat's own cells are therefore never
// usable, whether or not stealing is on - a word spelled entirely over cells it
// already owns earns nothing and is rejected as blocked-by-rule, which is how
// the aggressive policy produced a rejected intent before this was fixed.
func (o *Opponent) choose(snap match.Snapshot, seat match.Seat) []int {
	usable := make([]match.CellView, 0, len(snap.Cells))
	for _, c := range snap.Cells {
		if c.IsLocked {
			continue
		}
		if c.OwnerSeat == int(seat) {
			continue
		}
		if c.OwnerSeat >= 0 && !o.policy.AllowSteal {
			continue
		}
		usable = append(usable, c)
	}
	if len(usable) < o.policy.MinLen {
		return nil
	}

	// Letters available, by cell id. Iterating the sorted candidate list and
	// the id-ordered cell list is what makes the result deterministic.
	type slot struct {
		id     int
		letter string
	}
	slots := make([]slot, 0, len(usable))
	for _, c := range usable {
		slots = append(slots, slot{id: c.ID, letter: strings.ToLower(c.Letter)})
	}

	for _, word := range o.candidates {
		used := make(map[int]bool, len(word))
		path := make([]int, 0, len(word))
		ok := true
		for _, r := range word {
			found := false
			for _, s := range slots {
				if used[s.id] {
					continue
				}
				if s.letter == string(r) {
					used[s.id] = true
					path = append(path, s.id)
					found = true
					break
				}
			}
			if !found {
				ok = false
				break
			}
		}
		if ok {
			return path
		}
	}
	return nil
}

// Known reports whether the loaded dictionary contains a word, so a caller can
// assert the opponent and the simulation agree on legality.
func (o *Opponent) Known(word string) bool { return o.dict.Contains(word) }
