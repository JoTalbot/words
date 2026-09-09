package main

// Basic in-memory 1v1 matchmaking (M1 vertical slice). Players are queued
// per language; two waiting players of the same language are paired FIFO and
// a live room is created for them. Clients poll their queue entry until they
// receive match join info. This is a stub, not a ranked MMR system.

import (
	"sync"
	"time"
)

// queueTTL is the default time a queue entry stays valid before expiring.
const queueTTL = 2 * time.Minute

// createRoomFn abstracts room provisioning so the matchmaker stays free of
// transport/room details (it is injected by the API).
type createRoomFn func(lang string) (id uint64, seed uint64, tokens [2]string, userIDs [2]uint64, err error)

// queueEntry is one waiting player and, once matched, their join info.
type queueEntry struct {
	ID        string    `json:"queue_id"`
	Language  string    `json:"language"`
	Status    string    `json:"status"` // "waiting" | "matched" | "expired"
	MatchID   uint64    `json:"match_id,omitempty"`
	Seed      uint64    `json:"seed,omitempty"`
	Token     string    `json:"token,omitempty"`
	UserID    uint64    `json:"user_id,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	deadline  time.Time
}

// matchmaker pairs waiting players per language.
type matchmaker struct {
	mu      sync.Mutex
	ttl     time.Duration
	waiting map[string][]*queueEntry // language -> FIFO
	entries map[string]*queueEntry   // id -> entry
}

func newMatchmaker() *matchmaker {
	return &matchmaker{
		ttl:     queueTTL,
		waiting: map[string][]*queueEntry{},
		entries: map[string]*queueEntry{},
	}
}

// enqueue registers a player and attempts pairing. It returns the entry; if
// a match was formed immediately, the entry already carries join info.
func (mm *matchmaker) enqueue(lang string, create createRoomFn) *queueEntry {
	mm.mu.Lock()
	defer mm.mu.Unlock()

	id, err := randomHex(8)
	if err != nil {
		// crypto/rand failure is unrecoverable; return a waiting entry that
		// will never match rather than crashing the handler. In practice
		// randomHex only fails on catastrophic entropy exhaustion.
		id = "0000000000000000"
	}
	e := &queueEntry{
		ID:        id,
		Language:  lang,
		Status:    "waiting",
		CreatedAt: time.Now(),
		deadline:  time.Now().Add(mm.ttl),
	}
	mm.entries[e.ID] = e
	mm.waiting[lang] = append(mm.waiting[lang], e)
	mm.pairLocked(lang, create)
	return e
}

// poll returns the current state of a queue entry, marking it expired (and
// removing it) when its deadline passed while still waiting. Matched entries
// are delivered once and then removed.
func (mm *matchmaker) poll(id string) (*queueEntry, bool) {
	mm.mu.Lock()
	defer mm.mu.Unlock()
	e, ok := mm.entries[id]
	if !ok {
		return nil, false
	}
	if e.Status == "waiting" && time.Now().After(e.deadline) {
		e.Status = "expired"
		mm.waiting[e.Language] = removeWaiting(mm.waiting[e.Language], e)
		delete(mm.entries, id)
		return e, true
	}
	if e.Status == "matched" {
		delete(mm.entries, id) // one-shot delivery
	}
	return e, true
}

// pairLocked pairs queued players of one language while at least two wait.
// If room creation fails (capacity), pairing stops until the next enqueue.
func (mm *matchmaker) pairLocked(lang string, create createRoomFn) {
	for {
		q := mm.waiting[lang]
		if len(q) < 2 {
			return
		}
		a, b := q[0], q[1]
		id, seed, tokens, userIDs, err := create(lang)
		if err != nil {
			return
		}
		a.Status, a.MatchID, a.Seed, a.Token, a.UserID = "matched", id, seed, tokens[0], userIDs[0]
		b.Status, b.MatchID, b.Seed, b.Token, b.UserID = "matched", id, seed, tokens[1], userIDs[1]
		mm.waiting[lang] = q[2:]
	}
}

// removeWaiting removes one entry from a FIFO queue by pointer identity.
func removeWaiting(q []*queueEntry, target *queueEntry) []*queueEntry {
	for i, e := range q {
		if e == target {
			return append(q[:i], q[i+1:]...)
		}
	}
	return q
}

// reap purges entries that clients abandoned without polling: waiting
// entries past their deadline and matched entries whose join info was never
// collected. Without this, an enqueue-only client leaks a queueEntry forever.
// The API calls reap periodically from its reaper goroutine.
func (mm *matchmaker) reap() {
	mm.mu.Lock()
	defer mm.mu.Unlock()
	now := time.Now()

	for lang, q := range mm.waiting {
		kept := q[:0]
		for _, e := range q {
			if e.Status == "waiting" && now.After(e.deadline) {
				e.Status = "expired"
				delete(mm.entries, e.ID)
				continue
			}
			kept = append(kept, e)
		}
		if len(kept) == 0 {
			delete(mm.waiting, lang)
		} else {
			mm.waiting[lang] = kept
		}
	}

	for id, e := range mm.entries {
		if e.Status == "matched" && now.After(e.deadline) {
			delete(mm.entries, id)
		}
	}
}
