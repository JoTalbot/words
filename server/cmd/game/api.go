package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
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
}

// NewAPI builds the service.
func NewAPI() *API {
	a := &API{rooms: map[uint64]*matchroom.Room{}, stopCh: make(chan struct{})}
	return a
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

// createMatchRequest mirrors the JSON body of POST /v1/matches.
type createMatchRequest struct {
	Language string `json:"language"`
}

// createMatchResponse is the JSON body returned on match creation.
type createMatchResponse struct {
	MatchID  uint64      `json:"match_id"`
	Seed     uint64      `json:"seed"`
	Language string      `json:"language"`
	Tokens   [2]string   `json:"tokens"`
	UserIDs  [2]uint64   `json:"user_ids"`
}

// Routes registers the service handlers.
func (a *API) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", a.handleHealthz)
	mux.HandleFunc("POST /v1/matches", a.handleCreateMatch)
	mux.HandleFunc("GET /v1/match/ws", a.handleWS)
	mux.HandleFunc("GET /v1/match/{id}/snapshot", a.handleSnapshot)
	return mux
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
	seed, err := randomSeed()
	if err != nil {
		httpError(w, http.StatusInternalServerError, "rng failure")
		return
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
			if room.Match().IsOver() {
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
	sub := room.Subscribe(seat)
	ctx := r.Context()

	// Immediate canonical snapshot anchors the client (resume semantics:
	// the client reconciles against this snapshot regardless of prior state).
	room.Tick() // no-op ordering aid is not needed; send current state:
	sendSnapshot := func() {
		userIDs := [2]uint64{room.UserID(0), room.UserID(1)}
		snap := protocol.SnapshotToProto(room.Match().Snapshot(), userIDs)
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
				evp := protocol.WordEventToProto(toMatchEvent(ev), room.UserID(ev.Seat))
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
		if _, err := room.Submit(seat, ids); err != nil {
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
