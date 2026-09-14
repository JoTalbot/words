package protocol

import (
	"google.golang.org/protobuf/proto"

	wordarenav1 "github.com/JoTalbot/words/server/gen/wordarena/net/v1"
	"github.com/JoTalbot/words/server/internal/match"
)

// Delta snapshots (M2 batch 32A).
//
// Why this exists: batch 31B removed the per-subscriber *CPU* cost of fan-out
// by encoding each frame once, but it did nothing about the bytes. Every
// subscriber still received a full 60-player snapshot containing 60
// PlayerState and 60 BoardCell entries on every frame, even though a frame
// that is one second apart from the previous one changes only a handful of
// values. Bandwidth, not CPU, is what a Royale lobby actually pays for, and it
// is what kept the mode off the public surface.
//
// The delta is a pure function of two canonical server snapshots. It adds no
// authority anywhere: the server still decides the state, and the client's
// copy is only ever a reconstruction of what the server sent. Nothing here
// reads client input.
//
// Invariant (enforced by TestDeltaReconstructsFullSnapshot and
// TestDeltaStreamReconstructsEveryFrame): for any base and next produced by
// the simulation,
//
//	ApplyDelta(SnapshotToProto(base), DeltaToProto(base, next)) == SnapshotToProto(next)
//
// with proto.Equal - i.e. the receiver ends up holding exactly the frame the
// full-snapshot path would have delivered, including element order.

// DiffSnapshots returns the players and cells of next that differ from base.
//
// Identity is the seat (players) and the cell id (cells). The match never
// removes a player - elimination is a flag on the same seat - so a change set
// that only replaces and appends is sufficient, and a repeated field being
// empty always means "unchanged", never "cleared".
//
// Comparison is on the fields that reach the wire. A field the wire does not
// carry cannot make a frame differ, so ignoring it keeps the change set
// minimal without ever losing information.
func DiffSnapshots(base, next match.Snapshot) (players []match.PlayerView, cells []match.CellView) {
	basePlayers := make(map[match.Seat]match.PlayerView, len(base.Players))
	for _, p := range base.Players {
		basePlayers[p.Seat] = p
	}
	for _, p := range next.Players {
		prev, ok := basePlayers[p.Seat]
		if !ok || playerChanged(prev, p) {
			players = append(players, p)
		}
	}

	baseCells := make(map[int]match.CellView, len(base.Cells))
	for _, c := range base.Cells {
		baseCells[c.ID] = c
	}
	for _, c := range next.Cells {
		prev, ok := baseCells[c.ID]
		if !ok || cellChanged(prev, c) {
			cells = append(cells, c)
		}
	}
	return players, cells
}

func playerChanged(a, b match.PlayerView) bool {
	return a.Score != b.Score ||
		a.RankPosition != b.RankPosition ||
		a.IsEliminated != b.IsEliminated ||
		a.ComboMult != b.ComboMult ||
		// Bot disclosure is wire-visible, so a receiver that is told about it
		// must be able to receive the change. In practice the declaration is
		// fixed at match construction and never flips, which is exactly why
		// leaving it out would have been an invisible correctness hole rather
		// than an optimisation.
		a.IsBot != b.IsBot
}

func cellChanged(a, b match.CellView) bool {
	return a.Letter != b.Letter ||
		a.OwnerSeat != b.OwnerSeat ||
		a.IsLocked != b.IsLocked ||
		a.LockRemainingMs != b.LockRemainingMs
}

// DeltaToProto renders the wire delta from base to next. It is the only
// producer of MatchStateDelta; base must be the frame immediately before next
// in the same room.
func DeltaToProto(base, next match.Snapshot, userIDs []uint64) *wordarenav1.MatchStateDelta {
	changedPlayers, changedCells := DiffSnapshots(base, next)
	out := &wordarenav1.MatchStateDelta{
		MatchId:         next.MatchID,
		ServerTick:      uint32(next.ServerTick),
		RemainingTimeMs: uint32(next.RemainingTimeMs),
		CurrentWave:     uint32(next.CurrentWave),
		StateVersion:    uint32(next.StateVersion),
		BaseVersion:     uint32(base.StateVersion),
		Over:            next.Phase == "over",
	}
	for _, p := range changedPlayers {
		out.Players = append(out.Players, PlayerViewToProto(p, userIDs))
	}
	for _, c := range changedCells {
		out.Cells = append(out.Cells, CellViewToProto(c, userIDs))
	}
	return out
}

// ApplyDelta reconstructs the full snapshot a receiver holds after applying d
// to base. It is the definition of the delta's meaning, and both the server
// test suite and any client implementation are expected to agree with it.
//
// A receiver whose state is not at d.BaseVersion must not call this: the
// delta is meaningless, and the correct reaction is to wait for a full
// snapshot. MatchStateDelta deliberately carries base_version so a client can
// detect that without extra protocol state.
//
// The result is a fresh message; base is not modified. Order is preserved, so
// the reconstruction is proto.Equal - not merely equivalent - to the full
// snapshot of the same frame.
func ApplyDelta(base *wordarenav1.MatchStateSnapshot, d *wordarenav1.MatchStateDelta) *wordarenav1.MatchStateSnapshot {
	out := proto.Clone(base).(*wordarenav1.MatchStateSnapshot)
	out.MatchId = d.MatchId
	out.ServerTick = d.ServerTick
	out.RemainingTimeMs = d.RemainingTimeMs
	out.CurrentWave = d.CurrentWave
	out.StateVersion = d.StateVersion
	out.Over = d.Over

	playerAt := make(map[uint64]int, len(out.Players))
	for i, p := range out.Players {
		playerAt[p.UserId] = i
	}
	for _, p := range d.Players {
		if i, ok := playerAt[p.UserId]; ok {
			out.Players[i] = p
			continue
		}
		playerAt[p.UserId] = len(out.Players)
		out.Players = append(out.Players, p)
	}

	cellAt := make(map[uint32]int, len(out.Cells))
	for i, c := range out.Cells {
		cellAt[c.CellId] = i
	}
	for _, c := range d.Cells {
		if i, ok := cellAt[c.CellId]; ok {
			out.Cells[i] = c
			continue
		}
		cellAt[c.CellId] = len(out.Cells)
		out.Cells = append(out.Cells, c)
	}
	return out
}

// DeltaEnvelope wraps a state delta into a server envelope.
func DeltaEnvelope(matchID uint64, d *wordarenav1.MatchStateDelta) *wordarenav1.ServerEnvelope {
	return &wordarenav1.ServerEnvelope{
		MatchId: matchID,
		Payload: &wordarenav1.ServerEnvelope_SnapshotDelta{SnapshotDelta: d},
	}
}
