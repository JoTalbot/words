package match

import (
	"sort"

	"github.com/JoTalbot/words/server/internal/dictionary"
	"github.com/JoTalbot/words/server/internal/prng"
	"github.com/JoTalbot/words/server/internal/scoring"
)

// boardRNG derives the per-wave stream from the match seed: waves of the
// same match never correlate, and the same (seed, language, wave) always
// yields the same board.
func boardRNG(matchSeed uint64, lang string, wave int) *prng.Source {
	return prng.NewFromKey(matchSeed, langCode(lang), uint64(wave), 0x57415645) // "WAVE"
}

// BoardCells is the board size for a roster (Q10, docs/PRODUCT-DECISIONS.md).
//
// Two seats keep exactly the M0 board, so 1v1 is bit-identical forever. Above
// that the board grows with the roster - twelve cells shared by sixty players
// is not a game - at BoardCellsPerSeat cells per seat, rounded up to a whole
// row of BoardColumnsLarge and capped at MaxCellsPerWave.
//
// It depends ONLY on the seat count, never on live match state, so it stays a
// pure function of (seed, language, wave, seats).
func BoardCells(seats int) int {
	if seats <= 2 {
		return CellsPerWave
	}
	n := seats * BoardCellsPerSeat
	if r := n % BoardColumnsLarge; r != 0 {
		n += BoardColumnsLarge - r
	}
	if n < CellsPerWave {
		n = CellsPerWave
	}
	if n > MaxCellsPerWave {
		n = MaxCellsPerWave
	}
	return n
}

// BoardColumns is the grid width for a roster; the client lays cells out row
// major, so this is part of the board contract, not a rendering detail.
func BoardColumns(seats int) int {
	if seats <= 2 {
		return BoardColumnsSmall
	}
	return BoardColumnsLarge
}

// generateWave samples cells letters without replacement from the weighted
// per-language pool (data v1), then shuffles them onto the cell grid.
// Deterministic for a given (seed, language, wave, size).
//
// The letter stream is drawn from a shuffle that does not depend on the board
// size, so a larger board is a PREFIX-COMPATIBLE extension of a smaller one:
// cell i holds the same letter whatever the roster. That is what lets the
// 1v1 board stay bit-identical while Royale boards grow.
func generateWave(matchSeed uint64, lang string, wave, cells int) []Cell {
	rng := boardRNG(matchSeed, lang, wave)
	pool := weightedPool(lang)
	for i := len(pool) - 1; i > 0; i-- {
		j := rng.Intn(i + 1)
		pool[i], pool[j] = pool[j], pool[i]
	}
	out := make([]Cell, cells)
	for i := 0; i < cells; i++ {
		out[i] = Cell{ID: i, Letter: pool[i]}
	}
	return out
}

// weightedPool materializes each alphabet letter Weight times (data v1).
//
// Letters are iterated in canonical alphabet order: ranging over a map is
// nondeterministic in Go and would corrupt board generation.
func weightedPool(lang string) []rune {
	var rs []rune
	for r := range scoring.Letters(dictionary.Language(lang)) {
		rs = append(rs, r)
	}
	sort.Slice(rs, func(i, j int) bool { return rs[i] < rs[j] })
	var out []rune
	for _, r := range rs {
		for i := 0; i < scoring.LetterWeight(dictionary.Language(lang), r); i++ {
			out = append(out, r)
		}
	}
	return out
}
