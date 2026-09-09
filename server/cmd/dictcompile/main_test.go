package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/JoTalbot/words/server/internal/dictionary"
)

func TestCompilePipelineNormalizesAndSorts(t *testing.T) {
	src := []string{
		"# header comment",
		"  Cat  ", // trims, lowercases
		"dog",
		"cat",        // duplicate after normalize
		"xy1",        // out-of-alphabet -> skipped
		"a",          // too short -> skipped
		"ab",         // too short
		"abcdefghi",  // 9 ok
		"abcdefghij", // 10 -> skipped
		"",
	}
	words, stats, err := compile(dictionary.En, src, 3, 9)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"abcdefghi", "cat", "dog"}
	if len(words) != len(want) {
		t.Fatalf("got %v, want %v", words, want)
	}
	for i := range want {
		if words[i] != want[i] {
			t.Fatalf("word %d = %q, want %q", i, words[i], want[i])
		}
	}
	// stats.total counts raw input lines: comment + blank + 5 skipped + 3 accepted.
	if stats.total != 10 || stats.skipped != 7 {
		t.Fatalf("total=%d skipped=%d, want total=10 skipped=7", stats.total, stats.skipped)
	}
}

func TestCompileDeterministicBytes(t *testing.T) {
	src := []string{"banana", "apple", "cherry", "apple", "APPLE"}
	b1, _, err := compile(dictionary.En, src, 3, 9)
	if err != nil {
		t.Fatal(err)
	}
	b2, _, err := compile(dictionary.En, src, 3, 9)
	if err != nil {
		t.Fatal(err)
	}
	if hex.EncodeToString(sha256.New().Sum(encodeArtifact(b1))) != hex.EncodeToString(sha256.New().Sum(encodeArtifact(b2))) {
		t.Fatal("compilation is not deterministic")
	}
}

func TestCompileRuYotationNormalization(t *testing.T) {
	// ё is normalized to е (M0 ru alphabet rule); ёж->еж is filtered out
	// by the length bound (2 runes).
	src := []string{"ёж", "еж", "ЁЛКА", "ёлка", "ёжик"}
	words, _, err := compile(dictionary.Ru, src, 3, 9)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"ежик", "елка"} // byte-wise sort (UTF-8), deterministic across platforms
	if len(words) != len(want) || words[0] != want[0] || words[1] != want[1] {
		t.Fatalf("got %v, want %v", words, want)
	}
}

func TestCompileUkWorks(t *testing.T) {
	words, _, err := compile(dictionary.Uk, []string{"абажур", "абажур", "абзац"}, 3, 9)
	if err != nil {
		t.Fatal(err)
	}
	if len(words) != 2 {
		t.Fatalf("uk words = %v", words)
	}
}

// TestShippedSnapshotsCanonical locks the data pipeline: every shipped
// data/*.words artifact must be byte-identical to what the compiler
// produces from it (sorted, normalized, deduped, length-filtered), and its
// manifest sha256/word count must match. Data files are compiled artifacts;
// hand edits now fail CI.
func TestShippedSnapshotsCanonical(t *testing.T) {
	dataDir := filepath.Join("..", "..", "internal", "dictionary", "data")
	raw, err := os.ReadFile(filepath.Join(dataDir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var mf struct {
		Version   string `json:"version"`
		Languages map[string]struct {
			File   string `json:"file"`
			SHA256 string `json:"sha256"`
			Words  int    `json:"words"`
		} `json:"languages"`
	}
	if err := json.Unmarshal(raw, &mf); err != nil {
		t.Fatal(err)
	}
	for _, langTag := range []string{"en", "ru", "uk"} {
		entry := mf.Languages[langTag]
		path := filepath.Join(dataDir, entry.File)
		src, err := readLines(path)
		if err != nil {
			t.Fatal(err)
		}
		lang, err := dictionary.ParseLanguage(langTag)
		if err != nil {
			t.Fatal(err)
		}
		words, _, err := compile(lang, src, defaultMinLen, defaultMaxLen)
		if err != nil {
			t.Fatal(err)
		}
		got := string(encodeArtifact(words))
		onDisk, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if got != string(onDisk) {
			t.Fatalf("%s is not a canonical compiled artifact (run dictcompile -lang %s -src %s -out %s -write after an intentional change)", entry.File, langTag, path, path)
		}
		sum := sha256.Sum256(onDisk)
		if gotHash := hex.EncodeToString(sum[:]); gotHash != entry.SHA256 {
			t.Fatalf("%s manifest sha256 mismatch: file %s, manifest %s", entry.File, gotHash, entry.SHA256)
		}
		if len(words) != entry.Words {
			t.Fatalf("%s manifest count %d != compiled %d", entry.File, entry.Words, len(words))
		}
	}
}
