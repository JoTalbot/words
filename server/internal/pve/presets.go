// Package pve presets (M2 batch 35B).
//
// Q11 deliberately left "difficulty levels" open, and PD-007 frames M0/M2
// numbers as calibration candidates. What this batch decides is the
// MECHANISM - a named, deterministic, disclosed knob behind the existing
// WORDARENA_ALLOW_BOT_SEATS gate - and the starting values. The values
// themselves are engineering defaults consistent with the doc's "polite by
// default" constraint (the first content teaches claiming before defending):
// easy is exactly the shipped 32E opponent, so no existing practice match
// changes; normal and hard grow the word range and shorten the thinking
// interval, and only hard steals. They are expected to move with playtest
// evidence, which is a data change rather than a redesign.
package pve

import "github.com/JoTalbot/words/server/internal/match"

// Difficulty names a practice opponent preset.
type Difficulty string

const (
	// DifficultyEasy is the shipped 32E opponent: short words, unhurried,
	// never steals. It is also the default for a practice match that does
	// not name a difficulty, so every existing practice flow is unchanged.
	DifficultyEasy Difficulty = "easy"
	// DifficultyNormal plays longer words and thinks half as long, but
	// still never steals: a fair opponent that answers defending with
	// pressure instead of taking it.
	DifficultyNormal Difficulty = "normal"
	// DifficultyHard is the full aggressive shape: the widest word range,
	// the shortest interval, and steals enabled. It is the same profile the
	// anti-snowball calibration harness drives (docs/M2-ANTI-SNOWBALL.md),
	// which keeps "what hard plays" measurable against that evidence.
	DifficultyHard Difficulty = "hard"
)

// Valid reports whether d is a named preset.
func (d Difficulty) Valid() bool {
	switch d {
	case DifficultyEasy, DifficultyNormal, DifficultyHard:
		return true
	}
	return false
}

// Policy resolves the preset to an opponent policy. Unknown values resolve
// to the shipped default so a typo can never produce a broken opponent -
// the request surface rejects unknown names before this is reached, and
// this keeps the zero value safe for non-HTTP callers.
func (d Difficulty) Policy() Policy {
	switch d {
	case DifficultyNormal:
		return Policy{MinLen: match.MinWordLength, MaxLen: 5, Interval: 30}
	case DifficultyHard:
		return Policy{MinLen: match.MinWordLength, MaxLen: 6, Interval: 15, AllowSteal: true}
	default:
		return DefaultPolicy()
	}
}

// All lists the presets in presentation order.
func All() []Difficulty {
	return []Difficulty{DifficultyEasy, DifficultyNormal, DifficultyHard}
}
