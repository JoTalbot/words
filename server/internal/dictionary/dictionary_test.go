package dictionary

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

// TestManifestChecksums makes the versioned data set tamper-evident: the
// manifest sha256 must match the embedded files byte for byte.
func TestManifestChecksums(t *testing.T) {
	for lang, lm := range manifestData.Languages {
		raw, err := dataFS.ReadFile("data/" + lm.File)
		if err != nil {
			t.Fatalf("%s: read: %v", lang, err)
		}
		sum := sha256.Sum256(raw)
		if got := hex.EncodeToString(sum[:]); got != lm.SHA256 {
			t.Errorf("%s: manifest sha256 %s != file sha256 %s", lang, lm.SHA256, got)
		}
	}
}

// TestSnapshotSizes pins snapshot word counts so accidental data edits are
// visible. Growing data is fine, but the pin changes deliberately.
func TestSnapshotSizes(t *testing.T) {
	want := map[Language]int{En: 139, Ru: 96, Uk: 56}
	for lang, n := range want {
		snap, err := LoadSnapshot(lang)
		if err != nil {
			t.Fatalf("%s: %v", lang, err)
		}
		if got := snap.Size(); got != n {
			t.Errorf("%s snapshot size = %d, want %d", lang, got, n)
		}
	}
}

func TestNormalize(t *testing.T) {
	cases := []struct {
		lang Language
		in   string
		want string
		err  bool
	}{
		{En, "Cat", "cat", false},
		{En, "CAT", "cat", false},
		{En, "co1t", "", true},
		{En, "hôtel", "", true},
		{Ru, "Кот", "кот", false},
		{Ru, "ЁЖ", "еж", false},   // ё -> е (M0 v0.1 rule)
		{Ru, "МЁД", "мед", false}, // ё -> е
		{Ru, "ко1т", "", true},
		{Ru, "яма2", "", true},
		{Uk, "Кіт", "кіт", false},
		{Uk, "Їжак", "їжак", false},
		{Uk, "Ґанок", "ґанок", false},
		{Uk, "сыр", "", true}, // ы is not in the uk alphabet
		{Uk, "съезд", "", true},
	}
	for _, c := range cases {
		got, err := c.lang.Normalize(c.in)
		if c.err {
			if err == nil {
				t.Errorf("%s.Normalize(%q): want error, got %q", c.lang, c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s.Normalize(%q): %v", c.lang, c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("%s.Normalize(%q) = %q, want %q", c.lang, c.in, got, c.want)
		}
	}
}

func TestContains(t *testing.T) {
	en, err := LoadSnapshot(En)
	if err != nil {
		t.Fatal(err)
	}
	ru, err := LoadSnapshot(Ru)
	if err != nil {
		t.Fatal(err)
	}
	uk, err := LoadSnapshot(Uk)
	if err != nil {
		t.Fatal(err)
	}
	checks := []struct {
		lang Language
		word string
		want bool
	}{
		{En, "CAT", true},
		{En, "table", true},
		{En, "stone", true},
		{En, "zzzz", false},
		{En, "cat'", false},
		{Ru, "Кот", true},
		{Ru, "мёд", true}, // stored normalized as мед
		{Ru, "мед", true},
		{Ru, "ёж", false},
		{Ru, "абракадабра", false},
		{Uk, "кіт", true},
		{Uk, "ґрати", true},
		{Uk, "їжак", true},
		{Uk, "місяць", true},
		{Uk, "сыр", false},
	}
	for _, c := range checks {
		var got bool
		switch c.lang {
		case En:
			got = en.Contains(c.word)
		case Ru:
			got = ru.Contains(c.word)
		case Uk:
			got = uk.Contains(c.word)
		}
		if got != c.want {
			t.Errorf("%s.Contains(%q) = %v, want %v", c.lang, c.word, got, c.want)
		}
	}
}

// TestLoadSnapshotFromReader proves deterministic custom snapshots load
// through the same normalization path as shipped data (future compiled
// artifacts use this path).
func TestLoadSnapshotFromReader(t *testing.T) {
	snap, err := LoadSnapshotFromReader(En, strings.NewReader("ABC\ncat\n\n#comment\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !snap.Contains("abc") || !snap.Contains("CAT") {
		t.Fatalf("reader snapshot lookup failed: %v", snap.Words())
	}
	if snap.Contains("dog") {
		t.Fatalf("reader snapshot unexpectedly contains dog")
	}
	if n := snap.Size(); n != 2 {
		t.Fatalf("reader snapshot size = %d, want 2", n)
	}
}

// TestInvalidDataLineRejected: shipped-style data with a bad line must fail
// loudly instead of silently producing a partial dictionary.
func TestInvalidDataLineRejected(t *testing.T) {
	if _, err := LoadSnapshotFromReader(Ru, strings.NewReader("кот\nкошка123\n")); err == nil {
		t.Fatal("want error for malformed line")
	}
}

// TestSupportedLanguages covers PD-008 language set.
func TestSupportedLanguages(t *testing.T) {
	for _, l := range SupportedLanguages {
		if _, err := ParseLanguage(string(l)); err != nil {
			t.Errorf("ParseLanguage(%q): %v", l, err)
		}
	}
	if _, err := ParseLanguage("fr"); err == nil {
		t.Error("want error for unsupported language")
	}
	if _, err := ParseLanguage(""); err == nil {
		t.Error("want error for empty language")
	}
}
