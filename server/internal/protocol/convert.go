// Package protocol converts between the deterministic match domain and the
// versioned wire schema (proto/wordarena/v1). Conversion is lossless for
// the fields M0 clients need; nothing competitive is invented here.
package protocol

import (
	"fmt"

	wordarenav1 "github.com/JoTalbot/words/server/gen/wordarena/net/v1"
	"github.com/JoTalbot/words/server/internal/match"
)

// resultToProto maps a domain word result to the wire enum.
func resultToProto(r match.WordResult) wordarenav1.WordResult {
	switch r {
	case match.ResultAccepted:
		return wordarenav1.WordResult_ACCEPTED
	case match.ResultRejectedNotInDict:
		return wordarenav1.WordResult_REJECTED_NOT_IN_DICT
	case match.ResultBlockedByRule:
		return wordarenav1.WordResult_BLOCKED_BY_RULE
	case match.ResultInvalidInput:
		return wordarenav1.WordResult_INVALID_INPUT
	case match.ResultMatchNotActive:
		return wordarenav1.WordResult_MATCH_NOT_ACTIVE
	default:
		return wordarenav1.WordResult_WORD_RESULT_UNSPECIFIED
	}
}

// resultFromProto maps a wire enum to the domain result.
func resultFromProto(r wordarenav1.WordResult) (match.WordResult, error) {
	switch r {
	case wordarenav1.WordResult_ACCEPTED:
		return match.ResultAccepted, nil
	case wordarenav1.WordResult_REJECTED_NOT_IN_DICT:
		return match.ResultRejectedNotInDict, nil
	case wordarenav1.WordResult_BLOCKED_BY_RULE:
		return match.ResultBlockedByRule, nil
	case wordarenav1.WordResult_INVALID_INPUT:
		return match.ResultInvalidInput, nil
	case wordarenav1.WordResult_MATCH_NOT_ACTIVE:
		return match.ResultMatchNotActive, nil
	default:
		return 0, fmt.Errorf("protocol: unmapped word result %v", r)
	}
}

// WordEventToProto renders a domain event as the wire event. userID is the
// account id of the acting seat.
func WordEventToProto(e match.Event, userID uint64) *wordarenav1.WordValidatedEvent {
	return &wordarenav1.WordValidatedEvent{
		EventId:          uint64(e.Seq),
		ServerTick:       uint32(e.Tick),
		ClientSequence:   0, // set by the session layer when known
		Result:           resultToProto(e.Result),
		NormalizedWord:   e.Word,
		ScoreAdded:       uint32(max64(0, e.ScoreAdded)),
		TotalScore:       uint32(max64(0, e.TotalScore)),
		ComboMultiplier:  float32(e.ComboMult),
		ClaimedCellIndex: primaryCell(e),
		IsSteal:          e.IsSteal,
		StateVersion:     uint32(e.StateVersion),
		UserId:           userID,
	}
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

// primaryCell is the first word cell for UI affordances.
func primaryCell(e match.Event) uint32 {
	if len(e.CellIDs) == 0 {
		return 0
	}
	return uint32(e.CellIDs[0])
}

// SnapshotToProto renders the canonical snapshot for the wire. userIDs maps
// match seats (0,1) to account ids; the zero value falls back to seat+1.
func SnapshotToProto(s match.Snapshot, userIDs [2]uint64) *wordarenav1.MatchStateSnapshot {
	out := &wordarenav1.MatchStateSnapshot{
		MatchId:         s.MatchID,
		ServerTick:      uint32(s.ServerTick),
		RemainingTimeMs: uint32(s.RemainingTimeMs),
		CurrentWave:     uint32(s.CurrentWave),
		StateVersion:    uint32(s.StateVersion),
		Over:            s.Phase == "over",
	}
	for _, p := range s.Players {
		uid := userIDs[p.Seat]
		if uid == 0 {
			uid = uint64(p.Seat) + 1
		}
		out.Players = append(out.Players, &wordarenav1.PlayerState{
			UserId:          uid,
			Score:           uint32(max64(0, p.Score)),
			RankPosition:    uint32(p.RankPosition),
			IsEliminated:    p.IsEliminated,
			ComboMultiplier: float32(p.ComboMult),
		})
	}
	for _, c := range s.Cells {
		owner := uint64(0)
		if c.OwnerSeat >= 0 {
			uid := userIDs[c.OwnerSeat]
			if uid == 0 {
				uid = uint64(c.OwnerSeat) + 1
			}
			owner = uid
		}
		out.Cells = append(out.Cells, &wordarenav1.BoardCell{
			CellId:          uint32(c.ID),
			Letter:          c.Letter,
			OwnerUserId:     owner,
			IsLocked:        c.IsLocked,
			LockRemainingMs: uint32(c.LockRemainingMs),
		})
	}
	return out
}

// EnvelopeToServerEvent maps a ClientEnvelope word submission to a seat
// intent. Only submit_word is meaningful for M0; resume is handled by the
// session layer.
func EnvelopeToIntent(env *wordarenav1.ClientEnvelope, seat match.Seat) ([]int, error) {
	sw := env.GetSubmitWord()
	if sw == nil {
		return nil, fmt.Errorf("protocol: envelope has no submit_word")
	}
	ids := make([]int, 0, len(sw.LetterIndices))
	for _, v := range sw.LetterIndices {
		if v > 1<<31 {
			return nil, fmt.Errorf("protocol: cell index out of range")
		}
		ids = append(ids, int(v))
	}
	return ids, nil
}

// WordEnvelope wraps a word event into a server envelope.
func WordEnvelope(matchID uint64, ev *wordarenav1.WordValidatedEvent) *wordarenav1.ServerEnvelope {
	return &wordarenav1.ServerEnvelope{
		MatchId: matchID,
		Payload: &wordarenav1.ServerEnvelope_WordEvent{WordEvent: ev},
	}
}

// SnapshotEnvelope wraps a snapshot into a server envelope.
func SnapshotEnvelope(matchID uint64, s *wordarenav1.MatchStateSnapshot) *wordarenav1.ServerEnvelope {
	return &wordarenav1.ServerEnvelope{
		MatchId: matchID,
		Payload: &wordarenav1.ServerEnvelope_Snapshot{Snapshot: s},
	}
}
