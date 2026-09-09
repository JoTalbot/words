package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	wordarenav1 "github.com/JoTalbot/words/server/gen/wordarena/net/v1"
	"github.com/JoTalbot/words/server/internal/match"
	"github.com/JoTalbot/words/server/internal/matchroom"
	"github.com/JoTalbot/words/server/internal/protocol"
	"github.com/coder/websocket"
	"google.golang.org/protobuf/proto"
)

// API hosts the M0 dev transport: match creation over HTTP, live play over
// WebSocket (binary Protobuf envelopes). Rooms are in-memory; the protocol
// and the deterministic simulation are the product surface.
type API struct {
	rooms   map[uint64]*matchroom.Room
	mu      sync.Mutex
	counter atomic.Uint64

	stopCh chan struct{}

	// Resource limits (production-shape hardening, M1 prep). Zero means
	// unlimited for maxRooms; maxWSBytes is always enforced when positive.
	maxRooms   int
	maxWSBytes int64

	// results persists the final outcome of finished matches (in-memory,
	// TTL-reaped). Durable match persistence is M1 work.
	results   map[uint64]matchResult
	resultsMu sync.Mutex
}

// matchResult is the persisted post-match outcome served by
// GET /v1/matches/{id}/result.
type matchResult struct {
	MatchID    uint64    `json:"match_id"`
	Seed       uint64    `json:"seed"`
	Language   string    `json:"language"`
	Over       bool      `json:"over"`
	WinnerSeat int       `json:"winner_seat"` // -1 on tie
	IsTie      bool      `json:"is_tie"`
	Scores     [2]int64  `json:"scores"`
	StateVer   int       `json:"state_version"`
	ServerTick int       `json:"server_tick"`
	recordedAt time.Time `json:"-"`
}

// resultTTL is how long finished match results are kept in memory.
const resultTTL = 5 * time.Minute

// NewAPI builds the service. Limits are read from environment variables
// (WORDARENA_MAX_ROOMS, WORDARENA_MAX_WS_BYTES) with safe defaults.
func NewAPI() *API {
	a := &API{
		rooms:      map[uint64]*matchroom.Room{},
		stopCh:     make(chan struct{}),
		maxRooms:   envInt("WORDARENA_MAX_ROOMS", 128),
		maxWSBytes: int64(envInt("WORDARENA_MAX_WS_BYTES", 64<<10)),
		results:    map[uint64]matchResult{},
	}
	return a
}

// envInt parses an integer environment variable with a default.
func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			return n
		}
	}
	return def
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func randomSeed() (uint64, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return 0, err
	}
	return uint64(b[0])<<56 | uint64(b[1])<<48 | uint64(b[2])<<40 | uint64(b[3])<<32 |
		uint64(b[4])<<24 | uint64(b[5])<<16 | uint64(b[6])<<8 | uint64(b[7]), nil
}

// createMatchRequest mirrors the JSON body of POST /v1/matches. Seed is
// optional: fixed seeds create reproducible matches for tools and E2E
// tests; random seeds are used when absent.
type createMatchRequest struct {
	Language string  `json:"language"`
	Seed     *uint64 `json:"seed,omitempty"`
}

// createMatchResponse is the JSON body returned on match creation.
type createMatchResponse struct {
	MatchID  uint64    `json:"match_id"`
	Seed     uint64    `json:"seed"`
	Language string    `json:"language"`
	Tokens   [2]string `json:"tokens"`
	UserIDs  [2]uint64 `json:"user_ids"`
}

// Routes registers the service handlers, wrapped in request logging.
func (a *API) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", a.handleHealthz)
	mux.HandleFunc("POST /v1/matches", a.handleCreateMatch)
	mux.HandleFunc("GET /v1/match/ws", a.handleWS)
	mux.HandleFunc("GET /v1/match/{id}/snapshot", a.handleSnapshot)
	mux.HandleFunc("GET /v1/matches/{id}/result", a.handleResult)
	return requestLogger(mux)
}

func (a *API) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

func (a *API) handleCreateMatch(w http.ResponseWriter, r *http.Request) {
	var req createMatchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	lang := req.Language
	if lang == "" {
		lang = "en"
	}
	// validate language by attempting a match build later; cheap check here
	switch lang {
	case "en", "ru", "uk":
	default:
		httpError(w, http.StatusBadRequest, "language must be en, ru or uk")
		return
	}
	var seed uint64
	if req.Seed != nil {
		seed = *req.Seed
	} else {
		s, err := randomSeed()
		if err != nil {
			httpError(w, http.StatusInternalServerError, "rng failure")
			return
		}
		seed = s
	}
	tok0, err := randomHex(16)
	if err != nil {
		httpError(w, http.StatusInternalServerError, "rng failure")
		return
	}
	tok1, err := randomHex(16)
	if err != nil {
		httpError(w, http.StatusInternalServerError, "rng failure")
		return
	}
	id := a.counter.Add(1)
	room, err := matchroom.New(matchroom.Config{
		MatchID:  id,
		Seed:     seed,
		Language: lang,
		UserIDs:  [2]uint64{id*2 + 1, id*2 + 2}, // deterministic synthetic ids
		Token0:   tok0,
		Token1:   tok1,
	})
	if err != nil {
		httpError(w, http.StatusInternalServerError, err.Error())
		return
	}
	a.mu.Lock()
	if a.maxRooms > 0 && len(a.rooms) >= a.maxRooms {
		a.mu.Unlock()
		httpError(w, http.StatusTooManyRequests, "too many active matches")
		return
	}
	a.rooms[id] = room
	a.mu.Unlock()
	go a.runRoomTicker(id, room)

	resp := createMatchResponse{
		MatchID: id, Seed: seed, Language: lang,
		Tokens: [2]string{tok0, tok1}, UserIDs: [2]uint64{id*2 + 1, id*2 + 2},
	}
	writeJSON(w, http.StatusCreated, resp)
}

// runRoomTicker advances a room at 30 Hz until shortly after match end.
func (a *API) runRoomTicker(id uint64, room *matchroom.Room) {
	tick := time.NewTicker(time.Second / match.TicksPerSecond)
	defer tick.Stop()
	for {
		select {
		case <-a.stopCh:
			return
		case <-tick.C:
			room.Tick()
			if room.IsOver() {
				// Persist the final outcome before the room is torn down so
				// clients can fetch it via GET /v1/matches/{id}/result.
				a.recordResult(room)
				// keep serving final state briefly so late readers catch up
				select {
				case <-time.After(3 * time.Second):
				case <-a.stopCh:
					return
				}
				a.mu.Lock()
				delete(a.rooms, id)
				a.mu.Unlock()
				room.Close()
				return
			}
		}
	}
}

func (a *API) handleSnapshot(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r.PathValue("id"))
	if err != nil {
		httpError(w, http.StatusBadRequest, "invalid match id")
		return
	}
	a.mu.Lock()
	room, ok := a.rooms[id]
	a.mu.Unlock()
	if !ok {
		httpError(w, http.StatusNotFound, "match not found or finished")
		return
	}
	snap := protocol.SnapshotToProto(room.Match().Snapshot(), [2]uint64{room.UserID(0), room.UserID(1)})
	writeJSON(w, http.StatusOK, map[string]any{
		"server_tick":   snap.ServerTick,
		"wave":          snap.CurrentWave,
		"state_version": snap.StateVersion,
		"cells":         cellsToJSON(snap.Cells),
		"players":       playersToJSON(snap.Players),
	})
}

func cellsToJSON(cells []*wordarenav1.BoardCell) []map[string]any {
	out := make([]map[string]any, 0, len(cells))
	for _, c := range cells {
		out = append(out, map[string]any{
			"id": c.CellId, "letter": c.Letter, "owner": c.OwnerUserId,
			"locked": c.IsLocked, "lock_ms": c.LockRemainingMs,
		})
	}
	return out
}

// handleResult serves the persisted outcome of a finished match.
func (a *API) handleResult(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r.PathValue("id"))
	if err != nil {
		httpError(w, http.StatusBadRequest, "invalid match id")
		return
	}
	a.resultsMu.Lock()
	res, ok := a.results[id]
	a.resultsMu.Unlock()
	if !ok {
		httpError(w, http.StatusNotFound, "result not found or match still active")
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// recordResult captures the final outcome of an over match and reaps stale
// entries. It is idempotent: the first record for a match wins.
func (a *API) recordResult(room *matchroom.Room) {
	m := room.Match()
	r := m.Result()
	snap := m.Snapshot()
	winner := int(r.WinnerSeat)
	if r.IsTie {
		winner = -1
	}
	res := matchResult{
		MatchID:    m.ID,
		Seed:       m.Seed,
		Language:   m.Lang,
		Over:       r.Over,
		WinnerSeat: winner,
		IsTie:      r.IsTie,
		Scores:     [2]int64{m.Score(0), m.Score(1)},
		StateVer:   snap.StateVersion,
		ServerTick: snap.ServerTick,
		recordedAt: time.Now(),
	}
	a.resultsMu.Lock()
	if _, exists := a.results[res.MatchID]; !exists {
		a.results[res.MatchID] = res
	}
	// reap stale results
	cutoff := time.Now().Add(-resultTTL)
	for id, old := range a.results {
		if old.recordedAt.Before(cutoff) {
			delete(a.results, id)
		}
	}
	a.resultsMu.Unlock()

	log.Printf("match lifecycle=%s id=%d seed=%d lang=%s winner=%d tie=%v scores=%d:%d ticks=%d",
		"over", res.MatchID, res.Seed, res.Language, res.WinnerSeat, res.IsTie,
		res.Scores[0], res.Scores[1], res.ServerTick)
}

// requestLogger emits one structured line per HTTP request (method, path,
// status, bytes, duration). WebSocket upgrades are logged on handshake.
func requestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		log.Printf("http method=%s path=%s status=%d bytes=%d dur=%s",
			r.Method, r.URL.Path, rec.status, rec.bytes, time.Since(start).Round(time.Microsecond))
	})
}

// statusRecorder captures the response status and byte count for logging
// while still supporting WebSocket hijacking and streaming flush.
type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (r *statusRecorder) WriteHeader(c int) {
	r.status = c
	r.ResponseWriter.WriteHeader(c)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	n, err := r.ResponseWriter.Write(b)
	r.bytes += n
	return n, err
}

func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (r *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := r.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("http: ResponseWriter does not support hijacking")
	}
	return h.Hijack()
}

func playersToJSON(ps []*wordarenav1.PlayerState) []map[string]any {
	out := make([]map[string]any, 0, len(ps))
	for _, p := range ps {
		out = append(out, map[string]any{
			"user_id": p.UserId, "score": p.Score, "rank": p.RankPosition,
			"combo_mult": p.ComboMultiplier,
		})
	}
	return out
}

// handleWS upgrades the connection and runs the live duplex stream.
func (a *API) handleWS(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	id, err := parseID(q.Get("match_id"))
	if err != nil {
		httpError(w, http.StatusBadRequest, "invalid match_id")
		return
	}
	token := q.Get("token")
	a.mu.Lock()
	room, ok := a.rooms[id]
	a.mu.Unlock()
	if !ok {
		httpError(w, http.StatusNotFound, "match not found or finished")
		return
	}
	seat, ok := room.SeatForToken(token)
	if !ok {
		httpError(w, http.StatusUnauthorized, "invalid token")
		return
	}

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: []string{"*"}})
	if err != nil {
		return
	}
	if a.maxWSBytes > 0 {
		conn.SetReadLimit(a.maxWSBytes)
	}
	sub := room.Subscribe(seat)
	ctx := r.Context()

	// Immediate canonical snapshot anchors the client (resume semantics:
	// the client reconciles against this snapshot regardless of prior state).
	sendSnapshot := func() {
		userIDs := [2]uint64{room.UserID(0), room.UserID(1)}
		snap := protocol.SnapshotToProto(room.Snapshot(), userIDs)
		env := protocol.SnapshotEnvelope(id, snap)
		_ = wsWriteProto(ctx, conn, env)
	}
	sendSnapshot()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-ctx.Done():
				return
			case ev := <-sub.Events:
				pb := protocol.WordEventToProto(toMatchEvent(ev), room.UserID(ev.Seat))
				pb.ClientSequence = ev.ClientSeq
				evp := pb
				env := protocol.WordEnvelope(id, evp)
				if err := wsWriteProto(ctx, conn, env); err != nil {
					return
				}
			case sf := <-sub.Snapshots:
				userIDs := [2]uint64{room.UserID(0), room.UserID(1)}
				env := protocol.SnapshotEnvelope(id, protocol.SnapshotToProto(sf.Snapshot, userIDs))
				if err := wsWriteProto(ctx, conn, env); err != nil {
					return
				}
			}
		}
	}()

	// Reader loop: client intents.
	for {
		typ, data, err := conn.Read(r.Context())
		if err != nil {
			break
		}
		if typ != websocket.MessageBinary {
			continue
		}
		var env wordarenav1.ClientEnvelope
		if err := proto.Unmarshal(data, &env); err != nil {
			continue
		}
		sw := env.GetSubmitWord()
		if sw == nil {
			continue
		}
		ids := make([]int, 0, len(sw.LetterIndices))
		for _, v := range sw.LetterIndices {
			ids = append(ids, int(v))
		}
		if _, err := room.SubmitWithSeq(seat, sw.ClientSequence, ids); err != nil {
			_ = conn.Close(websocket.StatusInternalError, "submit failed")
			break
		}
	}
	_ = conn.Close(websocket.StatusNormalClosure, "bye")
	room.Unsubscribe(seat)
	<-done
}

// toMatchEvent adapts a matchroom fan-out frame to the domain event shape
// the protocol converter expects.
func toMatchEvent(f matchroom.EventFrame) match.Event {
	return match.Event{
		Seq: f.Seq, Tick: f.Tick, Seat: f.Seat, CellIDs: f.CellIDs,
		Word: f.Word, Result: f.Result, ScoreAdded: f.ScoreAdded,
		TotalScore: f.TotalScore, IsSteal: f.IsSteal,
		StateVersion: f.StateVer,
	}
}

// small helpers ---------------------------------------------------------

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

func clampNonNeg(v int64) int64 {
	if v < 0 {
		return 0
	}
	return v
}

func wsWriteProto(ctx context.Context, conn *websocket.Conn, msg *wordarenav1.ServerEnvelope) error {
	b, err := proto.Marshal(msg)
	if err != nil {
		return err
	}
	return conn.Write(ctx, websocket.MessageBinary, b)
}

func parseID(s string) (uint64, error) {
	var id uint64
	if s == "" {
		return 0, fmt.Errorf("empty id")
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("bad id")
		}
		id = id*10 + uint64(c-'0')
	}
	return id, nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func httpError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
