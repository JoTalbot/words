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

// generateWave samples CellsPerWave letters without replacement from the
// weighted per-language pool (data v1), then shuffles them onto the cell
// grid. Deterministic for a given (seed, language, wave).
func generateWave(matchSeed uint64, lang string, wave int) []Cell {
	rng := boardRNG(matchSeed, lang, wave)
	pool := weightedPool(lang)
	for i := len(pool) - 1; i > 0; i-- {
		j := rng.Intn(i + 1)
		pool[i], pool[j] = pool[j], pool[i]
	}
	cells := make([]Cell, CellsPerWave)
	for i := 0; i < CellsPerWave; i++ {
		cells[i] = Cell{ID: i, Letter: pool[i]}
	}
	return cells
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
