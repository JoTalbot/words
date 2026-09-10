package main

// Player profiles (M1 vertical slice). A profile is the account identity
// referenced by match user_ids when a client supplies player_ids at match
// creation. Anonymous matches keep synthetic user ids and never touch
// profile stats.
//
// Storage is behind the ProfileRepo interface: the default is in-memory
// (memProfileStore); when WORDARENA_POSTGRES_DSN is set the Postgres
// implementation (store_pg.go) is used so profiles survive restarts.

import (
	"sync"
	"time"
)

// Profile is a player's identity plus lifetime competitive stats.
type Profile struct {
	ID            uint64    `json:"id"`
	Nickname      string    `json:"nickname"`
	Language      string    `json:"language"`
	CreatedAt     time.Time `json:"created_at"`
	MatchesPlayed uint64    `json:"matches_played"`
	Wins          uint64    `json:"wins"`
	Losses        uint64    `json:"losses"`
	Draws         uint64    `json:"draws"`
	TotalScore    int64     `json:"total_score"`
}

// ProfileRepo persists player profiles and folds finished-match outcomes
// into lifetime stats.
type ProfileRepo interface {
	// Create registers a new profile and assigns its id.
	Create(nickname, lang string) (Profile, error)
	// Get returns a profile by id.
	Get(id uint64) (Profile, bool, error)
	// Record folds one finished match into a profile's stats. outcome is
	// one of "win", "loss", "draw". Unknown ids (synthetic seats) are no-ops.
	Record(id uint64, score int64, outcome string) error
	// Close releases backend resources (no-op for in-memory storage).
	Close() error
}

// memProfileStore is the in-memory player registry (default backend).
type memProfileStore struct {
	mu      sync.Mutex
	byID    map[uint64]*Profile
	counter uint64
}

func newMemProfileStore() *memProfileStore {
	return &memProfileStore{byID: map[uint64]*Profile{}}
}

func (ps *memProfileStore) Create(nickname, lang string) (Profile, error) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	ps.counter++
	p := &Profile{ID: ps.counter, Nickname: nickname, Language: lang, CreatedAt: time.Now()}
	ps.byID[p.ID] = p
	return *p, nil
}

func (ps *memProfileStore) Get(id uint64) (Profile, bool, error) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	p, ok := ps.byID[id]
	if !ok {
		return Profile{}, false, nil
	}
	return *p, true, nil
}

func (ps *memProfileStore) Record(id uint64, score int64, outcome string) error {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	p, ok := ps.byID[id]
	if !ok {
		return nil
	}
	p.MatchesPlayed++
	p.TotalScore += score
	switch outcome {
	case "win":
		p.Wins++
	case "loss":
		p.Losses++
	case "draw":
		p.Draws++
	}
	return nil
}

func (ps *memProfileStore) Close() error { return nil }
