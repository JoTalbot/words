package match

// Anti-snowball: the catch-up mechanic (M2, doc: docs/M2-ANTI-SNOWBALL.md).
//
// The competitive loop rewards whoever claims first: a claim locks its cells,
// a lock denies rivals, and a run of claims compounds through the combo
// multiplier. In a 60-seat Royale that turns into the classic runaway - the
// leader holds the board and the trailing seats cannot get a word in - and the
// M2 roadmap carries "anti-snowball mechanics" as its own row. Product decision
// Q1 requires the score formula to define "catch-up mechanics" and "hard caps
// to prevent runaway scores"; this is the implementation of the first half.
//
// Two properties make this safe to ship as an opt-in rule rather than a change
// to the game:
//
//   - It is OFF by default. Every existing match, replay, golden test, exit-gate
//     baseline and device smoke is unaffected, and the M0/M1 1v1 contract keeps
//     its numbers. Config.AntiSnowball turns it on.
//   - It is a pure function of canonical, logged state (the scores of
//     non-eliminated seats at evaluation time) and integer arithmetic. No
//     wall-clock, no floating point, no client input. Two replays of the same
//     event log produce the same scores, which is what PD-003 requires.
//
// What it deliberately does NOT do: it does not touch cell credit values. A
// bonus is added to the acting seat's score, never folded into CreditedValue,
// because CreditedValue is what a later steal debits. Boosting it would silently
// re-price every steal on the board - a much larger and less predictable change
// than the one being made here.

const (
	// CatchUpGap is how far behind the leader a seat must be before the bonus
	// applies. It is deliberately expressed in points rather than in places,
	// so it cannot depend on roster size.
	CatchUpGap = 25
	// CatchUpBonusDivisor means the bonus is 1/divisor of the word's own
	// points: half again for a seat that is far behind. Integer division on
	// purpose - a competitive score must not depend on floating-point
	// behaviour.
	CatchUpBonusDivisor = 2
	// CatchUpMaxBonus is the hard per-word cap. A long word on a fresh board
	// is worth far more than a short one, and without a cap the mechanic would
	// simply hand the game to whoever is furthest behind the moment a big
	// board opens.
	CatchUpMaxBonus = 15
)

// CatchUpParams holds the catch-up rule's constants for one match. The zero
// value is the batch 32C constant set (25/2/15), so every existing Config,
// replay and baseline keeps its exact behaviour; setting fields is how a
// match opts into different numbers. These are calibration candidates, not a
// protocol (PD-007, docs/M2-ANTI-SNOWBALL.md): the constants are code-level
// until the simulation evidence picks documented provisional values, so they
// are not exposed as caller-controlled request fields.
type CatchUpParams struct {
	// Gap: how far behind the leader a seat must be, in points. Zero means
	// CatchUpGap.
	Gap int
	// Divisor: the bonus is points/Divisor. Zero means CatchUpBonusDivisor.
	Divisor int
	// MaxBonus: the hard per-word cap. Zero means CatchUpMaxBonus.
	MaxBonus int
	// GapPercent scales the trigger with the game's own score scale
	// (batch 35D): a seat is eligible when
	//
	//	leader - score >= max(Gap, leader*GapPercent/100)
	//
	// in integer arithmetic (deterministic, replay-safe). Zero disables the
	// scaled component, so the zero value keeps the exact roster-blind 25
	// point trigger of batches 32C/35A. Why a percentage of the LEADER and
	// not of the roster size: the 35A measurement showed the failure is
	// score-scale blindness (at 60 seats the final gap is ~128 points, so a
	// fixed 25 is a permanent state for most of the field and the rule rides
	// 97-100% of words); the leader's score IS that scale, whatever the
	// roster, the language or the wave shape. The absolute Gap stays as the
	// floor so the early game (leader score 0 or small) keeps the duel's
	// measured-correct behaviour - at two seats the percentage never binds.
	GapPercent int
	// BonusBudget is the VOLUME lever: the maximum total catch-up bonus
	// points ONE seat may receive across a match. Zero means no budget, so
	// the zero value keeps the exact behaviour of 32C/35A/35D.
	//
	// It exists because of what 35D measured. Every threshold family - the
	// absolute gap and the leader-relative percentage - failed to close the
	// gap at 8+ seats, and the failure was not about WHO was selected: the
	// fire rate stayed 100% because with tens of seats somebody is always far
	// behind, and the damage tracked the TOTAL bonus volume monotonically
	// (3840 pts/match at 60 seats opens the final gap 103%; cutting the volume
	// to 2552 cuts the damage to 32%). Selecting better cannot fix that; only
	// bounding how much a seat may absorb can. A budget is also the direct
	// answer to Q1's "hard caps to prevent runaway scores" at the level the
	// per-word MaxBonus cannot reach: MaxBonus bounds one word, the budget
	// bounds a seat.
	//
	// The budget binds as a clamp, not a gate: the word that runs a seat out
	// of budget still earns the remainder of it, so the invariant "a seat's
	// lifetime bonus == min(budget, what it would otherwise have earned)"
	// holds exactly, and a replay can prove why a seat stopped being helped.
	//
	// Sign convention, inherited from MaxBonus below: zero keeps meaning "the
	// default" (no budget), so a nonsensical negative cannot also mean zero -
	// it means "the budget allows nothing", the same exception that lets
	// MaxBonus express "no bonus at all".
	BonusBudget int
}

// DefaultCatchUpParams is the batch 32C constant set, made explicit.
func DefaultCatchUpParams() CatchUpParams {
	return CatchUpParams{Gap: CatchUpGap, Divisor: CatchUpBonusDivisor, MaxBonus: CatchUpMaxBonus}
}

// normalized resolves zero and negative fields to safe values. The zero
// value must stay exactly the 32C set (25/2/15), so zero means the default
// for every field - with one deliberate exception in MaxBonus's sign: a
// NEGATIVE cap means "no bonus" (the rule is on but the cap allows nothing),
// because zero has to keep meaning the default and 0 is not a valid tuning
// point the calibration would want. Clamping instead of erroring keeps
// Config non-fallible, as every existing caller assumes.
func (c CatchUpParams) normalized() CatchUpParams {
	d := DefaultCatchUpParams()
	if c.Gap <= 0 {
		c.Gap = d.Gap
	}
	if c.Divisor <= 0 {
		c.Divisor = d.Divisor
	}
	switch {
	case c.MaxBonus == 0:
		c.MaxBonus = d.MaxBonus
	case c.MaxBonus < 0:
		c.MaxBonus = 0
	}
	if c.GapPercent < 0 {
		c.GapPercent = 0
	}
	// Zero means "no budget" - the legacy unlimited behaviour, which is what
	// the zero value of every existing CatchUpParams must keep. A nonsensical
	// negative therefore cannot fold into zero the way it does for the other
	// fields: it resolves to -1, the budget that awards nothing (mirroring
	// MaxBonus's documented exception, where a negative means "no bonus").
	if c.BonusBudget < 0 {
		c.BonusBudget = -1
	}
	return c
}

// catchUpThreshold is the eligibility gap for the given leader score: the
// absolute floor, or the leader-relative percentage when that is larger.
// Integer math only - a replay must compute the same threshold bit for bit.
func (c CatchUpParams) catchUpThreshold(leader int) int {
	t := c.Gap
	if p := leader * c.GapPercent / 100; p > t {
		t = p
	}
	return t
}

// CatchUpBonus returns the extra points a seat earns for an accepted word
// worth `points`, or zero when the rule does not apply.
//
// The comparison is against the highest score among NON-ELIMINATED seats: an
// eliminated seat is a spectator whose frozen score must not define how far
// behind the living are (Q9, docs/M2-ROYALE-RULES.md).
func (m *Match) CatchUpBonus(seat Seat, points int) int {
	if !m.antiSnowball || points <= 0 {
		return 0
	}
	if seat < 0 || int(seat) >= len(m.players) {
		return 0
	}
	if m.players[seat].EliminatedAtWave >= 0 {
		return 0
	}
	leader := 0
	for s := Seat(0); int(s) < len(m.players); s++ {
		p := &m.players[s]
		if p.EliminatedAtWave >= 0 {
			continue
		}
		if sc := int(p.Score); sc > leader {
			leader = sc
		}
	}
	if leader-int(m.players[seat].Score) < m.catchUp.catchUpThreshold(leader) {
		return 0
	}
	bonus := points / m.catchUp.Divisor
	if bonus > m.catchUp.MaxBonus {
		bonus = m.catchUp.MaxBonus
	}
	if bonus < 1 {
		return 0
	}
	// The volume cap (batch 36A): clamp to what this seat may still receive,
	// and award nothing once a seat has absorbed its whole budget. Reading
	// m.catchUpSpent is safe from a "pure query" standpoint because the
	// accumulator only ever moves in applyWord, immediately after this
	// function returned the very same number - see recordCatchUpBonus.
	switch budget := m.catchUp.BonusBudget; {
	case budget < 0: // the resolved "budget that allows nothing"
		return 0
	case budget > 0:
		if remaining := budget - m.catchUpSpent[seat]; remaining <= 0 {
			return 0
		} else if bonus > remaining {
			bonus = remaining
		}
	}
	return bonus
}

// recordCatchUpBonus folds an awarded bonus into the seat's budget
// consumption. It must be called exactly once per accepted word, with the
// value CatchUpBonus returned, at the point the bonus is applied to the score
// - that is what makes the query and the ledger agree, and what lets a replay
// rebuild the ledger from the event log instead of storing it.
func (m *Match) recordCatchUpBonus(seat Seat, bonus int) {
	if bonus <= 0 || seat < 0 || int(seat) >= len(m.catchUpSpent) {
		return
	}
	m.catchUpSpent[seat] += bonus
}

// CatchUpSpent is the total catch-up bonus a seat has received so far: the
// budget consumption a reader needs to explain why a trailing seat stopped
// being helped. Zero when the rule is off or no budget is configured.
func (m *Match) CatchUpSpent(seat Seat) int {
	if int(seat) < 0 || int(seat) >= len(m.catchUpSpent) {
		return 0
	}
	return m.catchUpSpent[seat]
}

// AntiSnowball reports whether the catch-up rule is enabled for this match.
func (m *Match) AntiSnowball() bool { return m.antiSnowball }
