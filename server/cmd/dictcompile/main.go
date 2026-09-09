// Command dictcompile is the deterministic dictionary snapshot compiler
// (docs/ROADMAP.md M0: "dictionary compiler"; PD-004).
//
// It converts a plain source wordlist into the canonical runtime artifact:
// sorted, normalized (lowercase, per-language alphabet, ru: ё->е), deduped,
// length-filtered words, one per line, UTF-8, final newline — exactly the
// format server/internal/dictionary loads (data/*.words) and records in
// data/manifest.json (sha256 + word count).
//
// Usage:
//
//	# verify the shipped snapshots are canonical (idempotence check):
//	go run ./cmd/dictcompile -lang en -src internal/dictionary/data/en.words \
//	    -out internal/dictionary/data/en.words
//	# (exit 0 prints "canonical" when byte-identical)
//
//	# write a canonical artifact from an upstream wordlist:
//	go run ./cmd/dictcompile -lang en -src /tmp/upstream-en.txt \
//	    -out internal/dictionary/data/en.words -write
//
// The compiler never touches the network and produces the same bytes for
// the same input on every platform (deterministic pipeline).
package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/JoTalbot/words/server/internal/dictionary"
)

// Filter bounds of the shipped snapshots (see data/manifest.json note).
const (
	defaultMinLen = 3
	defaultMaxLen = 9
)

func main() {
	langTag := flag.String("lang", "", "language: en | ru | uk (required)")
	src := flag.String("src", "", "source wordlist path (required)")
	out := flag.String("out", "", "output artifact path (required)")
	write := flag.Bool("write", false, "write the artifact; default is verify-only (compare against -out)")
	minLen := flag.Int("min", defaultMinLen, "minimum word length in runes")
	maxLen := flag.Int("max", defaultMaxLen, "maximum word length in runes")
	version := flag.String("version", "", "optional snapshot version label for the printed manifest entry")
	flag.Parse()

	if *langTag == "" || *src == "" || *out == "" {
		fmt.Fprintln(os.Stderr, "dictcompile: -lang, -src and -out are required")
		os.Exit(2)
	}
	lang, err := dictionary.ParseLanguage(*langTag)
	if err != nil {
		fmt.Fprintln(os.Stderr, "dictcompile:", err)
		os.Exit(2)
	}

	lines, err := readLines(*src)
	if err != nil {
		fmt.Fprintln(os.Stderr, "dictcompile:", err)
		os.Exit(1)
	}
	words, stats, err := compile(lang, lines, *minLen, *maxLen)
	if err != nil {
		fmt.Fprintln(os.Stderr, "dictcompile:", err)
		os.Exit(1)
	}
	payload := encodeArtifact(words)
	sum := sha256.Sum256(payload)
	entry := manifestEntry{Version: *version, Words: len(words), SHA256: hex.EncodeToString(sum[:])}

	if *write {
		if err := os.WriteFile(*out, payload, 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "dictcompile:", err)
			os.Exit(1)
		}
		fmt.Printf("wrote %s\n", *out)
	} else {
		existing, err := os.ReadFile(*out)
		if err == nil {
			if string(existing) == string(payload) {
				fmt.Printf("canonical: %s is byte-identical to the compiled artifact\n", *out)
			} else {
				fmt.Printf("DIFF: %s differs from the compiled artifact (%d source lines, %d skipped, %d words)\n", *out, stats.total, stats.skipped, len(words))
				os.Exit(1)
			}
		} else if os.IsNotExist(err) {
			fmt.Printf("absent: %s does not exist; use -write to create it (%d words)\n", *out, len(words))
			os.Exit(1)
		} else {
			fmt.Fprintln(os.Stderr, "dictcompile:", err)
			os.Exit(1)
		}
	}
	outJSON, _ := json.MarshalIndent(entry, "", "  ")
	fmt.Println(string(outJSON))
}

// compileStats reports pipeline accounting for provenance.
type compileStats struct {
	total   int // source lines read (non-blank, non-comment)
	skipped int // lines dropped by normalization or length filter
}

// manifestEntry mirrors one data/manifest.json "languages" entry.
type manifestEntry struct {
	Version string `json:"version,omitempty"`
	SHA256  string `json:"sha256"`
	Words   int    `json:"words"`
}

// readLines reads non-blank, non-comment source lines.
func readLines(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || line[0] == '#' {
			continue
		}
		out = append(out, line)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// compile runs the deterministic pipeline: normalize -> length filter ->
// dedupe -> sort.
func compile(lang dictionary.Language, lines []string, minLen, maxLen int) ([]string, compileStats, error) {
	seen := map[string]struct{}{}
	var words []string
	stats := compileStats{total: len(lines)}
	for _, line := range lines {
		norm, err := lang.Normalize(line)
		if err != nil {
			stats.skipped++ // out-of-alphabet or empty after trim
			continue
		}
		n := len([]rune(norm))
		if n < minLen || n > maxLen {
			stats.skipped++
			continue
		}
		if _, dup := seen[norm]; dup {
			stats.skipped++
			continue
		}
		seen[norm] = struct{}{}
		words = append(words, norm)
	}
	sort.Strings(words)
	return words, stats, nil
}

// encodeArtifact renders the canonical byte form: one word per line,
// sorted, UTF-8, with a final newline and no trailing blank line.
func encodeArtifact(words []string) []byte {
	var b []byte
	for _, w := range words {
		b = append(b, w...)
		b = append(b, '\n')
	}
	return b
}
