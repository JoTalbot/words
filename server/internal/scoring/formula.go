package scoring

import "github.com/JoTalbot/words/server/internal/dictionary"

// Formula constants (docs/M0-MATCH-RULES.md §4). Ticks are a match-layer
// concern; scoring only consumes the derived combo value.
const (
	// MinWordLength is enforced by the rules layer; scoring assumes ≥ 1.
	LengthBonusAt5        = 5
	LengthBonusAt7        = 12
	ComboCap              = 5
	ComboStep             = 0.25
	ComboResetWindowTicks = 300
)

// ComboMultiplier returns m = 1 + 0.25 × min(combo−1, 4), m ∈ [1, 2],
// for the current combo count (combo counts accepted words in a row).
func ComboMultiplier(combo int) float64 {
	if combo < 1 {
		combo = 1
	}
	c := combo - 1
	if c > ComboCap-1 {
		c = ComboCap - 1
	}
	return 1 + ComboStep*float64(c)
}

// WordScore computes the deterministic score for a word claim.
//
// freshValues are the per-cell base values of cells that generate points
// (fresh claims and steals; re-used own cells contribute 0 and are excluded).
// length is the total word length and drives the length bonus.
// combo is the player's combo count at the moment of the claim.
//
// Returns the final integer points and the raw base (values + bonus).
func WordScore(freshValues []int, length int, combo int) (points, base int) {
	for _, v := range freshValues {
		base += v
	}
	switch {
	case length >= 7:
		base += LengthBonusAt7
	case length >= 5:
		base += LengthBonusAt5
	}
	mult := ComboMultiplier(combo)
	return floorScore(float64(base) * mult), base
}

// floorScore floors a non-negative float exactly for the M0 domain values.
// It intentionally avoids math.Floor rounding surprises by adding a tiny
// epsilon only when the value is not integral.
func floorScore(v float64) int {
	iv := int(v)
	if float64(iv) > v {
		return iv - 1
	}
	return iv
}

// CellCreditedValue is the amount recorded on a cell at claim time and
// debited from its owner if the cell is later stolen (M0-MATCH-RULES §4).
func CellCreditedValue(letterValue int, multiplier float64) int {
	return floorScore(float64(letterValue) * multiplier)
}

var _ = dictionary.En // keep import for future language helpers
