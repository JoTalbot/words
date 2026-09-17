package match

import (
	"fmt"
	"sort"

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
	suddenDeath bool
	// antiSnowball enables the opt-in catch-up rule; catchUp is its resolved
	// constant set (zero config = the 25/2/15 default); see antisnowball.go.
	antiSnowball  bool
	catchUp       CatchUpParams
	inSuddenDeath bool

	cells []Cell
	// Batch 30A (M2 foundation): the roster is variable-length. A 1v1 match
	// is simply len(players) == 2 and every rule below is written in terms
	// of the roster, not a hardcoded pair - which is the precondition for
	// the 60-player Royale mode (docs/ROADMAP.md M2) without a second,
	// divergent simulation.
	players []PlayerState
	log     []Event
	// catchUpSpent is the per-seat catch-up bonus ledger (batch 36A): how
	// many bonus points each seat has absorbed, which is what
	// CatchUpParams.BonusBudget caps. It is DERIVED state, not canonical: it
	// moves only inside applyWord, in lockstep with the bonus that is already
	// recorded on the event (Event.CatchUpBonus) and folded into the score.
	// That is why it is not folded into Fingerprint - a divergent ledger
	// necessarily diverges a score and an event, which the existing
	// fingerprint already catches, while folding it in would move every
	// golden fixture for no new safety.
	catchUpSpent []int
	// boardCells is the roster-derived board size (Q10); fixed at
	// construction so every wave of a match has the same shape.
	boardCells int
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
		ID:           cfg.MatchID,
		Seed:         cfg.Seed,
		Lang:         cfg.Lang,
		dict:         *snap,
		wave:         0,
		phase:        "active",
		suddenDeath:  cfg.SuddenDeath,
		antiSnowball: cfg.AntiSnowball,
		catchUp:      cfg.CatchUp.normalized(),
	}
	seats := cfg.Seats
	if seats == 0 {
		seats = 2 // unchanged default: every existing caller is 1v1
	}
	if seats < MinSeats || seats > MaxSeats {
		return nil, fmt.Errorf("match: seats %d out of range [%d,%d]", seats, MinSeats, MaxSeats)
	}
	m.players = make([]PlayerState, seats)
	m.catchUpSpent = make([]int, seats)
	for i := range m.players {
		m.players[i].Seat = Seat(i)
		// Q5: bot-ness is declared, never inferred. A declaration beyond the
		// roster is ignored rather than panicking, so a caller that supplies a
		// longer slice than it has seats cannot take the service down.
		if i < len(cfg.BotSeats) {
			m.players[i].IsBot = cfg.BotSeats[i]
		}
	}
	m.boardCells = BoardCells(seats)
	m.cells = generateWave(cfg.Seed, cfg.Lang, 0, m.boardCells)
	for i := range m.players {
		m.players[i].EliminatedAtWave = -1
	}
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

// BotSeats returns the seats this match declared to be simulated players, in
// seat order (Q5). It is derived from the canonical player state rather than
// from the config that built it, so it stays correct for a replayed or
// restored match.
func (m *Match) BotSeats() []int {
	var out []int
	for i := range m.players {
		if m.players[i].IsBot {
			out = append(out, i)
		}
	}
	return out
}

// HasBot reports whether any seat of this match is a declared simulated
// player. A match that contains one is not rating- or reward-eligible
// (docs/M2-BOT-POLICY.md).
func (m *Match) HasBot() bool {
	for i := range m.players {
		if m.players[i].IsBot {
			return true
		}
	}
	return false
}

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
	m.cullAtWaveBoundary()
	m.startWave(m.wave + 1)
}

// --- Q9: Royale elimination (docs/PRODUCT-DECISIONS.md) ---

// IsEliminated reports whether a seat has been culled out of the match.
func (m *Match) IsEliminated(seat Seat) bool {
	if seat < 0 || int(seat) >= len(m.players) {
		return false
	}
	return m.players[seat].EliminatedAtWave >= 0
}

// activeCount is how many seats are still playing.
func (m *Match) activeCount() int {
	n := 0
	for i := range m.players {
		if m.players[i].EliminatedAtWave < 0 {
			n++
		}
	}
	return n
}

// survivorTarget is how many seats may continue past a wave boundary: two
// thirds of those still playing, never below MinSurvivors.
func survivorTarget(active int) int {
	n := active * SurvivorsNumerator / SurvivorsDenominator
	if n < MinSurvivors {
		n = MinSurvivors
	}
	if n > active {
		n = active
	}
	return n
}

// cullAtWaveBoundary eliminates the lowest-scoring seats at the end of a wave
// (Q9, "per-wave cull with a survivor share").
//
// Determinism and fairness rules, both of which matter for replay equality:
//   - rosters below EliminationMinSeats never cull, so 1v1 and small lobbies
//     behave exactly as they did in M0/M1;
//   - the cut is by score ascending, ties broken by seat index ascending, so
//     the outcome is a pure function of logged state;
//   - a tie ACROSS the cut line is resolved in the survivors' favour: if
//     keeping everyone level with the last survivor would exceed the target,
//     they all stay rather than being separated by seat number alone. Losing
//     a Royale on your seat index would be indefensible, so the roster
//     shrinks more slowly instead.
func (m *Match) cullAtWaveBoundary() {
	if len(m.players) < EliminationMinSeats {
		return
	}
	active := m.activeCount()
	if active <= MinSurvivors {
		return
	}
	target := survivorTarget(active)
	if target >= active {
		return
	}

	order := make([]Seat, 0, active)
	for i := range m.players {
		if m.players[i].EliminatedAtWave < 0 {
			order = append(order, Seat(i))
		}
	}
	sort.SliceStable(order, func(a, b int) bool {
		pa, pb := &m.players[order[a]], &m.players[order[b]]
		if pa.Score != pb.Score {
			return pa.Score > pb.Score // best first
		}
		return order[a] < order[b]
	})

	// The score of the worst seat that survives on rank alone; everyone
	// level with it is kept too.
	cutoff := m.players[order[target-1]].Score
	culled := false
	for _, seat := range order[target:] {
		if m.players[seat].Score == cutoff {
			continue // tie with the last survivor: keep
		}
		m.players[seat].EliminatedAtWave = m.wave
		culled = true
	}
	if culled {
		m.stateVer++
	}
}

// startSuddenDeath begins the opt-in tiebreak wave (docs/M1-SUDDEN-DEATH.md).
// The board is generated deterministically for wave index WavesPerMatch.
func (m *Match) startSuddenDeath() {
	m.wave++
	m.waveStart = m.tick
	m.cells = generateWave(m.Seed, m.Lang, m.wave, m.boardCells)
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
	m.cells = generateWave(m.Seed, m.Lang, w, m.boardCells)
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
	// An eliminated seat is a spectator: its intents are logged (so the
	// replay still sees them) but can never change competitive state.
	if m.IsEliminated(seat) {
		ev.Result = ResultBlockedByRule
		ev.WordResultString = "blocked_by_rule"
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
	var fresh []int   // free or rival-unlocked cells (scoring cells)
	var stealOf []int // indices among fresh that belong to some rival
	// Batch 31A: "the opponent" is any seat other than this one. The old
	// Seat(1-seat) spelling silently made every seat above 1 unable to steal
	// (and immune to being stolen from) in a roster larger than two.
	blockedLocked := false
	for _, id := range ev.CellIDs {
		c := m.cells[id]
		switch {
		case c.State == CellFree:
			fresh = append(fresh, id)
		case c.Owner == seat:
			// Own cell: contributes letters but never scores again.
		case c.State == CellOwnedLocked:
			blockedLocked = true
		default: // owned by a rival and unlocked -> stealable
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
	// Anti-snowball (M2, opt-in): a seat far behind the leader earns a capped
	// bonus on the word itself. It is added to the SCORE, never to the cell's
	// CreditedValue, so a later steal still debits exactly what the cell was
	// worth to its owner - see internal/match/antisnowball.go.
	if bonus := m.CatchUpBonus(seat, points); bonus > 0 {
		ev.CatchUpBonus = int64(bonus)
		points += bonus
		// The ledger is what makes BonusBudget meaningful, so it moves in the
		// same statement as the score it just clamped - never in the query,
		// which is also called by read-only paths.
		m.recordCatchUpBonus(seat, bonus)
	}
	p.Score += int64(points)
	p.LastAcceptTick = m.tick

	// Apply mutations: fresh claims and steals become locked-owned cells;
	// steals debit the victim's previously credited value.
	isSteal := len(stealOf) > 0
	for _, id := range fresh {
		c := &m.cells[id]
		if c.State != CellFree && c.Owner != seat {
			// Debit the actual victim, whichever seat that is.
			m.players[c.Owner].Score -= int64(c.CreditedValue)
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
		AntiSnowball:    m.antiSnowball,
	}
	s.Players = make([]PlayerView, len(m.players))
	for seat := Seat(0); int(seat) < len(m.players); seat++ {
		p := &m.players[seat]
		s.Players[seat] = PlayerView{
			Seat:         seat,
			Score:        p.Score,
			RankPosition: m.rankOf(seat),
			IsEliminated: p.EliminatedAtWave >= 0,
			Combo:        p.Combo,
			ComboMult:    comboMultOf(p),
			IsBot:        p.IsBot,
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
	// Elimination is competitive state: a replay that culled different
	// seats must not compare equal (Q9).
	for _, p := range m.players {
		h.addI64(int64(p.EliminatedAtWave))
	}
	// Bot disclosure is part of the canonical record (Q5): a replay in which a
	// seat was a bot must not compare equal to one in which it was not, or a
	// silently substituted opponent could replay as the same match.
	for _, p := range m.players {
		if p.IsBot {
			h.addI64(1)
		} else {
			h.addI64(0)
		}
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
