// Package matchroom hosts live authoritative matches behind the transport:
// a 30 Hz tick loop, seat tokens, reconnect-within-grace handling and
// canonical event/snapshot fan-out. It contains no HTTP or WebSocket code —
// transports call its methods and subscribe to its streams.
package matchroom

import (
	"fmt"
	"sync"

	"github.com/JoTalbot/words/server/internal/match"
)

// GraceTicks mirrors the reconnect grace window (M0 rules §7).
const GraceTicks = 300

// Config describes a room to host.
type Config struct {
	MatchID  uint64
	Seed     uint64
	Language string
	UserIDs  [2]uint64
	Token0   string
	Token1   string
}

// Room runs one authoritative match.
type Room struct {
	id    uint64
	match *match.Match
	mu    sync.Mutex

	userIDs [2]uint64
	tokens  map[string]match.Seat

	subs    map[match.Seat]*Subscription
	stopped bool
}

// Subscription delivers canonical state to one connected client.
type Subscription struct {
	Seat match.Seat
	// Events receives word-validation events.
	Events chan EventFrame
	// Snapshots receives 1 Hz canonical snapshots.
	Snapshots chan SnapshotFrame
	closed    chan struct{}
	once      sync.Once
}

// EventFrame is a word-validation event for fan-out.
type EventFrame struct {
	Seat       match.Seat
	Seq        int
	ClientSeq  uint32 // echoed client intent sequence (telemetry/ordering aid)
	Tick       int
	Result     match.WordResult
	Word       string
	ScoreAdded int64
	TotalScore int64
	IsSteal    bool
	StateVer   int
	CellIDs    []int
	ComboMult  float64
}

// SnapshotFrame carries the canonical state at a tick.
type SnapshotFrame struct {
	Snapshot match.Snapshot
}

// New creates a room around a fresh deterministic match.
func New(cfg Config) (*Room, error) {
	m, err := match.New(match.Config{
		MatchID: cfg.MatchID,
		Seed:    cfg.Seed,
		Lang:    cfg.Language,
	})
	if err != nil {
		return nil, err
	}
	r := &Room{
		id:      cfg.MatchID,
		match:   m,
		userIDs: cfg.UserIDs,
		tokens:  map[string]match.Seat{},
		subs:    map[match.Seat]*Subscription{},
	}
	if cfg.Token0 != "" {
		r.tokens[cfg.Token0] = 0
	}
	if cfg.Token1 != "" {
		r.tokens[cfg.Token1] = 1
	}
	return r, nil
}

// Match exposes the underlying simulation (read-only by convention; do not
// call mutating methods without going through the room).
func (r *Room) Match() *match.Match { return r.match }

// Snapshot returns the canonical state under the room lock.
func (r *Room) Snapshot() match.Snapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.match.Snapshot()
}

// IsOver reports match completion under the room lock.
func (r *Room) IsOver() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.match.IsOver()
}

// UserID returns the account id for a seat.
func (r *Room) UserID(s match.Seat) uint64 { return r.userIDs[s] }

// SeatForToken resolves a token to its seat.
func (r *Room) SeatForToken(tok string) (match.Seat, bool) {
	s, ok := r.tokens[tok]
	return s, ok
}

// Subscribe registers a client stream for a seat. One stream per seat.
func (r *Room) Subscribe(seat match.Seat) *Subscription {
	r.mu.Lock()
	defer r.mu.Unlock()
	if s, ok := r.subs[seat]; ok {
		return s
	}
	s := &Subscription{
		Seat:      seat,
		Events:    make(chan EventFrame, 128),
		Snapshots: make(chan SnapshotFrame, 8),
		closed:    make(chan struct{}),
	}
	r.subs[seat] = s
	return s
}

// Unsubscribe detaches a client stream.
func (r *Room) Unsubscribe(seat match.Seat) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if s, ok := r.subs[seat]; ok {
		s.once.Do(func() { close(s.closed) })
		delete(r.subs, seat)
	}
}

// Submit applies one intent under the room lock and fans out the event.
func (r *Room) Submit(seat match.Seat, cellIDs []int) (EventFrame, error) {
	return r.SubmitWithSeq(seat, 0, cellIDs)
}

// SubmitWithSeq is Submit with the client intent sequence echoed back in the
// resulting event frame so clients can correlate intents to outcomes.
func (r *Room) SubmitWithSeq(seat match.Seat, clientSeq uint32, cellIDs []int) (EventFrame, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if seat < 0 || int(seat) > 1 {
		return EventFrame{}, fmt.Errorf("matchroom: invalid seat %d", seat)
	}
	if r.stopped {
		return EventFrame{}, fmt.Errorf("matchroom: room stopped")
	}
	ev := r.match.Submit(seat, cellIDs)
	frame := EventFrame{
		Seat: seat, Seq: ev.Seq, ClientSeq: clientSeq, Tick: ev.Tick,
		Result: ev.Result, Word: ev.Word, ScoreAdded: ev.ScoreAdded,
		TotalScore: ev.TotalScore, IsSteal: ev.IsSteal,
		StateVer: ev.StateVersion, CellIDs: ev.CellIDs, ComboMult: ev.ComboMult,
	}
	r.broadcastEventLocked(frame)
	return frame, nil
}

// Tick advances the simulation by one 30 Hz step and fans out a canonical
// snapshot once per second (30 ticks).
func (r *Room) Tick() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stopped {
		return
	}
	r.match.AdvanceTicks(1)
	if r.match.Tick()%match.TicksPerSecond == 0 {
		snap := r.match.Snapshot()
		r.broadcastSnapshotLocked(snap)
	}
}

// Close stops the room and closes subscriber channels.
func (r *Room) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stopped = true
	for seat, s := range r.subs {
		s.once.Do(func() { close(s.closed) })
		delete(r.subs, seat)
	}
}

func (r *Room) broadcastEventLocked(ev EventFrame) {
	for _, s := range r.subs {
		select {
		case s.Events <- ev:
		default:
			// Slow client: drop the event; periodic canonical snapshots
			// keep it consistent (M0 convergence requirement).
		}
	}
}

func (r *Room) broadcastSnapshotLocked(snap match.Snapshot) {
	for _, s := range r.subs {
		select {
		case s.Snapshots <- SnapshotFrame{Snapshot: snap}:
		default:
		}
	}
}
