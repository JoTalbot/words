// Package match implements the deterministic, server-authoritative 1v1
// simulation (docs/M0-MATCH-RULES.md).
//
// Design invariants:
//   - no wall clock, no math/rand, no map iteration order may influence
//     match logic;
//   - every mutation increments StateVersion;
//   - every intent (accepted or rejected) is appended to the event log so
//     the match is fully replayable;
//   - cell data only ever flows through the functions in this package.
package match

import (
	"fmt"
	"strings"
)

// Timing constants (docs/M0-MATCH-RULES.md §7).
const (
	// TicksPerSecond is the fixed simulation rate (30 Hz).
	TicksPerSecond = 30
	// tickMillis is the logical duration of one tick.
	tickMillis = 1000 / TicksPerSecond

	// LockTicks is the claim/steal lock duration (3 s).
	LockTicks = 90
	// WaveTicks is the wave time limit (60 s).
	WaveTicks = 1800
	// WavesPerMatch is the number of waves in an M0 match.
	WavesPerMatch = 3
	// CellsPerWave is the shared board size.
	CellsPerWave = 12
	// MinWordLength is the minimum accepted word length.
	MinWordLength = 3
	// ComboResetWindowTicks: combo resets when the previous accepted word
	// is older than this (10 s).
	ComboResetWindowTicks = 300
)

// Seat identifies a player within the match (0 or 1).
type Seat int

// CellState mirrors M0-MATCH-RULES §3.
type CellState int

const (
	CellFree CellState = iota
	CellOwnedLocked
	CellOwnedUnlocked
)

// Cell is one board cell.
type Cell struct {
	ID                 int
	Letter             rune
	Owner              Seat // valid when state != CellFree
	State              CellState
	LockRemainingTicks int
	// CreditedValue is the score amount recorded for this cell at its last
	// claim; a cross-steal debits it from the previous owner.
	CreditedValue int
}

// WordResult matches the protocol outcomes relevant to M0 rules.
type WordResult int

const (
	ResultAccepted WordResult = iota
	ResultRejectedNotInDict
	ResultBlockedByRule
	ResultInvalidInput
	ResultMatchNotActive
)

// PlayerState is the mutable per-player competitive state.
type PlayerState struct {
	Seat           Seat
	Score          int64
	Combo          int // consecutive accepted words (0 = none yet)
	LastAcceptTick int // tick of last accepted word
}

// Event is one immutable log record (accepted or rejected intent).
type Event struct {
	Seq              int // 1-based event sequence in the log
	Tick             int // simulation tick at handling time
	Seat             Seat
	CellIDs          []int  // word path in board order
	Word             string // normalized letters along the path
	Result           WordResult
	WordResultString string
	ScoreAdded       int64
	TotalScore       int64
	ComboMult        float64
	IsSteal          bool
	StateVersion     int
}

// Snapshot is the canonical public state view at a tick.
type Snapshot struct {
	MatchID         uint64
	Language        string
	Seed            uint64
	ServerTick      int
	RemainingTimeMs int
	CurrentWave     int
	StateVersion    int
	// Phase is "active" | "sudden_death" | "over". Sudden Death is only
	// reachable when the match was created with Config.SuddenDeath.
	Phase string
	// SuddenDeath reports whether the match is currently in its tiebreak
	// wave (first accepted word wins, see docs/M1-SUDDEN-DEATH.md).
	SuddenDeath bool
	Players     [2]PlayerView
	Cells       []CellView
}

// PlayerView is a snapshot of one player's competitive state.
type PlayerView struct {
	Seat         Seat
	Score        int64
	RankPosition int
	IsEliminated bool
	Combo        int
	ComboMult    float64
}

// CellView is the client-facing cell state.
type CellView struct {
	ID              int
	Letter          string
	OwnerSeat       int // -1 when free
	IsLocked        bool
	LockRemainingMs int
}

// RankResult describes the final outcome.
type RankResult struct {
	WinnerSeat Seat
	IsTie      bool
	Over       bool
}

// Config seeds a fresh match.
type Config struct {
	MatchID uint64
	Seed    uint64
	Lang    string // "en" | "ru" | "uk" (dictionary.Language)
	Dict    WordValidator
	// SuddenDeath enables the opt-in tiebreak (docs/M1-SUDDEN-DEATH.md).
	// When false the match keeps M0 rules: a tied final score is a draw.
	SuddenDeath bool
}

// WordValidator is the dictionary capability the match needs.
type WordValidator interface {
	ContainsNormalized(word string) bool
}

// LanguageTag is kept as plain string to avoid an import cycle risk in the
// public surface; callers pass dictionary.Language values.

func langCode(lang string) uint64 {
	// Stable domain-separation code per language.
	switch lang {
	case "ru":
		return 1
	case "uk":
		return 2
	default:
		return 0 // en
	}
}

// ErrInvalidCell is returned for out-of-range or duplicated cell ids.
func validatePath(cells []Cell, ids []int) error {
	if len(ids) < MinWordLength {
		return fmt.Errorf("word shorter than %d cells", MinWordLength)
	}
	seen := make(map[int]bool, len(ids))
	for _, id := range ids {
		if id < 0 || id >= len(cells) {
			return fmt.Errorf("cell %d out of range", id)
		}
		if seen[id] {
			return fmt.Errorf("cell %d duplicated", id)
		}
		seen[id] = true
	}
	return nil
}

func lettersOf(cells []Cell, ids []int) string {
	var b strings.Builder
	for _, id := range ids {
		b.WriteRune(cells[id].Letter)
	}
	return b.String()
}

// copyIDs returns a defensive copy of a cell path.
func copyIDs(ids []int) []int {
	out := make([]int, len(ids))
	copy(out, ids)
	return out
}
