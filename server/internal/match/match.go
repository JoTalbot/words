package match

import (
	"fmt"

	"github.com/JoTalbot/words/server/internal/dictionary"
	"github.com/JoTalbot/words/server/internal/scoring"
)

// Match is the authoritative 1v1 simulation. Create via New; mutate via
// Submit and AdvanceTicks only.
type Match struct {
	ID   uint64
	Seed uint64
	Lang string
	dict dictionary.Snapshot

	wave      int
	tick      int
	waveStart int
	phase     string // "active" | "sudden_death" | "over"
	stateVer  int
	eventSeq  int

	// suddenDeath is the opt-in flag; inSuddenDeath marks the tiebreak wave.
	suddenDeath   bool
	inSuddenDeath bool

	cells []Cell
	// Batch 30A (M2 foundation): the roster is variable-length. A 1v1 match
	// is simply len(players) == 2 and every rule below is written in terms
	// of the roster, not a hardcoded pair - which is the precondition for
	// the 60-player Royale mode (docs/ROADMAP.md M2) without a second,
	// divergent simulation.
	players []PlayerState
	log     []Event
}

// New creates a fresh match and generates wave 0.
func New(cfg Config) (*Match, error) {
	lang, err := dictionary.ParseLanguage(cfg.Lang)
	if err != nil {
		return nil, err
	}
	snap, err := dictionary.LoadSnapshot(lang)
	if err != nil {
		return nil, fmt.Errorf("match: load dictionary %s: %w", lang, err)
	}
	m := &Match{
		ID:          cfg.MatchID,
		Seed:        cfg.Seed,
		Lang:        cfg.Lang,
		dict:        *snap,
		wave:        0,
		phase:       "active",
		suddenDeath: cfg.SuddenDeath,
	}
	seats := cfg.Seats
	if seats == 0 {
		seats = 2 // unchanged default: every existing caller is 1v1
	}
	if seats < MinSeats || seats > MaxSeats {
		return nil, fmt.Errorf("match: seats %d out of range [%d,%d]", seats, MinSeats, MaxSeats)
	}
	m.players = make([]PlayerState, seats)
	for i := range m.players {
		m.players[i].Seat = Seat(i)
	}
	m.cells = generateWave(cfg.Seed, cfg.Lang, 0)
	return m, nil
}

// NewWithBoard builds a match whose first wave uses the given letters
// (test-only path for deterministic rule tests). Letters must come from the
// language alphabet; length must be CellsPerWave.
func NewWithBoard(cfg Config, letters string) (*Match, error) {
	m, err := New(cfg)
	if err != nil {
		return nil, err
	}
	rs := []rune(letters)
	if len(rs) != CellsPerWave {
		return nil, fmt.Errorf("match: board needs %d letters, got %d", CellsPerWave, len(rs))
	}
	for i, r := range rs {
		if _, err := m.dict.Lang.Normalize(string(r)); err != nil {
			return nil, fmt.Errorf("match: letter %q invalid for %s", r, m.Lang)
		}
		m.cells[i] = Cell{ID: i, Letter: r}
	}
	return m, nil
}

// ---- queries ----

// Tick returns the current simulation tick.
func (m *Match) Tick() int { return m.tick }

// Wave returns the current wave index (0-based).
func (m *Match) Wave() int { return m.wave }

// StateVersion returns the canonical state version.
func (m *Match) StateVersion() int { return m.stateVer }

// IsOver reports whether the match finished.
func (m *Match) IsOver() bool { return m.phase == "over" }

// Score returns the current player score.
func (m *Match) Score(s Seat) int64 { return m.players[s].Score }

// Events returns the full event log (defensive copy not needed for reads;
// callers must not mutate).
func (m *Match) Events() []Event { return m.log }

// Cells exposes the live cells (read-only by convention).
func (m *Match) Cells() []Cell { return m.cells }

// Players exposes live player states (read-only by convention).
func (m *Match) Players() []PlayerState { return m.players }

// Seats returns the roster size of this match (2 for the 1v1 modes).
func (m *Match) Seats() int { return len(m.players) }

// Combo returns current combo count for a seat.
func (m *Match) Combo(s Seat) int { return m.players[s].Combo }

// SuddenDeathEnabled reports whether the match was created with the opt-in
// tiebreak flag (docs/M1-SUDDEN-DEATH.md).
func (m *Match) SuddenDeathEnabled() bool { return m.suddenDeath }

// ---- mutation ----

// AdvanceTicks moves the simulation forward: lock expiry first, then wave
// timeout handling. Deterministic regardless of caller frequency.
func (m *Match) AdvanceTicks(n int) {
	if m.phase == "over" {
		m.tick += n
		return
	}
	for step := 0; step < n; step++ {
		m.tick++
		m.tickLocks()
		if m.tick >= m.waveStart+WaveTicks {
			m.endWaveByTimeout()
			if m.phase == "over" {
				return
			}
		}
	}
}

func (m *Match) tickLocks() {
	for i := range m.cells {
		c := &m.cells[i]
		if c.State == CellOwnedLocked {
			c.LockRemainingTicks--
			if c.LockRemainingTicks <= 0 {
				c.State = CellOwnedUnlocked
				c.LockRemainingTicks = 0
			}
		}
	}
}

// freeCount counts unowned cells of the current wave.
func (m *Match) freeCount() int {
	n := 0
	for _, c := range m.cells {
		if c.State == CellFree {
			n++
		}
	}
	return n
}

// endWaveByTimeout finishes the wave when its time budget is exhausted.
// Unclaimed cells simply expire (score 0); no state carries over.
func (m *Match) endWaveByTimeout() {
	if m.inSuddenDeath {
		// A Sudden Death wave that runs out of time ends as a draw.
		m.phase = "over"
		m.stateVer++
		return
	}
	if m.wave >= WavesPerMatch-1 {
		if m.suddenDeath && m.isTied() {
			m.startSuddenDeath()
			return
		}
		m.phase = "over"
		m.stateVer++
		return
	}
	m.startWave(m.wave + 1)
}

// startSuddenDeath begins the opt-in tiebreak wave (docs/M1-SUDDEN-DEATH.md).
// The board is generated deterministically for wave index WavesPerMatch.
func (m *Match) startSuddenDeath() {
	m.wave++
	m.waveStart = m.tick
	m.cells = generateWave(m.Seed, m.Lang, m.wave)
	m.inSuddenDeath = true
	m.phase = "sudden_death"
	m.stateVer++
}

// isTied reports whether the leading score is shared by more than one seat.
// For a 1v1 match this is exactly the old "both scores equal" rule.
func (m *Match) isTied() bool {
	best := m.players[0].Score
	leaders := 0
	for _, p := range m.players {
		if p.Score > best {
			best = p.Score
		}
	}
	for _, p := range m.players {
		if p.Score == best {
			leaders++
		}
	}
	return leaders > 1
}

// startWave begins wave w with a freshly generated board.
func (m *Match) startWave(w int) {
	m.wave = w
	m.waveStart = m.tick
	m.cells = generateWave(m.Seed, m.Lang, w)
	m.stateVer++
}

// Submit evaluates and applies one word intent from a seat. It always
// appends an event to the log and returns it. All validation is local and
// deterministic; the dictionary is the injected snapshot.
func (m *Match) Submit(seat Seat, cellIDs []int) Event {
	m.eventSeq++
	ev := Event{Seq: m.eventSeq, Tick: m.tick, Seat: seat, CellIDs: copyIDs(cellIDs)}
	ev = m.evaluate(ev)
	m.log = append(m.log, ev)
	return ev
}

func (m *Match) evaluate(ev Event) Event {
	ev.StateVersion = m.stateVer
	seat := ev.Seat
	if m.phase == "over" {
		ev.Result = ResultMatchNotActive
		ev.WordResultString = "match_not_active"
		return ev
	}
	if err := validatePath(m.cells, ev.CellIDs); err != nil {
		ev.Result = ResultInvalidInput
		ev.WordResultString = "invalid_input"
		return ev
	}
	ev.Word = lettersOf(m.cells, ev.CellIDs)
	if !m.dict.ContainsNormalized(ev.Word) {
		ev.Result = ResultRejectedNotInDict
		ev.WordResultString = "rejected_not_in_dict"
		return ev
	}

	// Cell availability analysis (M0-MATCH-RULES §3).
	var fresh []int   // free or opponent-unlocked cells (scoring cells)
	var stealOf []int // indices among fresh that belong to the opponent
	opp := Seat(1 - seat)
	blockedLocked := false
	for _, id := range ev.CellIDs {
		c := m.cells[id]
		switch {
		case c.State == CellFree:
			fresh = append(fresh, id)
		case c.Owner == opp && c.State == CellOwnedLocked:
			blockedLocked = true
		case c.Owner == opp: // unlocked -> stealable
			fresh = append(fresh, id)
			stealOf = append(stealOf, id)
		}
	}
	if blockedLocked || len(fresh) == 0 {
		ev.Result = ResultBlockedByRule
		ev.WordResultString = "blocked_by_rule"
		return ev
	}

	// Combo accounting (M0-MATCH-RULES §4).
	p := &m.players[seat]
	if p.Combo == 0 || m.tick-p.LastAcceptTick > ComboResetWindowTicks {
		p.Combo = 0
	}
	p.Combo++
	mult := scoring.ComboMultiplier(p.Combo)

	// Points: fresh and stolen cells contribute their letter value.
	values := make([]int, 0, len(fresh))
	for _, id := range fresh {
		values = append(values, scoring.LetterValue(dictionary.Language(m.Lang), m.cells[id].Letter))
	}
	points, _ := scoring.WordScore(values, len(ev.CellIDs), p.Combo)
	p.Score += int64(points)
	p.LastAcceptTick = m.tick

	// Apply mutations: fresh claims and steals become locked-owned cells;
	// steals debit the victim's previously credited value.
	isSteal := len(stealOf) > 0
	for _, id := range fresh {
		c := &m.cells[id]
		if isSteal && c.Owner == opp {
			m.players[opp].Score -= int64(c.CreditedValue)
		}
		credited := scoring.CellCreditedValue(scoring.LetterValue(dictionary.Language(m.Lang), c.Letter), mult)
		c.Owner = seat
		c.State = CellOwnedLocked
		c.LockRemainingTicks = LockTicks
		c.CreditedValue = credited
	}
	m.stateVer++

	ev.Result = ResultAccepted
	ev.WordResultString = "accepted"
	ev.ScoreAdded = int64(points)
	ev.TotalScore = m.players[seat].Score
	ev.ComboMult = mult
	ev.IsSteal = isSteal
	ev.StateVersion = m.stateVer

	// Sudden Death: the first accepted word ends the match immediately.
	// Its points are already applied above, so the scorer is ahead.
	if m.inSuddenDeath {
		m.phase = "over"
		m.stateVer++
		return ev
	}

	// Wave completion: all cells owned -> next wave, sudden death, or end.
	if m.freeCount() == 0 {
		if m.wave >= WavesPerMatch-1 {
			if m.suddenDeath && m.isTied() {
				m.startSuddenDeath()
			} else {
				m.phase = "over"
				m.stateVer++
			}
		} else {
			m.startWave(m.wave + 1)
		}
	}
	return ev
}

// ---- snapshots & results ----

// Snapshot renders the canonical state (equivalent to a protocol snapshot).
func (m *Match) Snapshot() Snapshot {
	remaining := (m.waveStart + WaveTicks) - m.tick
	if m.phase == "over" {
		remaining = 0
	}
	if remaining < 0 {
		remaining = 0
	}
	s := Snapshot{
		MatchID:         m.ID,
		Language:        m.Lang,
		Seed:            m.Seed,
		ServerTick:      m.tick,
		RemainingTimeMs: remaining * tickMillis,
		CurrentWave:     m.wave,
		StateVersion:    m.stateVer,
		Phase:           m.phase,
		SuddenDeath:     m.inSuddenDeath,
	}
	s.Players = make([]PlayerView, len(m.players))
	for seat := Seat(0); int(seat) < len(m.players); seat++ {
		p := &m.players[seat]
		s.Players[seat] = PlayerView{
			Seat:         seat,
			Score:        p.Score,
			RankPosition: m.rankOf(seat),
			IsEliminated: false, // M0 has no elimination
			Combo:        p.Combo,
			ComboMult:    comboMultOf(p),
		}
	}
	for _, c := range m.cells {
		cv := CellView{ID: c.ID, Letter: string(c.Letter), OwnerSeat: -1}
		if c.State != CellFree {
			cv.OwnerSeat = int(c.Owner)
			cv.IsLocked = c.State == CellOwnedLocked
			cv.LockRemainingMs = c.LockRemainingTicks * tickMillis
		}
		s.Cells = append(s.Cells, cv)
	}
	return s
}

func comboMultOf(p *PlayerState) float64 {
	if p.Combo == 0 {
		return 1
	}
	// Multiplier stored only for display parity; computed live to stay
	// consistent with the formula.
	return scoring.ComboMultiplier(p.Combo)
}

// rankOf returns the competition rank of a seat: 1 for the highest score,
// and 1 + (number of seats scoring strictly higher) otherwise. Ties share the
// better rank, which reproduces the 1v1 rule exactly (equal scores -> both
// rank 1) while generalizing to any roster size.
func (m *Match) rankOf(seat Seat) int {
	rank := 1
	mine := m.players[seat].Score
	for _, p := range m.players {
		if p.Score > mine {
			rank++
		}
	}
	return rank
}

// Result returns the final outcome once the match is over.
func (m *Match) Result() RankResult {
	if !m.IsOver() {
		return RankResult{Over: false}
	}
	best := m.players[0].Score
	winner := Seat(0)
	for i, p := range m.players {
		if p.Score > best {
			best = p.Score
			winner = Seat(i)
		}
	}
	if m.isTied() {
		return RankResult{IsTie: true, Over: true}
	}
	return RankResult{WinnerSeat: winner, IsTie: false, Over: true}
}

// Fingerprint is a deterministic digest of competitive state for equality
// checks in tests and replay validation.
func (m *Match) Fingerprint() string {
	h := newHash()
	h.addU64(m.ID)
	h.addU64(m.Seed)
	h.addString(m.Lang)
	h.addI64(int64(m.tick))
	h.addI64(int64(m.wave))
	h.addI64(int64(m.stateVer))
	for _, p := range m.players {
		h.addI64(p.Score)
	}
	for _, p := range m.players {
		h.addI64(int64(p.Combo))
	}
	for _, c := range m.cells {
		h.addByte(byte(c.Letter))
		h.addI64(int64(c.State))
		h.addI64(int64(c.Owner))
		h.addI64(int64(c.LockRemainingTicks))
		h.addI64(int64(c.CreditedValue))
	}
	return h.hex()
}
