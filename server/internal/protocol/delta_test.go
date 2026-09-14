package protocol

import (
	"sort"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	wordarenav1 "github.com/JoTalbot/words/server/gen/wordarena/net/v1"
	"github.com/JoTalbot/words/server/internal/dictionary"
	"github.com/JoTalbot/words/server/internal/match"
	"github.com/JoTalbot/words/server/internal/matchroom"
)

// Batch 32A tests. The delta path is only safe if it is EXACT: a client that
// applies it must end up holding the very frame the full-snapshot path would
// have delivered. These tests therefore compare reconstructions with
// proto.Equal (which is order-sensitive), not field by field, and they run
// over real simulation output driven through a real room rather than over
// hand-written fixtures.

const deltaSeatsBench = 60

// deltaCandidates returns short dictionary words, longest last, so a test can
// claim cells in roughly the way the partner bot does. Nothing here needs to
// be clever: the server validates that a path is a set of in-range, distinct
// cells spelling a dictionary word, so a letter-multiset match is enough.
func deltaCandidates(t *testing.T, lang string) []string {
	t.Helper()
	snap, err := dictionary.LoadSnapshot(dictionary.Language(lang))
	if err != nil {
		t.Fatalf("load %s dictionary: %v", lang, err)
	}
	var out []string
	for _, w := range snap.Words() {
		if n := len([]rune(w)); n >= match.MinWordLength && n <= 5 {
			out = append(out, w)
		}
	}
	sort.Strings(out)
	if len(out) == 0 {
		t.Fatal("no candidate words")
	}
	return out
}

// pickCells spells a candidate word for one seat, returning the cell ids in
// path order, or nil when nothing fits.
//
// "Playable" mirrors the server's own availability rule (M0-MATCH-RULES §3):
// a cell is a scoring cell when it is free or owned by a rival and unlocked.
// Restricting the driver to FREE cells would quietly understate the delta: a
// 60-cell board is claimed within seconds, after which a real Royale lobby
// spends the rest of the match stealing from each other, and steals are
// exactly the events that change cells.
func pickCells(snap match.Snapshot, words []string, seat match.Seat) []int {
	free := make([]match.CellView, 0, len(snap.Cells))
	for _, c := range snap.Cells {
		if c.IsLocked {
			continue
		}
		if c.OwnerSeat < 0 || c.OwnerSeat != int(seat) {
			free = append(free, c)
		}
	}
	if len(free) < match.MinWordLength {
		return nil
	}
	for _, w := range words {
		if len([]rune(w)) > len(free) {
			continue
		}
		used := make(map[int]bool, len(free))
		ids := make([]int, 0, len(w))
		ok := true
		for _, r := range w {
			found := false
			for _, c := range free {
				if used[c.ID] {
					continue
				}
				if strings.EqualFold(c.Letter, string(r)) {
					used[c.ID] = true
					ids = append(ids, c.ID)
					found = true
					break
				}
			}
			if !found {
				ok = false
				break
			}
		}
		if ok {
			return ids
		}
	}
	return nil
}

func seatIDs(n int) []uint64 {
	ids := make([]uint64, n)
	for i := range ids {
		ids[i] = uint64(i + 1)
	}
	return ids
}

func newDeltaRoom(t *testing.T, seats int, seed uint64) *matchroom.Room {
	t.Helper()
	room, err := matchroom.New(matchroom.Config{
		MatchID:     1,
		Seed:        seed,
		Language:    "en",
		SeatUserIDs: seatIDs(seats),
	})
	if err != nil {
		t.Fatalf("new %d-seat room: %v", seats, err)
	}
	return room
}

func marshalFull(t *testing.T, snap match.Snapshot, ids []uint64) ([]byte, *wordarenav1.MatchStateSnapshot) {
	t.Helper()
	pb := SnapshotToProto(snap, ids)
	b, err := proto.Marshal(SnapshotEnvelope(snap.MatchID, pb))
	if err != nil {
		t.Fatalf("marshal full snapshot: %v", err)
	}
	return b, pb
}

// TestDeltaReconstructsFullSnapshot is the single-step invariant: applying the
// delta of a real transition reproduces the next full snapshot exactly.
func TestDeltaReconstructsFullSnapshot(t *testing.T) {
	words := deltaCandidates(t, "en")
	for _, seats := range []int{2, 4, 8, 30, deltaSeatsBench} {
		t.Run(seatsLabel(seats), func(t *testing.T) {
			room := newDeltaRoom(t, seats, 4242)
			ids := room.UserIDs()

			base := room.Snapshot()
			applied := false
			for tick := 0; tick < 600 && !applied; tick++ {
				if tick%45 == 0 {
					seat := match.Seat(tick % seats)
					if cells := pickCells(room.Snapshot(), words, seat); cells != nil {
						if _, err := room.Submit(seat, cells); err != nil {
							t.Fatalf("submit: %v", err)
						}
					}
				}
				for i := 0; i < 30; i++ {
					room.Tick()
				}
				next := room.Snapshot()
				if next.StateVersion == base.StateVersion {
					continue
				}
				d := DeltaToProto(base, next, ids)
				if got, want := d.StateVersion, uint32(next.StateVersion); got != want {
					t.Fatalf("delta state_version %d, want %d", got, want)
				}
				if got, want := d.BaseVersion, uint32(base.StateVersion); got != want {
					t.Fatalf("delta base_version %d, want %d", got, want)
				}
				recon := ApplyDelta(SnapshotToProto(base, ids), d)
				full := SnapshotToProto(next, ids)
				if !proto.Equal(recon, full) {
					t.Fatalf("reconstruction diverged at seats=%d tick=%d\n got: %v\nwant: %v",
						seats, next.ServerTick, recon, full)
				}
				applied = true
			}
			if !applied {
				t.Fatal("no state transition was produced; the test proved nothing")
			}
		})
	}
}

// TestDeltaStreamReconstructsEveryFrame is the invariant that matters in
// production: a connection that stays in sync and applies every consecutive
// delta must hold the canonical state at every frame of a 60-seat match.
//
// It runs two load profiles, because one number would be misleading. A delta
// can only save what does NOT change, so its benefit is a function of how much
// of the board is moving. "saturated" is a lobby playing flat out (every seat
// attempting a word twice a second); "typical" is a lobby mid-wave, where
// players are reading the board between claims. Both are measured and both are
// logged; the assertions are a strict improvement in both, and a strong
// improvement in the profile that a real lobby spends most of its time in.
func TestDeltaStreamReconstructsEveryFrame(t *testing.T) {
	profiles := []struct {
		name     string
		every    int // ticks between submission rounds
		perRound int // seats attempting a word each round
		minRatio float64
	}{
		{"saturated", 15, 20, 1.5},
		{"typical", 30, 4, 2.0},
	}
	for _, prof := range profiles {
		t.Run(prof.name, func(t *testing.T) {
			runDeltaStreamProfile(t, prof.every, prof.perRound, prof.minRatio)
		})
	}
}

func runDeltaStreamProfile(t *testing.T, every, perRound int, minRatio float64) {
	t.Helper()
	const seats = deltaSeatsBench
	words := deltaCandidates(t, "en")
	room := newDeltaRoom(t, seats, 4242)
	ids := room.UserIDs()
	sub := room.Subscribe(0)

	var (
		recon       *wordarenav1.MatchStateSnapshot
		prev        *match.Snapshot
		frames      int
		deltaFrames int
		changedSum  int
		fullBytes   int
		deltaBytes  int
		started     bool
		attempts    int
		accepted    int
		maxPlayers  int
		breakdown   changeBreakdown
	)

	for tick := 0; tick < 12000 && !room.IsOver(); tick++ {
		if every > 0 && tick%every == 0 {
			for k := 0; k < perRound; k++ {
				seat := match.Seat((tick*3 + k*7) % seats)
				if cells := pickCells(room.Snapshot(), words, seat); cells != nil {
					attempts++
					ev, err := room.Submit(seat, cells)
					if err == nil && ev.Result == match.ResultAccepted {
						accepted++
					}
				}
			}
		}
		room.Tick()
		for {
			var sf matchroom.SnapshotFrame
			select {
			case sf = <-sub.Snapshots:
			default:
				goto drained
			}
			frames++
			if n := len(sf.Snapshot.Players); n > maxPlayers {
				maxPlayers = n
			}
			fullB, fullPB := marshalFull(t, sf.Snapshot, ids)
			fullBytes += len(fullB)
			if !started {
				// The first frame is the connection's anchor, which the
				// transport always sends in full.
				recon = fullPB
				started = true
			} else {
				d := DeltaToProto(*prev, sf.Snapshot, ids)
				recon = ApplyDelta(recon, d)
				if !proto.Equal(recon, fullPB) {
					t.Fatalf("frame %d (tick %d) diverged\n got: %v\nwant: %v",
						frames, sf.Snapshot.ServerTick, recon, fullPB)
				}
				b, err := proto.Marshal(DeltaEnvelope(sf.Snapshot.MatchID, d))
				if err != nil {
					t.Fatalf("marshal delta: %v", err)
				}
				deltaBytes += len(b)
				deltaFrames++
				changedSum += len(d.Players) + len(d.Cells)
				cb := classify(*prev, sf.Snapshot)
				breakdown.cellOwner += cb.cellOwner
				breakdown.cellLockState += cb.cellLockState
				breakdown.cellLockOnly += cb.cellLockOnly
				breakdown.cellLetterOnly += cb.cellLetterOnly
				breakdown.playerScore += cb.playerScore
				breakdown.playerRank += cb.playerRank
				breakdown.playerRankOnly += cb.playerRankOnly
				breakdown.playerCombo += cb.playerCombo
				breakdown.playerElim += cb.playerElim
			}
			held := sf.Snapshot
			prev = &held
		}
	drained:
	}

	if frames < 20 {
		t.Fatalf("only %d frames observed; the match ended early", frames)
	}
	if deltaFrames == 0 {
		t.Fatal("no delta frame was produced")
	}
	if changedSum == 0 {
		t.Fatal("no frame ever changed anything; the byte comparison would be meaningless")
	}
	// A measurement of a match in which almost nothing happened would flatter
	// the delta path, so the load is checked, not assumed: the roster must be
	// full and the driven play must actually be scoring.
	if maxPlayers != seats {
		t.Fatalf("widest frame carried %d players, want %d: the byte comparison is not at full roster", maxPlayers, seats)
	}
	if accepted < 20 {
		t.Fatalf("only %d submissions were accepted out of %d attempts; the match barely played, "+
			"so the byte comparison would be meaningless", accepted, attempts)
	}

	avgFull := fullBytes / frames
	avgDelta := deltaBytes / deltaFrames
	avgChanged := changedSum / deltaFrames
	ratio := float64(avgFull) / float64(avgDelta)
	t.Logf("%d seats, %d frames (%d delta), %d/%d submissions accepted: full %d B/frame, "+
		"delta %d B/frame (%d entries/frame, %.1fx smaller, %.2f KB/s per client at 1 Hz)",
		seats, frames, deltaFrames, accepted, attempts, avgFull, avgDelta, avgChanged,
		ratio, float64(avgDelta)/1024)
	t.Logf("churn over the match: %s", breakdown)

	if ratio < minRatio {
		t.Fatalf("deltas are only %.2fx smaller than full snapshots (full %d B/frame, delta %d B/frame); "+
			"want at least %.2fx on this profile", ratio, avgFull, avgDelta, minRatio)
	}
	// The absolute budget is the number that actually matters: bytes do not
	// depend on host speed, so this is a real capacity statement rather than a
	// ratio that a slower machine would flatter. A full 60-seat frame measures
	// ~1.3-1.5 KB, so a revert to full-snapshot fan-out fails this as well as
	// the ratio check.
	const deltaBudgetBytesPerFrame = 1200
	if avgDelta > deltaBudgetBytesPerFrame {
		t.Fatalf("delta budget exceeded: %d B/frame at %d seats, budget is %d",
			avgDelta, seats, deltaBudgetBytesPerFrame)
	}
}

// TestDeltaIgnoresFieldsTheWireCannotCarry pins the reason the change set is
// computed on wire-visible fields only: a field the schema does not carry
// cannot make two frames differ for a client, so including it would only add
// bytes. Sudden Death is the live example - it exists in the domain and has no
// representation in the schema today.
func TestDeltaIgnoresFieldsTheWireCannotCarry(t *testing.T) {
	room := newDeltaRoom(t, 4, 7)
	ids := room.UserIDs()
	base := room.Snapshot()

	next := base
	next.Language = "ru"
	next.Seed = base.Seed + 1
	next.Phase = "sudden_death"
	next.SuddenDeath = !base.SuddenDeath
	next.ServerTick = base.ServerTick + 30

	players, cells := DiffSnapshots(base, next)
	if len(players) != 0 || len(cells) != 0 {
		t.Fatalf("off-wire fields produced a change set: %d players, %d cells", len(players), len(cells))
	}
	// The reconstruction is still exact, because neither snapshot's wire form
	// depends on those fields.
	d := DeltaToProto(base, next, ids)
	if got, want := ApplyDelta(SnapshotToProto(base, ids), d), SnapshotToProto(next, ids); !proto.Equal(got, want) {
		t.Fatalf("wire-identical frames did not reconstruct equal: %v vs %v", got, want)
	}
}

// TestDeltaDetectsEveryVisibleChange makes sure the change set is not
// accidentally too small: each field that IS on the wire must show up.
func TestDeltaDetectsEveryVisibleChange(t *testing.T) {
	room := newDeltaRoom(t, 4, 7)
	base := room.Snapshot()
	if len(base.Players) == 0 || len(base.Cells) == 0 {
		t.Fatal("fixture has no players or cells")
	}

	cases := []struct {
		name  string
		mut   func(s *match.Snapshot)
		wantP int
		wantC int
	}{
		{"score", func(s *match.Snapshot) { s.Players[0].Score++ }, 1, 0},
		{"rank", func(s *match.Snapshot) { s.Players[0].RankPosition++ }, 1, 0},
		{"eliminated", func(s *match.Snapshot) { s.Players[0].IsEliminated = !s.Players[0].IsEliminated }, 1, 0},
		{"combo multiplier", func(s *match.Snapshot) { s.Players[0].ComboMult += 0.5 }, 1, 0},
		{"cell letter", func(s *match.Snapshot) { s.Cells[0].Letter = "z" }, 0, 1},
		{"cell owner", func(s *match.Snapshot) { s.Cells[0].OwnerSeat = 3 }, 0, 1},
		{"cell lock", func(s *match.Snapshot) { s.Cells[0].IsLocked = !s.Cells[0].IsLocked }, 0, 1},
		{"cell lock remaining", func(s *match.Snapshot) { s.Cells[0].LockRemainingMs++ }, 0, 1},
		// Q5 bot disclosure is wire-visible, so a change to it must reach a
		// receiver. It is fixed at match construction in practice, which is
		// exactly why an omission here would be invisible rather than loud.
		{"bot disclosure", func(s *match.Snapshot) { s.Players[0].IsBot = !s.Players[0].IsBot }, 1, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			next := base
			next.Players = append([]match.PlayerView(nil), base.Players...)
			next.Cells = append([]match.CellView(nil), base.Cells...)
			tc.mut(&next)
			players, cells := DiffSnapshots(base, next)
			if len(players) != tc.wantP || len(cells) != tc.wantC {
				t.Fatalf("%s: change set = %d players / %d cells, want %d / %d",
					tc.name, len(players), len(cells), tc.wantP, tc.wantC)
			}
		})
	}
}

// TestApplyDeltaJoinsUnknownEntities covers the two ways a receiver can meet
// an entity it has never seen: a late seat appearing in the roster view and a
// cell that was not in its base.
func TestApplyDeltaJoinsUnknownEntities(t *testing.T) {
	base := &wordarenav1.MatchStateSnapshot{
		MatchId:      9,
		StateVersion: 3,
		Players:      []*wordarenav1.PlayerState{{UserId: 1, Score: 4}},
		Cells:        []*wordarenav1.BoardCell{{CellId: 0, Letter: "a"}},
	}
	d := &wordarenav1.MatchStateDelta{
		MatchId:      9,
		StateVersion: 4,
		BaseVersion:  3,
		Players:      []*wordarenav1.PlayerState{{UserId: 2, Score: 7}},
		Cells:        []*wordarenav1.BoardCell{{CellId: 1, Letter: "b"}},
	}
	got := ApplyDelta(base, d)

	if len(got.Players) != 2 || got.Players[0].UserId != 1 || got.Players[1].UserId != 2 {
		t.Fatalf("players after apply = %v; expected the base row first and the new row appended", got.Players)
	}
	if len(got.Cells) != 2 || got.Cells[0].CellId != 0 || got.Cells[1].CellId != 1 {
		t.Fatalf("cells after apply = %v; expected base order preserved with the new cell appended", got.Cells)
	}
	if got.StateVersion != 4 {
		t.Fatalf("state version %d, want 4", got.StateVersion)
	}
	// The receiver's state is the base, so ApplyDelta must not have touched it.
	if len(base.Players) != 1 || len(base.Cells) != 1 || base.StateVersion != 3 {
		t.Fatalf("ApplyDelta mutated its base: %v", base)
	}
}

// TestDeltaEnvelopeRoundTrip keeps the wire shape of the new message honest:
// it must survive marshal/unmarshal as a snapshot_delta payload, distinct from
// the full-snapshot arm of the oneof.
func TestDeltaEnvelopeRoundTrip(t *testing.T) {
	d := &wordarenav1.MatchStateDelta{
		MatchId:      11,
		ServerTick:   900,
		StateVersion: 42,
		BaseVersion:  41,
		Players:      []*wordarenav1.PlayerState{{UserId: 2, Score: 15, RankPosition: 1}},
		Cells:        []*wordarenav1.BoardCell{{CellId: 5, Letter: "e", OwnerUserId: 2}},
		Over:         true,
	}
	b, err := proto.Marshal(DeltaEnvelope(11, d))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got wordarenav1.ServerEnvelope
	if err := proto.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.GetSnapshot() != nil {
		t.Fatal("a delta was decoded as a full snapshot")
	}
	if got.GetSnapshotDelta() == nil {
		t.Fatal("delta payload missing after round-trip")
	}
	if !proto.Equal(got.GetSnapshotDelta(), d) {
		t.Fatalf("delta round-trip changed the message:\n got %v\nwant %v", got.GetSnapshotDelta(), d)
	}
}

// changeBreakdown attributes a delta's entries to the field that caused them.
// It exists because "the delta is N bytes" is not actionable on its own: the
// interesting question is which channel of state churn is paying for it.
type changeBreakdown struct {
	cellOwner      int
	cellLockState  int
	cellLockOnly   int
	cellLetterOnly int
	playerScore    int
	playerRank     int
	playerCombo    int
	playerElim     int
	playerRankOnly int
}

func (b changeBreakdown) String() string {
	return "cells[owner=" + itoa(b.cellOwner) +
		" lock-state=" + itoa(b.cellLockState) +
		" lock-countdown-only=" + itoa(b.cellLockOnly) +
		" letter=" + itoa(b.cellLetterOnly) + "] players[score=" + itoa(b.playerScore) +
		" rank=" + itoa(b.playerRank) + " rank-only=" + itoa(b.playerRankOnly) +
		" combo=" + itoa(b.playerCombo) + " elim=" + itoa(b.playerElim) + "]"
}

func classify(base, next match.Snapshot) changeBreakdown {
	var b changeBreakdown
	prevP := map[match.Seat]match.PlayerView{}
	for _, p := range base.Players {
		prevP[p.Seat] = p
	}
	for _, p := range next.Players {
		old, ok := prevP[p.Seat]
		if !ok {
			b.playerScore++
			continue
		}
		score, rank := old.Score != p.Score, old.RankPosition != p.RankPosition
		combo, elim := old.ComboMult != p.ComboMult, old.IsEliminated != p.IsEliminated
		switch {
		case score:
			b.playerScore++
		case rank:
			b.playerRank++
			if !combo && !elim {
				b.playerRankOnly++
			}
		case combo:
			b.playerCombo++
		case elim:
			b.playerElim++
		}
	}
	prevC := map[int]match.CellView{}
	for _, c := range base.Cells {
		prevC[c.ID] = c
	}
	for _, c := range next.Cells {
		old, ok := prevC[c.ID]
		if !ok {
			b.cellOwner++
			continue
		}
		switch {
		case old.OwnerSeat != c.OwnerSeat:
			b.cellOwner++
		case old.IsLocked != c.IsLocked:
			b.cellLockState++
		case old.Letter != c.Letter:
			b.cellLetterOnly++
		case old.LockRemainingMs != c.LockRemainingMs:
			b.cellLockOnly++
		}
	}
	return b
}

func seatsLabel(n int) string {
	if n == 2 {
		return "seats_2"
	}
	return string("seats_") + itoa(n)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [8]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
