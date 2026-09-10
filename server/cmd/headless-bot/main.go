// Command headless-bot is the M0 exit-gate acceptance harness
// (docs/M0.md): it plays complete deterministic 1v1 matches against a live
// word-arena server over the real HTTP+WebSocket transport with two remote
// clients, and verifies:
//
//   - both clients observe identical event streams and identical terminal
//     (over=true) snapshots;
//   - live final scores equal an accelerated offline replay of the same
//     deterministic intent script (reproducible event log).
//
// The intent script is solved offline by exact-cover over the real
// dictionary snapshot, so the same seed always produces the same match.
//
// Usage:
//
//	WORDARENA_ADDR=http://127.0.0.1:18080 go run ./cmd/headless-bot
//	go run ./cmd/headless-bot -rounds 2 -seeds 1512,1513,1517 -v
//	go run ./cmd/headless-bot -via-queue -rounds 1 -seeds 1
//
// With -via-queue the match is formed by two anonymous matchmaking
// enqueues (batch 17D regression): seats keep FIFO order, the server
// chooses the seed, and the same exit-gate invariants must hold.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	wordarenav1 "github.com/JoTalbot/words/server/gen/wordarena/net/v1"
	"github.com/JoTalbot/words/server/internal/dictionary"
	"github.com/JoTalbot/words/server/internal/match"
	"github.com/coder/websocket"
	"google.golang.org/protobuf/proto"
)

// ---- offline solver (deterministic intent script) ----

type botMove struct {
	wave int
	seat match.Seat
	ids  []uint32
	word string
	seq  uint32
}

type coverCtx struct {
	cells  []match.Cell
	wb     map[int][]string
	budget int
}

func (c *coverCtx) assign(rem []int, counts map[rune]int, w string) []int {
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

func (c *coverCtx) search(rem []int) [][]int {
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
			rest := remainder(rem, ids)
			if sub := c.search(rest); sub != nil {
				return append([][]int{ids}, sub...)
			}
		}
	}
	return nil
}

func remainder(rem, ids []int) []int {
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

func coverWave(snap *dictionary.Snapshot, cells []match.Cell) [][]int {
	rem := make([]int, 0, len(cells))
	for i := range cells {
		if cells[i].State == match.CellFree {
			rem = append(rem, i)
		}
	}
	wb := map[int][]string{}
	for _, w := range snap.Words() {
		l := len([]rune(w))
		if l >= 3 && l <= 8 {
			wb[l] = append(wb[l], w)
		}
	}
	return (&coverCtx{cells: cells, wb: wb, budget: 200000}).search(rem)
}

// initialSnapshotEqual reports whether two snapshots describe the same game,
// ignoring the clock fields that differ between two sockets purely because they
// connected at different ticks.
func initialSnapshotEqual(a, b *wordarenav1.MatchStateSnapshot) bool {
	return a.GetMatchId() == b.GetMatchId() &&
		a.GetCurrentWave() == b.GetCurrentWave() &&
		a.GetOver() == b.GetOver() &&
		msgsEqual(a.GetCells(), b.GetCells()) &&
		msgsEqual(a.GetPlayers(), b.GetPlayers())
}

// alignAndCompareInitial enforces acceptance 1 across two sockets that connected
// at different points in a 30 Hz stream. If the frames already agree, it returns
// immediately. If one socket is a wave behind, it reads that socket forward until
// the waves match and compares again. Only a genuine difference in the game -
// board, ownership, scores, wave - is reported, and the report carries both
// frames' clock fields so a future reader can see the skew that was ruled out.
func alignAndCompareInitial(s0, s1 *wordarenav1.MatchStateSnapshot, c0, c1 *botClient, ctx context.Context) error {
	if initialSnapshotEqual(s0, s1) {
		return nil
	}
	const maxAdvance = 8
	for i := 0; i < maxAdvance; i++ {
		behind, ahead := c0, c1
		cur, other := s0, s1
		if s1.GetCurrentWave() < s0.GetCurrentWave() {
			behind, ahead = c1, c0
			cur, other = s1, s0
		}
		_ = ahead
		if cur.GetCurrentWave() >= other.GetCurrentWave() {
			break // same wave and still different: a real divergence
		}
		p, err := behind.readPayload(ctx)
		if err != nil {
			return err
		}
		snap, ok := p.(*wordarenav1.MatchStateSnapshot)
		if !ok {
			continue
		}
		if behind == c0 {
			s0 = snap
		} else {
			s1 = snap
		}
		if initialSnapshotEqual(s0, s1) {
			return nil
		}
	}
	return fmt.Errorf(
		"initial snapshots describe different games "+
			"(seat0 tick=%d ver=%d wave=%d remaining_ms=%d, "+
			"seat1 tick=%d ver=%d wave=%d remaining_ms=%d, "+
			"cells_equal=%v players_equal=%v)",
		s0.GetServerTick(), s0.GetStateVersion(), s0.GetCurrentWave(), s0.GetRemainingTimeMs(),
		s1.GetServerTick(), s1.GetStateVersion(), s1.GetCurrentWave(), s1.GetRemainingTimeMs(),
		msgsEqual(s0.GetCells(), s1.GetCells()),
		msgsEqual(s0.GetPlayers(), s1.GetPlayers()))
}

// msgsEqual compares two repeated message fields element-wise. proto.Equal only
// takes whole messages, and the divergence report below needs to say whether the
// board itself differed or only the clock fields did.
func msgsEqual[T proto.Message](a, b []T) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !proto.Equal(a[i], b[i]) {
			return false
		}
	}
	return true
}

// errNotCoverable marks a board the offline planner could not fully cover with
// dictionary words. It is a precondition of this harness on an arbitrary board,
// not a server fault: the direct path never reports it because that path runs
// curated seeds, while -via-queue lets the server choose any seed. Counting it
// as a failure made roughly 3 percent of every random-seed queue run report a
// defect that was not there (5 of 147 matches measured on 2026-09-10), which is
// how a real divergence signal gets buried. Callers classify it with errors.As
// and report it as skipped.
type errNotCoverable struct {
	seed uint64
	wave int
}

func (e *errNotCoverable) Error() string {
	return fmt.Sprintf("seed %d: wave %d not fully claimable", e.seed, e.wave)
}

// solveSeed replays a full match in-process and returns the script.
func solveSeed(lang string, seed uint64) ([]botMove, [2]int64, error) {
	l, err := dictionary.ParseLanguage(lang)
	if err != nil {
		return nil, [2]int64{}, err
	}
	snap, err := dictionary.LoadSnapshot(l)
	if err != nil {
		return nil, [2]int64{}, err
	}
	m, err := match.New(match.Config{MatchID: 1, Seed: seed, Lang: lang})
	if err != nil {
		return nil, [2]int64{}, err
	}
	var moves []botMove
	seatSeq := [2]uint32{}
	guard := 0
	for !m.IsOver() {
		if guard > 64 {
			return nil, [2]int64{}, fmt.Errorf("seed %d: too many moves", seed)
		}
		wave := m.Wave()
		claims := coverWave(snap, m.Cells())
		if claims == nil {
			return nil, [2]int64{}, &errNotCoverable{seed: seed, wave: wave}
		}
		for _, path := range claims {
			seat := match.Seat(guard % 2)
			ev := m.Submit(seat, path)
			if ev.Result != match.ResultAccepted {
				return nil, [2]int64{}, fmt.Errorf("seed %d: claim %q not accepted: %s", seed, ev.Word, ev.WordResultString)
			}
			seatSeq[seat]++
			ids := make([]uint32, len(path))
			for i, id := range path {
				ids[i] = uint32(id)
			}
			moves = append(moves, botMove{wave: wave, seat: seat, ids: ids, word: ev.Word, seq: seatSeq[seat]})
			guard++
		}
	}
	return moves, [2]int64{m.Score(0), m.Score(1)}, nil
}

// ---- live transport client ----

type botClient struct {
	conn *websocket.Conn
}

func dialBot(addr string, matchID uint64, token string) (*botClient, error) {
	wsURL := "ws" + strings.TrimPrefix(addr, "http") + fmt.Sprintf("/v1/match/ws?match_id=%d&token=%s", matchID, token)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		return nil, err
	}
	return &botClient{conn: conn}, nil
}

func (b *botClient) close() { _ = b.conn.Close(websocket.StatusNormalClosure, "bot done") }

func (b *botClient) readPayload(ctx context.Context) (proto.Message, error) {
	_, data, err := b.conn.Read(ctx)
	if err != nil {
		return nil, err
	}
	var env wordarenav1.ServerEnvelope
	if err := proto.Unmarshal(data, &env); err != nil {
		return nil, err
	}
	if ev := env.GetWordEvent(); ev != nil {
		return ev, nil
	}
	if snap := env.GetSnapshot(); snap != nil {
		return snap, nil
	}
	return nil, fmt.Errorf("unknown server payload")
}

func (b *botClient) submit(ctx context.Context, matchID uint64, path []uint32, seq uint32) error {
	env := &wordarenav1.ClientEnvelope{
		MatchId: matchID,
		Payload: &wordarenav1.ClientEnvelope_SubmitWord{
			SubmitWord: &wordarenav1.SubmitWordIntent{
				MatchId: matchID, ClientSequence: seq, LetterIndices: path,
			},
		},
	}
	data, err := proto.Marshal(env)
	if err != nil {
		return err
	}
	return b.conn.Write(ctx, websocket.MessageBinary, data)
}

func (b *botClient) waitEvent(ctx context.Context, user uint64, seq uint32) (*wordarenav1.WordValidatedEvent, error) {
	for {
		p, err := b.readPayload(ctx)
		if err != nil {
			return nil, err
		}
		ev, ok := p.(*wordarenav1.WordValidatedEvent)
		if !ok {
			continue
		}
		if ev.UserId == user && ev.ClientSequence == seq {
			return ev, nil
		}
	}
}

func (b *botClient) waitOver(ctx context.Context) (*wordarenav1.MatchStateSnapshot, error) {
	for {
		p, err := b.readPayload(ctx)
		if err != nil {
			return nil, err
		}
		snap, ok := p.(*wordarenav1.MatchStateSnapshot)
		if !ok {
			continue
		}
		if snap.Over {
			return snap, nil
		}
	}
}

// ---- match runner ----

type matchResult struct {
	Seed          uint64   `json:"seed"`
	Lang          string   `json:"language"`
	Intents       int      `json:"intents"`
	Scores        [2]int64 `json:"final_scores"`
	TerminalEqual bool     `json:"terminal_snapshots_equal"`
	StreamsEqual  bool     `json:"client_streams_equal"`
	ElapsedMs     int64    `json:"elapsed_ms"`
}

// createdMatch is the join info for one match regardless of how it was
// created (direct REST create or matchmaking queue).
type createdMatch struct {
	MatchID uint64
	Seed    uint64
	Tokens  [2]string
	UserIDs [2]uint64
}

func createMatchDirect(addr, lang string, seed uint64) (createdMatch, error) {
	body := fmt.Sprintf(`{"language":%q,"seed":%d}`, lang, seed)
	resp, err := http.Post(addr+"/v1/matches", "application/json", strings.NewReader(body))
	if err != nil {
		return createdMatch{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return createdMatch{}, fmt.Errorf("create match: HTTP %d", resp.StatusCode)
	}
	var created struct {
		MatchID uint64    `json:"match_id"`
		Seed    uint64    `json:"seed"`
		Tokens  [2]string `json:"tokens"`
		UserIDs [2]uint64 `json:"user_ids"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		return createdMatch{}, err
	}
	return createdMatch{MatchID: created.MatchID, Seed: created.Seed, Tokens: created.Tokens, UserIDs: created.UserIDs}, nil
}

// queueEntryView mirrors the /v1/queue entry JSON (server/cmd/game/
// matchmaking.go queueEntry).
type queueEntryView struct {
	QueueID string `json:"queue_id"`
	Status  string `json:"status"`
	MatchID uint64 `json:"match_id"`
	Seed    uint64 `json:"seed"`
	Token   string `json:"token"`
	UserID  uint64 `json:"user_id"`
}

func queueEnqueue(addr, lang string) (queueEntryView, error) {
	body := fmt.Sprintf(`{"language":%q}`, lang)
	resp, err := http.Post(addr+"/v1/queue", "application/json", strings.NewReader(body))
	if err != nil {
		return queueEntryView{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		return queueEntryView{}, fmt.Errorf("queue enqueue: HTTP %d", resp.StatusCode)
	}
	var e queueEntryView
	if err := json.NewDecoder(resp.Body).Decode(&e); err != nil {
		return queueEntryView{}, err
	}
	return e, nil
}

func queuePoll(addr, id string) (queueEntryView, error) {
	resp, err := http.Get(addr + "/v1/queue/" + id)
	if err != nil {
		return queueEntryView{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return queueEntryView{}, fmt.Errorf("queue poll %s: HTTP %d", id, resp.StatusCode)
	}
	var e queueEntryView
	if err := json.NewDecoder(resp.Body).Decode(&e); err != nil {
		return queueEntryView{}, err
	}
	return e, nil
}

// createMatchViaQueue pairs two anonymous queue entries and returns their
// join info. The server keeps FIFO seat order (first enqueue -> seat 0), so
// the entry tokens map directly onto the bot seats. If an outside player
// steals one of the pairings on a shared server, the attempt is retried with
// fresh entries.
func createMatchViaQueue(addr, lang string) (createdMatch, error) {
	for attempt := 0; attempt < 3; attempt++ {
		a, err := queueEnqueue(addr, lang)
		if err != nil {
			return createdMatch{}, err
		}
		b, err := queueEnqueue(addr, lang)
		if err != nil {
			return createdMatch{}, err
		}
		deadline := time.Now().Add(20 * time.Second)
		for (a.Status != "matched" || b.Status != "matched") && time.Now().Before(deadline) {
			time.Sleep(200 * time.Millisecond)
			if a.Status != "matched" {
				if a, err = queuePoll(addr, a.QueueID); err != nil {
					return createdMatch{}, err
				}
			} else {
				if b, err = queuePoll(addr, b.QueueID); err != nil {
					return createdMatch{}, err
				}
			}
		}
		if a.Status == "matched" && b.Status == "matched" && a.MatchID == b.MatchID && a.MatchID != 0 {
			return createdMatch{
				MatchID: a.MatchID,
				Seed:    a.Seed,
				Tokens:  [2]string{a.Token, b.Token},
				UserIDs: [2]uint64{a.UserID, b.UserID},
			}, nil
		}
	}
	return createdMatch{}, fmt.Errorf("queue pairing did not complete (entries did not converge to one match)")
}

func playOne(addr, lang string, seed uint64, viaQueue bool) (*matchResult, error) {
	var created createdMatch
	var err error
	if viaQueue {
		created, err = createMatchViaQueue(addr, lang)
	} else {
		created, err = createMatchDirect(addr, lang, seed)
	}
	if err != nil {
		return nil, err
	}
	// The queued match seed is chosen by the server, so the offline solve
	// always runs against the authoritative seed from the join info.
	moves, want, err := solveSeed(lang, created.Seed)
	if err != nil {
		return nil, err
	}
	start := time.Now()
	c0, err := dialBot(addr, created.MatchID, created.Tokens[0])
	if err != nil {
		return nil, fmt.Errorf("dial seat0: %w", err)
	}
	defer c0.close()
	c1, err := dialBot(addr, created.MatchID, created.Tokens[1])
	if err != nil {
		return nil, fmt.Errorf("dial seat1: %w", err)
	}
	defer c1.close()

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	// initial canonical snapshots must be identical (acceptance 1)
	s0p, err := c0.readPayload(ctx)
	if err != nil {
		return nil, err
	}
	s0, ok := s0p.(*wordarenav1.MatchStateSnapshot)
	if !ok {
		return nil, fmt.Errorf("seat0: expected snapshot, got %T", s0p)
	}
	s1p, err := c1.readPayload(ctx)
	if err != nil {
		return nil, err
	}
	s1, ok := s1p.(*wordarenav1.MatchStateSnapshot)
	if !ok {
		return nil, fmt.Errorf("seat1: expected snapshot, got %T", s1p)
	}
	// Acceptance 1: both seats must see the same game. It used to assert
	// proto.Equal on the two first frames, and that was over-strict for a 30 Hz
	// stream. The room starts its ticker when it is created - for a queued match
	// that is the moment the matchmaker pairs the two seats (api.go:643,
	// `go a.runRoomTicker(id, room)`) - while each socket is sent a snapshot when
	// it connects (api.go:1163). The two sockets are dialed sequentially, so they
	// can legitimately receive different ticks, and if a wave boundary falls
	// between the two connections they receive different boards too. Neither is a
	// determinism failure. Proven from code on 2026-09-11 and consistent with the
	// measurement: 4 of 147 queue matches reported this class while every match
	// that ran to completion reported terminal_snapshots_equal and
	// client_streams_equal true.
	//
	// So compare what determinism actually promises - the game, not the clock.
	// server_tick, remaining_time_ms and state_version are deliberately excluded;
	// match_id, wave, board and scores are not. If the waves differ, advance the
	// socket that is behind until they agree, because a wave boundary between the
	// two connections is not divergence either.
	if err := alignAndCompareInitial(s0, s1, c0, c1, ctx); err != nil {
		return nil, fmt.Errorf("seed %d: %w", created.Seed, err)
	}

	bots := []*botClient{c0, c1}
	users := created.UserIDs
	lastWave := -1
	streamsEqual := true
	for _, mv := range moves {
		if mv.wave != lastWave {
			for {
				p, err := c0.readPayload(ctx)
				if err != nil {
					return nil, err
				}
				snap, ok := p.(*wordarenav1.MatchStateSnapshot)
				if ok && snap.CurrentWave >= uint32(mv.wave) {
					break
				}
			}
			lastWave = mv.wave
		}
		b := bots[mv.seat]
		if err := b.submit(ctx, created.MatchID, mv.ids, mv.seq); err != nil {
			return nil, fmt.Errorf("submit: %w", err)
		}
		ev0, err := bots[0].waitEvent(ctx, users[mv.seat], mv.seq)
		if err != nil {
			return nil, fmt.Errorf("seat0 event: %w", err)
		}
		ev1, err := bots[1].waitEvent(ctx, users[mv.seat], mv.seq)
		if err != nil {
			return nil, fmt.Errorf("seat1 event: %w", err)
		}
		if ev0.Result != wordarenav1.WordResult_ACCEPTED || ev1.Result != wordarenav1.WordResult_ACCEPTED {
			return nil, fmt.Errorf("seed %d: %q not accepted (%v/%v)", created.Seed, mv.word, ev0.Result, ev1.Result)
		}
		if !proto.Equal(ev0, ev1) {
			streamsEqual = false
		}
	}

	f0, err := bots[0].waitOver(ctx)
	if err != nil {
		return nil, fmt.Errorf("seat0 over: %w", err)
	}
	f1, err := bots[1].waitOver(ctx)
	if err != nil {
		return nil, fmt.Errorf("seat1 over: %w", err)
	}
	terminalEqual := proto.Equal(f0, f1)
	res := &matchResult{
		Seed: created.Seed, Lang: lang, Intents: len(moves),
		StreamsEqual: streamsEqual, TerminalEqual: terminalEqual,
		ElapsedMs: time.Since(start).Milliseconds(),
	}
	for seat := 0; seat < 2; seat++ {
		res.Scores[seat] = int64(f0.Players[seat].Score)
		if f0.Players[seat].Score != uint32(want[seat]) {
			return nil, fmt.Errorf("seed %d: live score %d != offline replay %d (seat %d)", created.Seed, f0.Players[seat].Score, want[seat], seat)
		}
	}
	if !terminalEqual {
		return nil, fmt.Errorf("seed %d: terminal snapshots diverge between clients", created.Seed)
	}
	if !streamsEqual {
		return nil, fmt.Errorf("seed %d: client event streams differ", created.Seed)
	}
	return res, nil
}

func main() {
	addr := flag.String("addr", os.Getenv("WORDARENA_ADDR"), "server base URL (default WORDARENA_ADDR or http://127.0.0.1:18080)")
	lang := flag.String("lang", "en", "match language")
	seeds := flag.String("seeds", "1512,1513,1517", "comma-separated deterministic seeds")
	rounds := flag.Int("rounds", 1, "repetitions per seed")
	viaQueue := flag.Bool("via-queue", false, "create each match through the matchmaking queue (POST /v1/queue) instead of POST /v1/matches; the server picks the seed and -seeds only controls the match count")
	verbose := flag.Bool("v", false, "verbose per-match output")
	flag.Parse()
	if *addr == "" {
		*addr = "http://127.0.0.1:18080"
	}
	seedList := []uint64{}
	for _, s := range strings.Split(*seeds, ",") {
		var v uint64
		if _, err := fmt.Sscanf(strings.TrimSpace(s), "%d", &v); err != nil {
			fmt.Fprintf(os.Stderr, "bad seed %q\n", s)
			os.Exit(2)
		}
		seedList = append(seedList, v)
	}

	health, err := http.Get(*addr + "/healthz")
	if err != nil || health.StatusCode != 200 {
		fmt.Fprintf(os.Stderr, "server unreachable at %s: %v\n", *addr, err)
		os.Exit(1)
	}
	health.Body.Close()

	var results []*matchResult
	failed := 0
	skipped := 0
	for r := 1; r <= *rounds; r++ {
		for _, seed := range seedList {
			res, err := playOne(*addr, *lang, seed, *viaQueue)
			if err != nil {
				var nc *errNotCoverable
				if errors.As(err, &nc) {
					// A harness precondition, not a server fault. Reported
					// separately so the failure rate stays meaningful, and the
					// match is not counted as measured.
					skipped++
					fmt.Printf("SKIP seed=%d round=%d: %v (offline planner cannot cover this board)\n", seed, r, err)
					continue
				}
				failed++
				fmt.Printf("FAIL seed=%d round=%d: %v\n", seed, r, err)
				continue
			}
			results = append(results, res)
			if *verbose {
				fmt.Printf("ok seed=%d round=%d intents=%d final=%d:%d terminal_equal=%v streams_equal=%v (%d ms)\n",
					seed, r, res.Intents, res.Scores[0], res.Scores[1], res.TerminalEqual, res.StreamsEqual, res.ElapsedMs)
			}
		}
	}
	if *verbose {
		out, _ := json.MarshalIndent(results, "", "  ")
		fmt.Println(string(out))
	} else {
		summary := map[string]any{
			"ok":      len(results),
			"failed":  failed,
			"skipped": skipped,
			"matches": results,
		}
		out, _ := json.Marshal(summary)
		fmt.Println(string(out))
	}
	if failed > 0 {
		os.Exit(1)
	}
	if len(results) == 0 {
		// Every match was skipped: nothing was measured, so this run proves
		// nothing and must not report a pass.
		fmt.Fprintln(os.Stderr, "EXIT-GATE: NO SAMPLES (every match was skipped by the offline planner)")
		os.Exit(1)
	}
	fmt.Printf("EXIT-GATE: PASS (%d measured, %d skipped by the offline planner)\n", len(results), skipped)
}
