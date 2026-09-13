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
// transport/room details (it is injected by the API). playerIDs, when
// non-nil, binds seats to registered profiles; a zero entry means the seat
// stays synthetic (anonymous).
// Batch 30C: playerIDs is seat-indexed and its LENGTH is the roster size, so
// the same matchmaker forms 1v1 matches and larger lobbies. A zero entry
// means that seat stays synthetic (anonymous).
type createRoomFn func(lang string, playerIDs []uint64) (roomInfo, error)

// queueEntry is one waiting player and, once matched, their join info.
type queueEntry struct {
	ID       string `json:"queue_id"`
	Language string `json:"language"`
	Status   string `json:"status"` // "waiting" | "matched" | "expired"
	// PlayerID is the optional registered profile the player queued with
	// (0 = anonymous). When both seats have profiles, the match folds its
	// outcome into their lifetime stats on completion.
	PlayerID uint64 `json:"player_id,omitempty"`
	MatchID  uint64 `json:"match_id,omitempty"`
	Seed     uint64 `json:"seed,omitempty"`
	Token    string `json:"token,omitempty"`
	UserID   uint64 `json:"user_id,omitempty"`
	// MatchCode and ReadCapability are the unguessable handles for the result
	// and replay endpoints. A queued player learns them here, exactly once,
	// because the matchmaker - not the client - is what created the match.
	MatchCode      string    `json:"match_code,omitempty"`
	ReadCapability string    `json:"read_capability,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	deadline       time.Time
}

// matchmaker pairs waiting players per language.
type matchmaker struct {
	mu      sync.Mutex
	ttl     time.Duration
	waiting map[string][]*queueEntry // language -> FIFO
	entries map[string]*queueEntry   // id -> entry
	// seats is the roster size this matchmaker forms (batch 30C). It stays
	// 2 - the 1v1 vertical slice - until a Royale mode is switched on; the
	// pairing loop itself is written for any N so there is no second
	// matchmaker to keep in sync.
	seats int
}

func newMatchmaker() *matchmaker {
	return newMatchmakerWithSeats(2)
}

// newMatchmakerWithSeats builds a matchmaker that forms matches of the given
// roster size. Values below 2 are clamped to 2: a "match" of one player is
// not a match.
func newMatchmakerWithSeats(seats int) *matchmaker {
	if seats < 2 {
		seats = 2
	}
	return &matchmaker{
		ttl:     queueTTL,
		waiting: map[string][]*queueEntry{},
		entries: map[string]*queueEntry{},
		seats:   seats,
	}
}

// enqueue registers a player and attempts pairing. It returns the entry; if
// a match was formed immediately, the entry already carries join info. A
// non-zero playerID must be a registered profile (validated by the caller);
// the same profile cannot wait in the same language queue twice — the
// existing entry is returned instead (idempotent re-enqueue).
func (mm *matchmaker) enqueue(lang string, playerID uint64, create createRoomFn) *queueEntry {
	mm.mu.Lock()
	defer mm.mu.Unlock()

	if playerID != 0 {
		for _, e := range mm.waiting[lang] {
			if e.PlayerID == playerID {
				return e
			}
		}
	}

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
		PlayerID:  playerID,
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

// pairLocked forms matches of mm.seats players of one language while that
// many wait. If room creation fails (capacity), pairing stops until the next
// enqueue. Seats keep FIFO order (first enqueue -> seat 0); profile ids bind
// only the seats that supplied them.
func (mm *matchmaker) pairLocked(lang string, create createRoomFn) {
	seats := mm.seats
	if seats < 2 {
		seats = 2
	}
	for {
		q := mm.waiting[lang]
		if len(q) < seats {
			return
		}
		group := q[:seats]
		// playerIDs is always seats long: its length tells the room factory
		// the roster size, and a zero entry keeps that seat anonymous.
		pids := make([]uint64, seats)
		for i, e := range group {
			pids[i] = e.PlayerID
		}
		info, err := create(lang, pids)
		if err != nil {
			return
		}
		if len(info.Tokens) < seats || len(info.UserIDs) < seats {
			// The factory returned a smaller roster than requested. Leave the
			// players queued rather than handing out seats that do not exist.
			return
		}
		for i, e := range group {
			e.Status = "matched"
			e.MatchID = info.ID
			e.Seed = info.Seed
			e.Token = info.Tokens[i]
			e.UserID = info.UserIDs[i]
			// Every seat gets the same handle pair: the code identifies the
			// match and the capability authorises reading its outcome, which
			// is shared knowledge between its players by definition.
			e.MatchCode, e.ReadCapability = info.Access.Code, info.Access.ReadCap
		}
		mm.waiting[lang] = q[seats:]
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
