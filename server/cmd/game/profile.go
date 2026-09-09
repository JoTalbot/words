package main

// Player profiles (M1 vertical slice). The prototype keeps profiles
// in-memory; durable storage (PostgreSQL) is a follow-up task. A profile is
// the account identity referenced by match user_ids when a client supplies
// player_ids at match creation. Anonymous matches keep synthetic user ids and
// never touch profile stats.

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

// profileStore is the in-memory player registry.
type profileStore struct {
	mu      sync.Mutex
	byID    map[uint64]*Profile
	counter uint64
}

func newProfileStore() *profileStore {
	return &profileStore{byID: map[uint64]*Profile{}}
}

func (ps *profileStore) create(nickname, lang string) *Profile {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	ps.counter++
	p := &Profile{ID: ps.counter, Nickname: nickname, Language: lang, CreatedAt: time.Now()}
	ps.byID[p.ID] = p
	return p
}

func (ps *profileStore) get(id uint64) (*Profile, bool) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	p, ok := ps.byID[id]
	return p, ok
}

// record folds one finished match into a profile's stats. outcome is one of
// "win", "loss", "draw". Unknown ids (anonymous/synthetic seats) are no-ops.
func (ps *profileStore) record(id uint64, score int64, outcome string) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	p, ok := ps.byID[id]
	if !ok {
		return
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
}
