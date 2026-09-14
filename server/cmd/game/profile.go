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
	"errors"
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

	// ownerTokenHash is the SHA-256 of the profile's owner token. It is
	// deliberately unexported and never serialized: the credential must not be
	// readable from a profile response (M2 batch 32G).
	ownerTokenHash string
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
	// SetOwnerToken stores the hash of a freshly minted owner token for a
	// profile (M2 batch 32G). The token itself is never stored: a database
	// read must not be enough to act as a player.
	SetOwnerToken(id uint64, hash string) error
	// PlayerIDByTokenHash resolves an owner-token hash to its profile. This is
	// the only way a guild mutation learns who is asking.
	PlayerIDByTokenHash(hash string) (uint64, bool, error)
	// Close releases backend resources (no-op for in-memory storage).
	Close() error
}

// memProfileStore is the in-memory player registry (default backend).
type memProfileStore struct {
	mu      sync.Mutex
	byID    map[uint64]*Profile
	byToken map[string]uint64 // owner-token hash -> player id (M2 batch 32G)
	counter uint64
}

func newMemProfileStore() *memProfileStore {
	return &memProfileStore{byID: map[uint64]*Profile{}, byToken: map[string]uint64{}}
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

func (ps *memProfileStore) SetOwnerToken(id uint64, hash string) error {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	p, ok := ps.byID[id]
	if !ok {
		return ErrGuildNotFound // reused shape: unknown profile
	}
	if p.ownerTokenHash != "" {
		// Rotation is not part of this slice; overwriting silently would make a
		// leaked token indistinguishable from a legitimate one.
		return errors.New("profile already has an owner token")
	}
	p.ownerTokenHash = hash
	ps.byToken[hash] = id
	return nil
}

func (ps *memProfileStore) PlayerIDByTokenHash(hash string) (uint64, bool, error) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	id, ok := ps.byToken[hash]
	if !ok {
		return 0, false, nil
	}
	return id, true, nil
}

func (ps *memProfileStore) Close() error { return nil }
