package dictionary

import (
	"bufio"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"sort"
	"strings"
	"sync"
)

//go:embed data
var dataFS embed.FS

// SnapshotVersion is the shipped data set version (see data/manifest.json).
const SnapshotVersion = "2026-09-08.v2"

// manifest mirrors data/manifest.json.
type manifest struct {
	Schema    int                          `json:"schema"`
	Version   string                       `json:"version"`
	Languages map[string]manifestLanguage  `json:"languages"`
}

type manifestLanguage struct {
	File   string `json:"file"`
	SHA256 string `json:"sha256"`
}

// Snapshot is an immutable in-memory dictionary snapshot (PD-004).
type Snapshot struct {
	Lang    Language
	Version string
	words   map[string]struct{}
}

// cache holds one immutable Snapshot per language. Snapshots are read-only
// after construction, so every match in the process shares the same
// dictionary (v1 rule: validation is local, deterministic and cheap).
var cache sync.Map // Language -> *Snapshot

// LoadSnapshot reads the embedded data snapshot for a language and validates
// every line against the language normalization rules. Any malformed line is
// an error: data files are versioned artifacts and must stay clean. Results
// are cached for the process lifetime.
func LoadSnapshot(lang Language) (*Snapshot, error) {
	if v, ok := cache.Load(lang); ok {
		return v.(*Snapshot), nil
	}
	if _, ok := manifestData.Languages[string(lang)]; !ok {
		return nil, fmt.Errorf("dictionary: no snapshot data for %s", lang)
	}
	raw, err := fs.ReadFile(dataFS, "data/"+manifestData.Languages[string(lang)].File)
	if err != nil {
		return nil, fmt.Errorf("dictionary: read snapshot: %w", err)
	}
	words := map[string]struct{}{}
	sc := bufio.NewScanner(strings.NewReader(string(raw)))
	line := 0
	for sc.Scan() {
		line++
		txt := strings.TrimSpace(sc.Text())
		if txt == "" || strings.HasPrefix(txt, "#") {
			continue
		}
		norm, err := lang.Normalize(txt)
		if err != nil {
			return nil, fmt.Errorf("dictionary: %s snapshot line %d: %w", lang, line, err)
		}
		words[norm] = struct{}{}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("dictionary: scan snapshot: %w", err)
	}
	if len(words) == 0 {
		return nil, fmt.Errorf("dictionary: snapshot %s is empty", lang)
	}
	snap := &Snapshot{Lang: lang, Version: SnapshotVersion, words: words}
	cache.Store(lang, snap)
	return snap, nil
}

// LoadSnapshotFromReader builds a Snapshot from an explicit reader
// (deterministic test fixtures and future compiled artifacts).
func LoadSnapshotFromReader(lang Language, r io.Reader) (*Snapshot, error) {
	words := map[string]struct{}{}
	sc := bufio.NewScanner(r)
	line := 0
	for sc.Scan() {
		line++
		txt := strings.TrimSpace(sc.Text())
		if txt == "" || strings.HasPrefix(txt, "#") {
			continue
		}
		norm, err := lang.Normalize(txt)
		if err != nil {
			return nil, fmt.Errorf("dictionary: fixture line %d: %w", line, err)
		}
		words[norm] = struct{}{}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("dictionary: scan: %w", err)
	}
	return &Snapshot{Lang: lang, Version: "fixture", words: words}, nil
}

// Contains reports whether the normalized form of word is in the snapshot.
// Lookup never mutates and never touches the network.
func (s *Snapshot) Contains(word string) bool {
	norm, err := s.Lang.Normalize(word)
	if err != nil {
		return false
	}
	_, ok := s.words[norm]
	return ok
}

// ContainsNormalized reports membership for an already normalized word.
func (s *Snapshot) ContainsNormalized(norm string) bool {
	_, ok := s.words[norm]
	return ok
}

// Words returns the sorted normalized word list (for fixtures/tools).
func (s *Snapshot) Words() []string {
	out := make([]string, 0, len(s.words))
	for w := range s.words {
		out = append(out, w)
	}
	sort.Strings(out)
	return out
}

// Size returns the number of words in the snapshot.
func (s *Snapshot) Size() int { return len(s.words) }

// manifestData is populated at init from the embedded manifest file.
var manifestData manifest

func init() {
	raw, err := dataFS.ReadFile("data/manifest.json")
	if err != nil {
		panic(fmt.Sprintf("dictionary: embed manifest: %v", err))
	}
	if err := json.Unmarshal(raw, &manifestData); err != nil {
		panic(fmt.Sprintf("dictionary: manifest parse: %v", err))
	}
}
