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
//	# hotfix: build a new versioned artifact as a delta over the previous one
//	# (the "versioned delta buffer" of docs/ARCHITECTURE.md):
//	go run ./cmd/dictcompile -lang en \
//	    -parent internal/dictionary/data/en.words \
//	    -add /tmp/hotfix-add.txt -remove /tmp/hotfix-remove.txt \
//	    -out internal/dictionary/data/en.words -write -version 2026-09-18.hf1
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
	src := flag.String("src", "", "source wordlist path (full pipeline; default empty -> parent-delta mode)")
	out := flag.String("out", "", "output artifact path (required)")
	parent := flag.String("parent", "", "previous snapshot artifact to apply -add / -remove on (hotfix mode)")
	add := flag.String("add", "", "wordlist whose lines are added, then normalized/length-filtered (hotfix mode)")
	remove := flag.String("remove", "", "wordlist whose normalized lines are removed (hotfix mode)")
	write := flag.Bool("write", false, "write the artifact; default is verify-only (compare against -out)")
	minLen := flag.Int("min", defaultMinLen, "minimum word length in runes")
	maxLen := flag.Int("max", defaultMaxLen, "maximum word length in runes")
	version := flag.String("version", "", "optional snapshot version label for the printed manifest entry")
	flag.Parse()

	if *langTag == "" || (*src == "" && *parent == "") || *out == "" {
		fmt.Fprintln(os.Stderr, "dictcompile: -lang and -out are required, and one of -src or -parent must be set")
		os.Exit(2)
	}
	lang, err := dictionary.ParseLanguage(*langTag)
	if err != nil {
		fmt.Fprintln(os.Stderr, "dictcompile:", err)
		os.Exit(2)
	}

	var words []string
	var stats compileStats
	var entry manifestEntry
	if *src != "" {
		lines, err := readLines(*src)
		if err != nil {
			fmt.Fprintln(os.Stderr, "dictcompile:", err)
			os.Exit(1)
		}
		words, stats, err = compile(lang, lines, *minLen, *maxLen)
		if err != nil {
			fmt.Fprintln(os.Stderr, "dictcompile:", err)
			os.Exit(1)
		}
		entry = manifestEntry{Version: *version, Words: len(words)}
	} else {
		words, entry, err = applyHotfix(lang, *parent, *add, *remove, *minLen, *maxLen, *version)
		if err != nil {
			fmt.Fprintln(os.Stderr, "dictcompile:", err)
			os.Exit(1)
		}
	}
	payload := encodeArtifact(words)
	sum := sha256.Sum256(payload)
	entry.SHA256 = hex.EncodeToString(sum[:])

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
				if *src != "" {
					fmt.Printf("DIFF: %s differs from the compiled artifact (%d source lines, %d skipped, %d words)\n", *out, stats.total, stats.skipped, len(words))
				} else {
					fmt.Printf("DIFF: %s differs from the hotfix artifact (%d words, parent %s, added %d, removed %d)\n", *out, len(words), entry.ParentSHA256[:12], entry.Added, entry.Removed)
				}
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
// ParentSHA256, Added and Removed record the hotfix provenance chain: a
// versioned artifact must state what it was built from and over, or it
// cannot be audited (the "versioned delta buffer" of docs/ARCHITECTURE.md).
type manifestEntry struct {
	Version      string `json:"version,omitempty"`
	SHA256       string `json:"sha256"`
	Words        int    `json:"words"`
	ParentSHA256 string `json:"parent_sha256,omitempty"`
	Added        int    `json:"added,omitempty"`
	Removed      int    `json:"removed,omitempty"`
}

// applyHotfix builds a new artifact deterministically from a parent snapshot
// plus add/remove wordlists: parent words (preserving order) -> drop the
// canonical parent header comment -> apply removals -> apply additions
// (normalized + length-filtered + deduped) -> resort. The result is
// byte-stable for identical inputs, and the provenance is captured so the
// snapshot chain stays auditable. Nothing here encodes a policy about WHAT
// may be added or removed - that is the content decision (and the B2 licence
// gate for uk) which stays with the owner.
func applyHotfix(lang dictionary.Language, parent, add, remove string, minLen, maxLen int, version string) ([]string, manifestEntry, error) {
	parentBytes, err := os.ReadFile(parent)
	if err != nil {
		return nil, manifestEntry{}, fmt.Errorf("read parent snapshot: %w", err)
	}
	// Drop the canonical header comment (a comment line starting with '#')
	// but keep every data line: the parent artifact is already
	// sorted/normalized, and the set is the single source of truth. The
	// artifact is rebuilt at the end from the set's sorted keys, so removal
	// cannot dangle and the result is always byte-stable.
	set := map[string]struct{}{}
	for _, line := range strings.Split(string(parentBytes), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		set[line] = struct{}{}
	}

	removedCount := 0
	if remove != "" {
		removeLines, err := readLines(remove)
		if err != nil {
			return nil, manifestEntry{}, fmt.Errorf("read remove list: %w", err)
		}
		for _, line := range removeLines {
			norm, err := lang.Normalize(line)
			if err != nil {
				// A removal line that is not even a valid word cannot name
				// anything in the snapshot, so it is by definition a no-op;
				// reporting it keeps the hotfix provenance honest.
				fmt.Fprintf(os.Stderr, "dictcompile: remove line not alphabet-clean, ignored: %q\n", line)
				continue
			}
			if _, ok := set[norm]; ok {
				delete(set, norm)
				removedCount++
			}
		}
	}

	addedCount := 0
	if add != "" {
		addLines, err := readLines(add)
		if err != nil {
			return nil, manifestEntry{}, fmt.Errorf("read add list: %w", err)
		}
		for _, line := range addLines {
			norm, err := lang.Normalize(line)
			if err != nil {
				fmt.Fprintf(os.Stderr, "dictcompile: add line not alphabet-clean, ignored: %q\n", line)
				continue
			}
			n := len([]rune(norm))
			if n < minLen || n > maxLen {
				continue
			}
			if _, dup := set[norm]; dup {
				continue
			}
			set[norm] = struct{}{}
			addedCount++
		}
	}

	order := make([]string, 0, len(set))
	for w := range set {
		order = append(order, w)
	}
	sort.Strings(order)
	parentSum := sha256.Sum256(parentBytes)
	return order, manifestEntry{
		Version:      version,
		Words:        len(order),
		ParentSHA256: hex.EncodeToString(parentSum[:]),
		Added:        addedCount,
		Removed:      removedCount,
	}, nil
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
