package match

import "fmt"

// Board-side catch-up levers (batch 36D, docs/M2-ANTI-SNOWBALL.md).
//
// Every rule measured so far pays a seat for what it is about to do, and the
// gap keeps growing with roster size: 35A showed threshold selection is
// duel-locked, 35D showed making selection leader-relative cannot close a gap
// of N seats, and 36A showed that capping the volume of the payout bounds the
// damage monotonically while leaving 0/24 pairs closed at 30 and 60 seats. The
// conclusion recorded there is that the board, not the ledger, is the only
// place left where a point can move instead of being minted.
//
// Two board knobs decide how much a trailing seat can take:
//
//   - LockTicks: how long a claimed cell is safe from a rival. It is the
//     leader's protection, so shortening it hands the trailing seats
//     opportunities on the same board rather than points on the balance sheet.
//   - StealDebitPercent: what share of a stolen cell's CreditedValue is taken
//     back from its previous owner. The shipped rule debits the whole amount,
//     which is why a steal both moves a cell and swings two scores.
//
// These are calibration parameters in exactly the sense 32C established for
// CatchUpParams and 36A for BonusBudget: they exist so a sweep can measure the
// levers and so the evidence stays reproducible. They are deliberately NOT part
// of the create-match request - the server-authoritative rule is that clients
// never choose fairness or physics (docs/ARCHITECTURE.md, PD-007) - and they
// are not persisted, so a replay still derives its behaviour from the match
// configuration it was created with.
type BoardParams struct {
	// LockTicks is how long a claimed or stolen cell stays locked against a
	// rival. Zero means the shipped LockTicks; a negative value is nonsense
	// and normalizes to it. The sweep uses this as a duration, so larger
	// values mean a more protected leader.
	LockTicks int
	// StealDebitPercent is the share of the stolen cell's CreditedValue
	// removed from its previous owner. Zero means 100, the shipped full
	// debit, so "no debit at all" has to be asked for explicitly as a
	// negative value, which clamps to 0 - the same split 36A introduced for
	// BonusBudget, where the zero value is reserved for "legacy".
	StealDebitPercent int
}

// Board parameters are game constants in the same class as LockTicks itself, so
// they are clamped to a bounded range rather than trusted. The ceiling is not a
// fairness judgement: a lock long enough to outlast the board's own waves would
// silently turn claim/steal into claim-only, which is a different game and
// should be proposed as one.
const (
	boardLockTicksMax     = 600 // 20 s at 30 Hz
	boardDebitPercentMax  = 100
	boardDebitPercentFull = 100
)

// DefaultBoardParams is the shipped board: the LockTicks constant and a full
// steal debit. Every existing fixture, replay and baseline behaves this way.
func DefaultBoardParams() BoardParams {
	return BoardParams{LockTicks: LockTicks, StealDebitPercent: boardDebitPercentFull}
}

// normalized is the internal spelling used inside the package.
func (p BoardParams) normalized() BoardParams { return p.Normalized() }

// Normalized returns the effective board: defaults for unset fields, and each
// set field clamped into its legal range. It is exported, unlike CatchUpParams'
// equivalent, so the calibration harness can record the constants a match ACTUALLY
// ran with - a sweep artifact that printed the requested -1 for "no debit" would
// make the evidence unreadable, and the harness must not re-derive the clamp
// itself, which would test a copy of the rule instead of the rule.
func (p BoardParams) Normalized() BoardParams {
	out := p
	if out.LockTicks <= 0 {
		out.LockTicks = LockTicks
	}
	if out.LockTicks > boardLockTicksMax {
		out.LockTicks = boardLockTicksMax
	}
	switch {
	case out.StealDebitPercent < 0:
		out.StealDebitPercent = 0
	case out.StealDebitPercent == 0:
		out.StealDebitPercent = boardDebitPercentFull
	case out.StealDebitPercent > boardDebitPercentMax:
		out.StealDebitPercent = boardDebitPercentMax
	}
	return out
}

func (p BoardParams) String() string {
	return fmt.Sprintf("lock=%d,debit=%d", p.LockTicks, p.StealDebitPercent)
}
