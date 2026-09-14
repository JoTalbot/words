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
	if leader-int(m.players[seat].Score) < CatchUpGap {
		return 0
	}
	bonus := points / CatchUpBonusDivisor
	if bonus > CatchUpMaxBonus {
		bonus = CatchUpMaxBonus
	}
	if bonus < 1 {
		return 0
	}
	return bonus
}

// AntiSnowball reports whether the catch-up rule is enabled for this match.
func (m *Match) AntiSnowball() bool { return m.antiSnowball }
