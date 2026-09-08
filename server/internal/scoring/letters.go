// Package scoring implements the deterministic M0 scoring formula
// (docs/M0-MATCH-RULES.md §4).
//
// Letter tables below are DATA (snapshot v1), not match-rule code. They are
// approximate prototyping values derived from typical published letter
// frequency tables; every value is a calibration candidate (PD Q1/Q6) and
// must be replaced through the data pipeline, never hot-patched in code.
package scoring

import "github.com/JoTalbot/words/server/internal/dictionary"

// LetterInfo describes one letter's gameplay data.
type LetterInfo struct {
	// Value is the base points awarded when a cell with this letter is
	// freshly claimed or stolen.
	Value int
	// Weight drives deterministic board letter sampling (relative frequency).
	Weight int
}

// lettersV1 holds the v1 letter tables per language.
var lettersV1 = map[dictionary.Language]map[rune]LetterInfo{
	dictionary.En: {
		'a': {1, 80}, 'b': {3, 15}, 'c': {3, 30}, 'd': {2, 45}, 'e': {1, 120},
		'f': {4, 20}, 'g': {2, 17}, 'h': {4, 65}, 'i': {1, 75}, 'j': {8, 2},
		'k': {5, 8}, 'l': {1, 40}, 'm': {3, 25}, 'n': {1, 75}, 'o': {1, 80},
		'p': {3, 17}, 'q': {10, 1}, 'r': {1, 60}, 's': {1, 70}, 't': {1, 90},
		'u': {1, 30}, 'v': {4, 11}, 'w': {4, 20}, 'x': {8, 2}, 'y': {4, 17},
		'z': {10, 1},
	},
	dictionary.Ru: {
		'а': {1, 80}, 'б': {3, 12}, 'в': {1, 45}, 'г': {3, 13}, 'д': {2, 30},
		'е': {1, 85}, 'ж': {5, 7}, 'з': {2, 13}, 'и': {1, 75}, 'й': {4, 10},
		'к': {2, 35}, 'л': {1, 42}, 'м': {2, 32}, 'н': {1, 65}, 'о': {1, 110},
		'п': {2, 28}, 'р': {1, 50}, 'с': {1, 55}, 'т': {1, 60}, 'у': {2, 26},
		'ф': {10, 2}, 'х': {5, 8}, 'ц': {5, 4}, 'ч': {5, 12}, 'ш': {8, 6},
		'щ': {10, 3}, 'ы': {4, 16}, 'ь': {4, 14}, 'э': {8, 3}, 'ю': {8, 5},
		'я': {2, 20},
	},
	dictionary.Uk: {
		'а': {1, 85}, 'б': {3, 13}, 'в': {1, 55}, 'г': {3, 12}, 'ґ': {8, 1},
		'д': {2, 27}, 'е': {1, 60}, 'є': {5, 5}, 'ж': {5, 7}, 'з': {2, 14},
		'и': {1, 60}, 'і': {1, 70}, 'ї': {8, 8}, 'й': {4, 6}, 'к': {2, 33},
		'л': {1, 35}, 'м': {2, 30}, 'н': {1, 70}, 'о': {1, 95}, 'п': {2, 25},
		'р': {1, 45}, 'с': {1, 42}, 'т': {1, 50}, 'у': {2, 22}, 'ф': {8, 1},
		'х': {5, 8}, 'ц': {5, 4}, 'ч': {5, 7}, 'ш': {8, 4}, 'щ': {10, 2},
		'ь': {5, 13}, 'ю': {5, 5}, 'я': {2, 18},
	},
}

// DataVersion identifies the letter tables revision.
const DataVersion = "v1"

// LetterInfoFor returns letter data, or (zero) if the letter is not part of
// the language alphabet.
func LetterInfoFor(lang dictionary.Language, r rune) LetterInfo {
	return lettersV1[lang][r]
}

// LetterValue returns the base points for a letter.
func LetterValue(lang dictionary.Language, r rune) int {
	return lettersV1[lang][r].Value
}

// LetterWeight returns the sampling weight for a letter.
func LetterWeight(lang dictionary.Language, r rune) int {
	return lettersV1[lang][r].Weight
}

// Letters returns a copy of the table for a language (read-only use in prod
// paths; callers must not mutate).
func Letters(lang dictionary.Language) map[rune]LetterInfo {
	out := make(map[rune]LetterInfo, len(lettersV1[lang]))
	for k, v := range lettersV1[lang] {
		out[k] = v
	}
	return out
}
