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
	// CellsPerWave is the shared board size for a 1v1 match, and the
	// default everywhere a roster is not specified. Larger rosters use
	// BoardCells (Q10); a two-seat match is exactly the M0 board.
	CellsPerWave = 12
	// BoardColumnsSmall is the 1v1 grid width (4x3 = CellsPerWave).
	BoardColumnsSmall = 4
	// BoardColumnsLarge is the grid width for every roster above two. A
	// wider grid keeps a big board legible instead of a long ribbon.
	BoardColumnsLarge = 6
	// BoardCellsPerSeat is how much board each seat is worth above 1v1.
	// One cell per player would mean a player who claims one cell has
	// ended the wave; two keeps the board contested without making it
	// unreadable.
	BoardCellsPerSeat = 2
	// MaxCellsPerWave caps the board no matter how large the roster is.
	// 60 cells is a 6x10 grid: still readable on a phone, and at the
	// 60-seat maximum it means one cell per player, which is the intended
	// Royale contention (see Q10 in docs/PRODUCT-DECISIONS.md).
	MaxCellsPerWave = 60

	// --- Q9: Royale elimination (docs/PRODUCT-DECISIONS.md) ---

	// EliminationMinSeats is the smallest roster that culls at all. Below
	// it (1v1 and very small lobbies) every seat plays every wave, so M0
	// and M1 behaviour is untouched.
	EliminationMinSeats = 4
	// SurvivorsNumerator/SurvivorsDenominator set the share of the roster
	// that survives each wave boundary: two thirds, so a 60-seat lobby
	// goes 60 -> 40 -> 26 across the three waves.
	SurvivorsNumerator   = 2
	SurvivorsDenominator = 3
	// MinSurvivors is the floor: a cull never takes a match below a real
	// contest.
	MinSurvivors = 2
	// MinWordLength is the minimum accepted word length.
	MinWordLength = 3
	// MinSeats and MaxSeats bound the roster. MaxSeats is the M2 Royale
	// target (docs/ROADMAP.md); the simulation itself is seat-count
	// agnostic, the bound exists so a bad request cannot allocate freely.
	MinSeats = 2
	MaxSeats = 60
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
	// EliminatedAtWave is the wave index at whose boundary this seat was
	// culled, or -1 while it is still playing (Q9). Elimination is a pure
	// function of logged state at a wave boundary, so it replays exactly.
	EliminatedAtWave int
	// IsBot marks a seat the server declared to be played by a simulated
	// player (M2 batch 32D, Q5). It is set once at construction from an
	// explicit declaration and never inferred: an anonymous human seat and a
	// tooling seat are not bots. Because it is part of the canonical state it
	// is hashed into Fingerprint, so a seat that declared itself a bot cannot
	// replay equal to one that did not.
	IsBot bool
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
	// CatchUpBonus is the anti-snowball bonus folded into ScoreAdded, or zero
	// when the opt-in rule did not apply. It is recorded rather than inferred
	// so a replay can prove WHY a score differs, not only that it does.
	CatchUpBonus int64
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
	// AntiSnowball reports whether the catch-up rule is enabled for this
	// match (docs/M2-ANTI-SNOWBALL.md), so a state view can show the rule a
	// score difference came from.
	AntiSnowball bool
	Players      []PlayerView
	Cells        []CellView
}

// PlayerView is a snapshot of one player's competitive state.
type PlayerView struct {
	Seat         Seat
	Score        int64
	RankPosition int
	IsEliminated bool
	Combo        int
	ComboMult    float64
	// IsBot discloses a simulated seat to every observer (Q5). It is part of
	// the public view on purpose: disclosure that a client could forget to
	// render would not be disclosure.
	IsBot bool
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
	// Seats is the roster size (batch 30A, M2 foundation). Zero means 2, so
	// every existing 1v1 caller is unaffected; larger values are the path to
	// the 60-player Royale mode without a second simulation.
	Seats int
	// AntiSnowball enables the opt-in catch-up rule (docs/M2-ANTI-SNOWBALL.md).
	// Default false: M0 behaviour, existing baselines and replays unchanged.
	AntiSnowball bool
	// CatchUp carries the rule's constants when it is on. The zero value is
	// the 25/2/15 batch 32C set, so a Config that only sets AntiSnowball
	// behaves exactly as the shipped rule did before this field existed.
	CatchUp CatchUpParams
	// BotSeats declares which seats are played by simulated players, indexed
	// by seat (M2 batch 32D, Q5). Nil or short means "no declaration", so the
	// zero value keeps every existing caller bot-free. A declaration is the
	// ONLY way a seat becomes a bot: nothing in the server infers bot-ness
	// from an absent profile, and a position that is true here is disclosed
	// to every seat, in the state view and in the finished result.
	BotSeats []bool
	// Board carries the board-side catch-up levers measured in M2 batch 36D:
	// the claim/steal lock duration and the steal's debit fraction. The zero
	// value is the shipped rule (see DefaultBoardParams), so every existing
	// caller and replay is byte-for-byte unaffected. Like CatchUp it is a
	// calibration parameter, not a client knob.
	Board BoardParams
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
