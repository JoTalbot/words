package scoring

import (
	"math"
	"testing"

	"github.com/JoTalbot/words/server/internal/dictionary"
)

func TestLetterValueKnown(t *testing.T) {
	cases := []struct {
		lang dictionary.Language
		r    rune
		want int
	}{
		{dictionary.En, 'e', 1}, {dictionary.En, 'q', 10}, {dictionary.En, 'j', 8},
		{dictionary.Ru, 'о', 1}, {dictionary.Ru, 'ф', 10}, {dictionary.Ru, 'щ', 10},
		{dictionary.Uk, 'ґ', 8}, {dictionary.Uk, 'і', 1}, {dictionary.Uk, 'в', 1},
	}
	for _, c := range cases {
		if got := LetterValue(c.lang, c.r); got != c.want {
			t.Errorf("LetterValue(%s, %q) = %d, want %d", c.lang, c.r, got, c.want)
		}
	}
}

// TestAlphabetCoverage: every alphabet letter of every supported language
// must have value and weight data (no zero-weight dead letters).
func TestAlphabetCoverage(t *testing.T) {
	for _, lang := range []dictionary.Language{dictionary.En, dictionary.Ru, dictionary.Uk} {
		for _, r := range alphabets[lang] {
			li := LetterInfoFor(lang, r)
			if li.Value <= 0 {
				t.Errorf("%s letter %q missing value", lang, r)
			}
			if li.Weight <= 0 {
				t.Errorf("%s letter %q missing weight", lang, r)
			}
		}
	}
}

var alphabets = map[dictionary.Language][]rune{
	dictionary.En: []rune("abcdefghijklmnopqrstuvwxyz"),
	dictionary.Ru: []rune("абвгдежзийклмнопрстуфхцчшщыьэюя"),
	dictionary.Uk: []rune("абвгґдеєжзиіїйклмнопрстуфхцчшщьюя"),
}

func TestComboMultiplier(t *testing.T) {
	cases := []struct {
		combo int
		want  float64
	}{
		{0, 1.0}, {1, 1.0}, {2, 1.25}, {3, 1.5}, {4, 1.75}, {5, 2.0}, {9, 2.0},
	}
	for _, c := range cases {
		if got := ComboMultiplier(c.combo); math.Abs(got-c.want) > 1e-9 {
			t.Errorf("ComboMultiplier(%d) = %v, want %v", c.combo, got, c.want)
		}
	}
}

func TestWordScoreGolden(t *testing.T) {
	cases := []struct {
		name        string
		freshValues []int
		length      int
		combo       int
		wantPoints  int
		wantBase    int
	}{
		{"three ones", []int{1, 1, 1}, 3, 1, 3, 3},
		{"three ones combo2", []int{1, 1, 1}, 3, 2, 3, 3}, // floor(3*1.25)=3
		{"four ones combo2", []int{1, 1, 1, 1}, 4, 2, 5, 4},
		{"len5 bonus", []int{1, 1, 1, 1, 1}, 5, 1, 10, 10}, // 5+5
		{"len5 combo5", []int{1, 1, 1, 1, 1}, 5, 5, 20, 10},
		{"len7 bonus", []int{2, 2, 2, 2, 2, 2, 2}, 7, 1, 26, 26}, // 14+12
		{"high letters", []int{10, 8, 4}, 3, 1, 22, 22},
		{"combo4 floor", []int{1, 1, 1}, 3, 4, 5, 3}, // floor(3*1.75)=5
		{"reused", []int{}, 3, 1, 0, 0},
	}
	for _, c := range cases {
		p, b := WordScore(c.freshValues, c.length, c.combo)
		if p != c.wantPoints || b != c.wantBase {
			t.Errorf("%s: WordScore = (%d,%d), want (%d,%d)", c.name, p, b, c.wantPoints, c.wantBase)
		}
	}
}

func TestWordScoreDeterministic(t *testing.T) {
	a, _ := WordScore([]int{1, 3, 1, 5, 2}, 5, 3)
	for i := 0; i < 100; i++ {
		b, _ := WordScore([]int{1, 3, 1, 5, 2}, 5, 3)
		if a != b {
			t.Fatalf("nondeterministic score %d vs %d", a, b)
		}
	}
}

func TestCellCreditedValue(t *testing.T) {
	cases := []struct {
		v    int
		mult float64
		want int
	}{
		{1, 1.0, 1}, {1, 1.25, 1}, {2, 1.25, 2}, {3, 1.25, 3},
		{5, 1.75, 8}, {10, 2.0, 20}, {1, 2.0, 2}, {1, 1.5, 1},
	}
	for _, c := range cases {
		if got := CellCreditedValue(c.v, c.mult); got != c.want {
			t.Errorf("CellCreditedValue(%d, %v) = %d, want %d", c.v, c.mult, got, c.want)
		}
	}
}
