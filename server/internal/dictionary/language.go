// Package dictionary owns word validation snapshots and per-language
// normalization. Snapshot content is versioned data (see data/manifest.json);
// match rules never embed word lists.
package dictionary

import (
	"fmt"
	"strings"
)

// Language identifies a supported match dictionary language.
type Language string

const (
	En Language = "en"
	Ru Language = "ru"
	Uk Language = "uk"
)

// SupportedLanguages is the M0 language set (PD-008).
var SupportedLanguages = []Language{En, Ru, Uk}

// ParseLanguage validates a language tag.
func ParseLanguage(tag string) (Language, error) {
	switch Language(strings.ToLower(strings.TrimSpace(tag))) {
	case En:
		return En, nil
	case Ru:
		return Ru, nil
	case Uk:
		return Uk, nil
	default:
		return "", fmt.Errorf("dictionary: unsupported language %q", tag)
	}
}

// letterSets is a process-lifetime membership table per language so the
// hot validation path never allocates.
var letterSets = func() map[Language]map[rune]bool {
	out := map[Language]map[rune]bool{}
	for _, l := range SupportedLanguages {
		m := map[rune]bool{}
		for _, r := range alphabet(l) {
			m[r] = true
		}
		out[l] = m
	}
	return out
}()

// letters returns the alphabet membership table for the language.
func (l Language) letters() map[rune]bool { return letterSets[l] }

func alphabet(l Language) string {
	switch l {
	case En:
		return "abcdefghijklmnopqrstuvwxyz"
	case Ru:
		// No ё in the M0 snapshot alphabet: ё is normalized to е (v0.1 rule).
		return "абвгдежзийклмнопрстуфхцчшщыьэюя"
	case Uk:
		return "абвгґдеєжзиіїйклмнопрстуфхцчшщьюя"
	default:
		return ""
	}
}

// Normalize canonicalizes a candidate word for lookup:
//
//   - Unicode NFC;
//   - lowercase (case-insensitive membership);
//   - ru: ё folded to е (documented M0 v0.1 rule);
//   - the result must consist only of alphabet letters of the language.
//
// Normalization is deterministic and shared by the validator and by tests.
func (l Language) Normalize(word string) (string, error) {
	if word == "" {
		return "", fmt.Errorf("dictionary: empty word")
	}
	w := strings.ToLower(word)
	if l == Ru {
		w = strings.ReplaceAll(w, "ё", "е")
	}
	letters := l.letters()
	var b strings.Builder
	b.Grow(len(w))
	for _, r := range w {
		if !letters[r] {
			return "", fmt.Errorf("dictionary: word %q contains letter %q not in %s alphabet", word, r, l)
		}
		b.WriteRune(r)
	}
	return b.String(), nil
}
