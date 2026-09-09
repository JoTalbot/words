package main

// Exit-gate automation (docs/M0.md, M0 exit criteria 1/2/5 and the exit
// gate: "two remote clients can complete repeated matches with identical
// final state and reproducible event logs").
//
// Strategy:
//  1. Offline, an intent script is solved for a deterministic seed by
//     replaying the whole match in-process against the real simulation
//     (server/internal/match) and the real dictionary snapshot. The solver
//     only ever claims free cells, so the script is valid at any real-time
//     speed.
//  2. Live, two WebSocket clients replay that exact intent script against
//     the transport. Every intent outcome is asserted on BOTH clients and
//     must be identical (same echo, same word, same result, same scores).
//  3. Terminal assertion: both clients receive the canonical over=true
//     snapshot and final scores must equal the offline replay scores —
//     proving that the same event log produces the same final result and
//     that clients converge on identical final state.

import (
	"net/http/httptest"
	"testing"
	"time"

	wordarenav1 "github.com/JoTalbot/words/server/gen/wordarena/net/v1"
	"github.com/JoTalbot/words/server/internal/dictionary"
	"github.com/JoTalbot/words/server/internal/match"
	"google.golang.org/protobuf/proto"
)

// egSeeds are deterministic english seeds whose three waves are fully
// claimable by the greedy solver (validated at test start). 1512 is the
// canonical M0 fixture seed (tests/fixtures/replay-m0-seed-1512.md).
var egSeeds = []uint64{1512, 1513, 1517}

type egMove struct {
	wave int
	seat match.Seat
	ids  []uint32
	word string
	seq  uint32 // client sequence within the seat (1-based)
}

// egSolve replays a full match in-process and returns the deterministic
// intent script plus the resulting final scores. If any wave cannot be
// fully claimed with dictionary words of length 3..8 the seed is reported
// as unsolvable (nil).
func egSolve(t *testing.T, lang string, seed uint64) (moves []egMove, want [2]int64, ok bool) {
	t.Helper()
	l, err := dictionary.ParseLanguage(lang)
	if err != nil {
		t.Fatalf("language: %v", err)
	}
	m, err := match.New(match.Config{MatchID: 1, Seed: seed, Lang: lang})
	if err != nil {
		t.Fatalf("match.New(seed %d): %v", seed, err)
	}
	snap, err := dictionary.LoadSnapshot(l)
	if err != nil {
		t.Fatalf("dictionary load: %v", err)
	}
	seatSeq := [2]uint32{}
	guard := 0
	for !m.IsOver() {
		if guard > 64 {
			return nil, [2]int64{}, false
		}
		cells := m.Cells()
		wave := m.Wave()
		claims := egCoverWave(snap, cells)
		if claims == nil {
			return nil, [2]int64{}, false // wave cannot be fully claimed
		}
		for _, path := range claims {
			seat := match.Seat(guard % 2)
			ev := m.Submit(seat, path)
			if ev.Result != match.ResultAccepted {
				return nil, [2]int64{}, false
			}
			seatSeq[seat]++
			ids := make([]uint32, len(path))
			for i, id := range path {
				ids[i] = uint32(id)
			}
			moves = append(moves, egMove{wave: wave, seat: seat, ids: ids, word: ev.Word, seq: seatSeq[seat]})
			guard++
		}
	}
	return moves, [2]int64{m.Score(0), m.Score(1)}, true
}

// egCoverWave computes an exact cover of every free cell with dictionary
// words (length 3..8): a deterministic backtracking search over sorted
// dictionary words whose letter multiset fits the remaining free cells.
// It returns the claim cell paths in submission order, or nil when no cover
// exists within the node budget.
func egCoverWave(snap *dictionary.Snapshot, cells []match.Cell) [][]int {
	rem := make([]int, 0, len(cells))
	for i := range cells {
		if cells[i].State == match.CellFree {
			rem = append(rem, i)
		}
	}
	words := snap.Words() // sorted normalized word list
	wb := map[int][]string{}
	for _, w := range words {
		l := len([]rune(w))
		if l >= 3 && l <= 8 {
			wb[l] = append(wb[l], w)
		}
	}
	c := &egCoverCtx{cells: cells, wb: wb, budget: 200000}
	pathSets := c.search(rem)
	return pathSets
}

type egCoverCtx struct {
	cells  []match.Cell
	wb     map[int][]string
	budget int
}

func (c *egCoverCtx) search(rem []int) [][]int {
	if c.budget <= 0 {
		return nil
	}
	c.budget--
	if len(rem) == 0 {
		return [][]int{}
	}
	counts := map[rune]int{}
	for _, id := range rem {
		counts[c.cells[id].Letter]++
	}
	maxLen := len(rem)
	if maxLen > 8 {
		maxLen = 8
	}
	for l := 3; l <= maxLen; l++ {
		for _, w := range c.wb[l] {
			ids := c.assign(rem, counts, w)
			if ids == nil {
				continue
			}
			rest := egRemainder(rem, ids)
			if sub := c.search(rest); sub != nil {
				return append([][]int{ids}, sub...)
			}
			// deterministic fallthrough: try the next word
		}
	}
	return nil
}

// assign maps word letters to distinct free cells (first-fit, left to
// right). Returns the cell path in word order, or nil if the multiset does
// not fit rem.
func (c *egCoverCtx) assign(rem []int, counts map[rune]int, w string) []int {
	// multiset fit check against counts
	tmp := map[rune]int{}
	for k, v := range counts {
		tmp[k] = v
	}
	for _, r := range w {
		tmp[r]--
		if tmp[r] < 0 {
			return nil
		}
	}
	// first-fit cell assignment in word order
	used := map[int]bool{}
	ids := make([]int, 0, len(w))
	for _, r := range w {
		found := -1
		for _, id := range rem {
			if !used[id] && c.cells[id].Letter == r {
				found = id
				break
			}
		}
		if found < 0 {
			return nil
		}
		used[found] = true
		ids = append(ids, found)
	}
	return ids
}

// egRemainder returns rem without the given cell ids, preserving order.
func egRemainder(rem, ids []int) []int {
	drop := map[int]bool{}
	for _, id := range ids {
		drop[id] = true
	}
	out := make([]int, 0, len(rem)-len(ids))
	for _, id := range rem {
		if !drop[id] {
			out = append(out, id)
		}
	}
	return out
}

// egReadEventFor reads frames until the WordValidatedEvent echoing the given
// intent (seat's user + client sequence) arrives.
func egReadEventFor(t *testing.T, c *testClient, user uint64, wantSeq uint32) *wordarenav1.WordValidatedEvent {
	t.Helper()
	for {
		_, payload := c.readEnvelope(t)
		ev, ok := payload.(*wordarenav1.WordValidatedEvent)
		if !ok {
			continue
		}
		if ev.UserId == user && ev.ClientSequence == wantSeq {
			return ev
		}
	}
}

// egReadUntilWave reads frames until a snapshot for wave w arrives.
func egReadUntilWave(t *testing.T, c *testClient, w uint32) *wordarenav1.MatchStateSnapshot {
	t.Helper()
	for {
		_, payload := c.readEnvelope(t)
		snap, ok := payload.(*wordarenav1.MatchStateSnapshot)
		if !ok {
			continue
		}
		if snap.CurrentWave >= w {
			return snap
		}
	}
}

// egReadUntilOver reads frames until the terminal over=true snapshot.
func egReadUntilOver(t *testing.T, c *testClient) *wordarenav1.MatchStateSnapshot {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		_, payload := c.readEnvelope(t)
		snap, ok := payload.(*wordarenav1.MatchStateSnapshot)
		if !ok {
			continue
		}
		if snap.Over {
			return snap
		}
	}
	t.Fatal("terminal over snapshot not received within 20 s")
	return nil
}

// egPlayOne runs one full scripted match over the real transport and returns
// nothing on success (all assertions inside).
func egPlayOne(t *testing.T, srv *httptest.Server, lang string, seed uint64, round int) {
	t.Helper()
	moves, want, solvable := egSolve(t, lang, seed)
	if !solvable {
		t.Fatalf("seed %d (round %d): not fully claimable — fix egSeeds", seed, round)
	}
	id, tokens, userIDs := createMatch(t, srv, lang, &seed)
	c0 := dial(t, srv, id, tokens[0])
	defer c0.close()
	c1 := dial(t, srv, id, tokens[1])
	defer c1.close()
	c0.seat, c0.user = 0, userIDs[0]
	c1.seat, c1.user = 1, userIDs[1]
	bots := []*testClient{c0, c1}
	users := [2]uint64{userIDs[0], userIDs[1]}

	// 1. Both clients anchor on the same deterministic board (acceptance 1).
	s0 := c0.readSnapshot(t)
	s1 := c1.readSnapshot(t)
	if len(s0.Cells) != match.CellsPerWave || !proto.Equal(s0, s1) {
		t.Fatalf("seed %d round %d: initial snapshots diverge", seed, round)
	}

	lastWave := -1
	for _, mv := range moves {
		if mv.wave != lastWave {
			// Board changed: wait until the live board is the scripted wave.
			egReadUntilWave(t, c0, uint32(mv.wave))
			lastWave = mv.wave
		}
		b := bots[mv.seat]
		u := users[mv.seat]
		b.submit(t, id, mv.ids, mv.seq)
		ev0 := egReadEventFor(t, c0, u, mv.seq)
		ev1 := egReadEventFor(t, c1, u, mv.seq)
		if ev0.Result != wordarenav1.WordResult_ACCEPTED || ev1.Result != wordarenav1.WordResult_ACCEPTED {
			t.Fatalf("seed %d mv %q seat %d: results %v / %v", seed, mv.word, mv.seat, ev0.Result, ev1.Result)
		}
		if ev0.NormalizedWord != mv.word || ev1.NormalizedWord != mv.word {
			t.Fatalf("seed %d: word echo %q / %q, want %q", seed, ev0.NormalizedWord, ev1.NormalizedWord, mv.word)
		}
		if !proto.Equal(ev0, ev1) {
			t.Fatalf("seed %d: clients observed different events for %q: %v vs %v", seed, mv.word, ev0, ev1)
		}
	}

	// 2. Terminal canonical snapshot on both clients (acceptance 2/5).
	f0 := egReadUntilOver(t, c0)
	f1 := egReadUntilOver(t, c1)
	if !proto.Equal(f0, f1) {
		t.Fatalf("seed %d round %d: terminal snapshots diverge", seed, round)
	}
	if !f0.Over {
		t.Fatalf("seed %d round %d: terminal snapshot not flagged over", seed, round)
	}
	for seat := 0; seat < 2; seat++ {
		if got := int64(f0.Players[seat].Score); got != want[seat] {
			t.Fatalf("seed %d round %d: seat %d final score %d, offline replay wants %d (event log not reproducible)", seed, round, seat, got, want[seat])
		}
	}
	t.Logf("seed %d round %d: ok — %d intents, final %d:%d, terminal snapshot identical on both clients", seed, round, len(moves), want[0], want[1])
}

// TestExitGateRepeatedFullMatches runs repeated complete 1v1 matches over the
// real WebSocket transport and proves: deterministic board anchor, identical
// event streams on both clients, identical terminal state, and score
// reproducibility against an accelerated offline replay of the same log.
func TestExitGateRepeatedFullMatches(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping exit-gate transport automation in -short mode")
	}
	api := NewAPI()
	srv := httptest.NewServer(api.Routes())
	defer srv.Close()

	// Validate every seed solves; a future dictionary change must fail loudly.
	for _, seed := range egSeeds {
		if _, _, ok := egSolve(t, "en", seed); !ok {
			t.Fatalf("egSeeds entry %d is not fully claimable", seed)
		}
	}

	rounds := 2
	for r := 1; r <= rounds; r++ {
		for _, seed := range egSeeds {
			egPlayOne(t, srv, "en", seed, r)
		}
	}
}
