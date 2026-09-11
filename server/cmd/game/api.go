package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	wordarenav1 "github.com/JoTalbot/words/server/gen/wordarena/net/v1"
	"github.com/JoTalbot/words/server/internal/match"
	"github.com/JoTalbot/words/server/internal/matchroom"
	"github.com/JoTalbot/words/server/internal/protocol"
	"github.com/JoTalbot/words/server/internal/security"
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

	stopCh   chan struct{}
	draining atomic.Bool

	// Resource limits (production-shape hardening, M1 prep). Zero means
	// unlimited for maxRooms; maxWSBytes is always enforced when positive.
	maxRooms   int
	maxWSBytes int64

	// results persists the final outcome of finished matches (in-memory,
	// TTL-reaped). Durable match persistence is M1 work.
	results   map[uint64]matchResult
	resultsMu sync.Mutex

	// matchCaps holds the unguessable code and read capability issued when a
	// room is created, keyed by match id, plus the reverse index used to
	// resolve a code back to an id. Both are guarded by resultsMu. They are
	// process state: after a restart the durable match_code / read_capability
	// columns are the source of truth via ResultRepo.GetByCode.
	matchCaps   map[uint64]matchAccess
	resultCodes map[string]uint64

	// requireReadCap disables the sequential-id form of the result and replay
	// endpoints and requires the per-match capability on the code form. Set
	// via WORDARENA_REQUIRE_READ_CAPABILITY; false during the transition
	// window documented in docs/WIRE-PROTOCOL.md.
	requireReadCap bool

	// m holds process-lifetime counters served at GET /metrics
	// (telemetry baseline, M1 prep).
	m        metrics
	stopOnce sync.Once

	// mm pairs waiting players into matches (basic matchmaking, M1).
	mm *matchmaker

	// intentsPerSec caps word intents per seat per second (transport-level
	// abuse guard; does not affect match determinism). 0 disables the limit.
	intentsPerSec int
	// seatWindows holds recent intent timestamps per seat (matchID<<1|seat).
	seatWindows map[uint64][]time.Time
	seatWinMu   sync.Mutex

	// profiles is the player registry (ProfileRepo: in-memory by default,
	// Postgres when WORDARENA_POSTGRES_DSN is set); profiled marks matches
	// that were created with explicit player_ids (stats fold on match end).
	profiles   ProfileRepo
	profiled   map[uint64]bool
	profiledMu sync.Mutex

	// resultRepo is the durable result store (nil = in-memory only). The
	// in-memory a.results cache stays the fast path; resultRepo mirrors
	// writes and serves cache misses (survives restarts).
	resultRepo ResultRepo
	// pgDB is the shared Postgres handle, closed on Stop.
	pgDB *sql.DB

	// tokenTTL bounds seat-token lifetime (0 = never, M0 default). Set via
	// WORDARENA_SEAT_TOKEN_TTL_SECONDS.
	tokenTTL time.Duration

	// telemetry exports optional append-only operational events. It is
	// non-authoritative and must never block match simulation.
	telemetry telemetrySink

	// --- transport-level abuse protection (M1 security hardening) -------
	// These gates may reject a request; they never change what an accepted
	// request means, so match determinism is unaffected.

	// origins decides which browser origins may open a WebSocket.
	origins *security.OriginPolicy
	// mutators rate-limits state-changing calls per caller identity.
	mutators *security.Limiter
	// trustProxy enables X-Forwarded-For based caller identity. It must only
	// be set when the service sits behind a proxy that overwrites the header.
	trustProxy bool
	// allowExplicitSeed permits clients to pin a match seed. Deterministic
	// seeds are the backbone of replay tooling, so the default is permissive
	// on dev deployments; a public edge must set this to false (a pinned
	// seed lets an attacker precompute the whole board).
	allowExplicitSeed bool
	// maxBodyBytes caps a JSON request body.
	maxBodyBytes int64
}

// errRoomCapacity is returned by createRoom when the room cap is reached.
var errRoomCapacity = errors.New("room capacity reached")

// metrics are atomic counters for the telemetry baseline. They are cheap to
// update on the hot path and expose a coarse health/throughput signal.
type metrics struct {
	matchesCreated  atomic.Uint64
	matchesFinished atomic.Uint64
	intentsReceived atomic.Uint64
	wordsAccepted   atomic.Uint64
	wordsRejected   atomic.Uint64
}

// matchResult is the persisted post-match outcome served by
// GET /v1/matches/{id}/result.
type matchResult struct {
	MatchID    uint64        `json:"match_id"`
	Seed       uint64        `json:"seed"`
	Language   string        `json:"language"`
	Over       bool          `json:"over"`
	WinnerSeat int           `json:"winner_seat"` // -1 on tie
	IsTie      bool          `json:"is_tie"`
	Scores     [2]int64      `json:"scores"`
	StateVer   int           `json:"state_version"`
	ServerTick int           `json:"server_tick"`
	Events     []replayEvent `json:"events,omitempty"`

	// Code is the unguessable 128-bit handle clients present instead of the
	// sequential match id (docs/SECURITY-REVIEW-M1.md S-2). Safe to echo:
	// the caller already has it.
	Code string `json:"match_code,omitempty"`
	// ReadCap is the per-match read capability. Deliberately never
	// serialized: echoing it in a result body would hand the credential to
	// anyone who can already read the result, which is what it prevents.
	ReadCap    string    `json:"-"`
	recordedAt time.Time `json:"-"`
}

// replayEvent is a compact, JSON-friendly event-log record for audit and
// deterministic-replay tooling (GET /v1/matches/{id}/replay).
type replayEvent struct {
	Seq        int    `json:"seq"`
	Tick       int    `json:"tick"`
	Seat       int    `json:"seat"`
	CellIDs    []int  `json:"cell_ids"`
	Word       string `json:"word"`
	Result     string `json:"result"`
	ScoreAdded int64  `json:"score_added"`
	TotalScore int64  `json:"total_score"`
	IsSteal    bool   `json:"is_steal"`
	StateVer   int    `json:"state_version"`
}

// resultTTL is how long finished match results are kept in memory.
const resultTTL = 5 * time.Minute

// NewAPI builds the service with in-memory storage. Limits are read from
// environment variables (WORDARENA_MAX_ROOMS, WORDARENA_MAX_WS_BYTES) with
// safe defaults.
func NewAPI() *API {
	return newAPI(newMemProfileStore(), nil, nil)
}

// NewAPIWithPostgres builds the service with durable Postgres storage
// (profiles + match results). It connects, pings and applies the schema
// before returning; the service refuses to start if Postgres is unreachable
// so durability is never silently dropped.
func NewAPIWithPostgres(dsn string) (*API, error) {
	db, err := openPostgres(dsn)
	if err != nil {
		return nil, err
	}
	a := newAPI(&pgProfileStore{db: db}, &pgResultStore{db: db}, db)
	// Resume match ids from the durable store before serving anyone. This is
	// fatal on purpose: continuing at 1 would silently alias finished
	// matches, which is worse than not starting (docs/ARCHITECTURE.md keeps
	// durable reads authoritative for results).
	if err := a.primeMatchIDs(); err != nil {
		a.Stop()
		db.Close()
		return nil, err
	}
	return a, nil
}

// primeMatchIDs advances the match-id counter past the durable high-water
// mark. A no-op when there is no durable result store (ids cannot collide with
// nothing) or when the store is empty.
func (a *API) primeMatchIDs() error {
	if a.resultRepo == nil {
		return nil
	}
	maxID, err := a.resultRepo.MaxMatchID()
	if err != nil {
		return fmt.Errorf("resume match ids: %w", err)
	}
	if maxID == 0 {
		return nil
	}
	if cur := a.counter.Load(); maxID > cur {
		a.counter.Store(maxID)
		log.Printf("match ids resumed from the durable store at %d", maxID)
	}
	return nil
}

// newAPI is the shared constructor. profiles must not be nil; resultRepo and
// pgDB may be nil for memory-only operation.
func newAPI(profiles ProfileRepo, resultRepo ResultRepo, pgDB *sql.DB) *API {
	a := &API{
		rooms:             map[uint64]*matchroom.Room{},
		stopCh:            make(chan struct{}),
		maxRooms:          envInt("WORDARENA_MAX_ROOMS", 128),
		maxWSBytes:        int64(envInt("WORDARENA_MAX_WS_BYTES", 64<<10)),
		results:           map[uint64]matchResult{},
		matchCaps:         map[uint64]matchAccess{},
		resultCodes:       map[string]uint64{},
		requireReadCap:    envBool("WORDARENA_REQUIRE_READ_CAPABILITY", false),
		mm:                newMatchmaker(),
		intentsPerSec:     envInt("WORDARENA_INTENTS_PER_SEC", 60),
		seatWindows:       map[uint64][]time.Time{},
		profiles:          profiles,
		profiled:          map[uint64]bool{},
		resultRepo:        resultRepo,
		pgDB:              pgDB,
		tokenTTL:          time.Duration(envInt("WORDARENA_SEAT_TOKEN_TTL_SECONDS", 0)) * time.Second,
		telemetry:         newTelemetrySinkFromEnv(),
		origins:           security.NewOriginPolicy(os.Getenv("WORDARENA_WS_ALLOWED_ORIGINS")),
		mutators:          security.NewLimiter(envInt("WORDARENA_MUTATIONS_PER_MIN", 120), time.Minute, 0),
		trustProxy:        envBool("WORDARENA_TRUST_PROXY_HEADERS", false),
		allowExplicitSeed: envBool("WORDARENA_ALLOW_EXPLICIT_SEED", true),
		maxBodyBytes:      int64(envInt("WORDARENA_MAX_BODY_BYTES", 16<<10)),
	}
	go a.runReaper()
	return a
}

// runReaper periodically purges abandoned queue entries and stale match
// results so an idle server does not leak memory.
func (a *API) runReaper() {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-a.stopCh:
			return
		case <-t.C:
			a.mm.reap()
			a.reapResults()
			a.mutators.Reap()
		}
	}
}

// envBool parses a boolean environment variable ("1", "true", "yes" are
// true) with a default for unset or unparseable values.
func envBool(key string, def bool) bool {
	if v := os.Getenv(key); v != "" {
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "1", "true", "yes", "on":
			return true
		case "0", "false", "no", "off":
			return false
		}
	}
	return def
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

// Stop signals all room tickers to exit (graceful shutdown). It is safe to
// call multiple times.
func (a *API) Stop() {
	a.stopOnce.Do(func() {
		a.draining.Store(true)
		close(a.stopCh)
		if a.telemetry != nil {
			_ = a.telemetry.Close()
		}
		if a.pgDB != nil {
			_ = a.pgDB.Close()
		}
	})
}

// activeRooms returns the number of live rooms under the rooms lock.
func (a *API) activeRooms() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.rooms)
}

// allowIntent implements the per-seat intent rate limit. It records the
// intent timestamp and reports whether the seat is still within its
// per-second budget. The limit is transport-level abuse protection and never
// influences match determinism (the server remains authoritative either way).
func (a *API) allowIntent(matchID uint64, seat int) bool {
	if a.intentsPerSec <= 0 {
		return true
	}
	key := matchID<<1 | uint64(seat)
	now := time.Now()
	cutoff := now.Add(-time.Second)

	a.seatWinMu.Lock()
	defer a.seatWinMu.Unlock()
	w := a.seatWindows[key]
	kept := w[:0]
	for _, ts := range w {
		if ts.After(cutoff) {
			kept = append(kept, ts)
		}
	}
	if len(kept) >= a.intentsPerSec {
		a.seatWindows[key] = kept
		return false
	}
	a.seatWindows[key] = append(kept, now)
	return true
}

// isProfiledMatch reports whether a match was created with explicit
// player_ids (its seats map to registered profiles).
func (a *API) isProfiledMatch(id uint64) bool {
	a.profiledMu.Lock()
	defer a.profiledMu.Unlock()
	return a.profiled[id]
}

// clearSeatWindows drops the rate-limit state for a finished match.
func (a *API) clearSeatWindows(matchID uint64) {
	a.seatWinMu.Lock()
	delete(a.seatWindows, matchID<<1)
	delete(a.seatWindows, matchID<<1|1)
	a.seatWinMu.Unlock()
}

// reapResults removes match results older than resultTTL. Called by the
// reaper and after each result is recorded.
func (a *API) reapResults() {
	a.resultsMu.Lock()
	defer a.resultsMu.Unlock()
	cutoff := time.Now().Add(-resultTTL)
	for id, old := range a.results {
		if old.recordedAt.Before(cutoff) {
			delete(a.results, id)
		}
	}
}

// matchAccess is the unguessable handle pair issued for one match. Code is
// what a client presents in the URL; ReadCap proves it is entitled to the
// answer. They are separate so a match code can be shared - on a
// post-match screen, in a support ticket, in a URL - without also sharing
// the right to read the score and the full word-by-word replay.
type matchAccess struct {
	Code    string
	ReadCap string
}

// accessFor returns the handle pair issued for a match, or the zero value if
// this process never created it - which is the case for every match that
// finished before a restart.
func (a *API) accessFor(id uint64) matchAccess {
	a.resultsMu.Lock()
	defer a.resultsMu.Unlock()
	return a.matchCaps[id]
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
// tests; random seeds are used when absent. PlayerIDs, when set, bind the
// seats to registered profiles (stats are updated when the match ends).
type createMatchRequest struct {
	Language  string     `json:"language"`
	Seed      *uint64    `json:"seed,omitempty"`
	PlayerIDs *[2]uint64 `json:"player_ids,omitempty"`
	// SuddenDeath enables the opt-in tiebreak (docs/M1-SUDDEN-DEATH.md).
	// Defaults to false: M0 rules apply and ties are draws.
	SuddenDeath bool `json:"sudden_death,omitempty"`
}

// createMatchResponse is the JSON body returned on match creation.
type createMatchResponse struct {
	MatchID     uint64    `json:"match_id"`
	Seed        uint64    `json:"seed"`
	Language    string    `json:"language"`
	SuddenDeath bool      `json:"sudden_death"`
	Tokens      [2]string `json:"tokens"`
	UserIDs     [2]uint64 `json:"user_ids"`
	// MatchCode is the unguessable handle for the result and replay endpoints;
	// ReadCapability is the credential that endpoint requires once
	// WORDARENA_REQUIRE_READ_CAPABILITY is set. Both are returned exactly once,
	// here - the service never echoes the capability again.
	MatchCode      string `json:"match_code"`
	ReadCapability string `json:"read_capability"`
}

// Routes registers the service handlers, wrapped in request logging.
func (a *API) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", a.handleHealthz)
	mux.HandleFunc("/readyz", a.handleReadyz)
	mux.HandleFunc("POST /v1/matches", a.handleCreateMatch)
	mux.HandleFunc("GET /v1/match/ws", a.handleWS)
	mux.HandleFunc("POST /v1/matches/{id}/token/rotate", a.handleTokenRotate)
	mux.HandleFunc("GET /v1/match/{id}/snapshot", a.handleSnapshot)
	mux.HandleFunc("GET /v1/matches/{id}/result", a.handleResult)
	mux.HandleFunc("GET /v1/matches/{id}/replay", a.handleReplay)
	mux.HandleFunc("POST /v1/queue", a.handleQueueCreate)
	mux.HandleFunc("GET /v1/queue/{id}", a.handleQueuePoll)
	mux.HandleFunc("POST /v1/players", a.handlePlayerCreate)
	mux.HandleFunc("GET /v1/players/{id}", a.handlePlayerGet)
	mux.HandleFunc("GET /metrics", a.handleMetrics)
	mux.HandleFunc("GET /metrics/prometheus", a.handlePrometheusMetrics)
	return requestLogger(mux)
}

// handleMetrics serves the JSON telemetry counters.
func (a *API) handleMetrics(w http.ResponseWriter, _ *http.Request) {
	m := a.metricsSnapshot()
	writeJSON(w, http.StatusOK, map[string]any{
		"matches_created":           m.MatchesCreated,
		"matches_finished":          m.MatchesFinished,
		"intents_received":          m.IntentsReceived,
		"words_accepted":            m.WordsAccepted,
		"words_rejected":            m.WordsRejected,
		"active_matches":            m.ActiveMatches,
		"telemetry_events_enqueued": m.TelemetryEventsEnqueued,
		"telemetry_events_written":  m.TelemetryEventsWritten,
		"telemetry_events_dropped":  m.TelemetryEventsDropped,
		"telemetry_export_errors":   m.TelemetryExportErrors,
	})
}

func (a *API) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

// handleReadyz is the orchestration readiness endpoint. It is distinct from
// /healthz: a process can be alive while draining or while durable storage is
// unavailable. Readiness failures return 503 so load balancers stop sending
// new work before shutdown or during backend incidents.
func (a *API) handleReadyz(w http.ResponseWriter, r *http.Request) {
	if a.draining.Load() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"status": "draining",
		})
		return
	}
	storage := "memory"
	if a.pgDB != nil {
		storage = "postgres"
		ctx, cancel := context.WithTimeout(r.Context(), 500*time.Millisecond)
		defer cancel()
		if err := a.pgDB.PingContext(ctx); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{
				"status":  "not_ready",
				"storage": storage,
				"error":   "storage_unavailable",
			})
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":         "ready",
		"storage":        storage,
		"active_matches": a.activeRooms(),
	})
}

func (a *API) rejectIfDraining(w http.ResponseWriter) bool {
	if !a.draining.Load() {
		return false
	}
	httpError(w, http.StatusServiceUnavailable, "server is draining")
	return true
}

// decodeJSON reads a request body into v with a hard byte cap. Without the
// cap a single request could make the service allocate arbitrarily much before
// the decoder rejects it.
func (a *API) decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	body := r.Body
	if a.maxBodyBytes > 0 {
		body = http.MaxBytesReader(w, r.Body, a.maxBodyBytes)
	}
	dec := json.NewDecoder(body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		httpError(w, http.StatusBadRequest, "invalid json body")
		return false
	}
	return true
}

// allowMutation applies the per-caller limit to unauthenticated, state
// creating endpoints. Authenticated seat traffic is bounded separately by
// the per-seat intent limit, so a NATed household of players is not queued
// behind the creation budget.
func (a *API) allowMutation(w http.ResponseWriter, r *http.Request) bool {
	if a.mutators == nil {
		return true
	}
	key := security.ClientIP(r, a.trustProxy)
	if a.mutators.Allow(key) {
		return true
	}
	if d := a.mutators.RetryAfter(key); d > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(d.Seconds()))))
	}
	httpError(w, http.StatusTooManyRequests, "too many requests from this caller")
	return false
}

func (a *API) handleCreateMatch(w http.ResponseWriter, r *http.Request) {
	if a.rejectIfDraining(w) {
		return
	}
	if !a.allowMutation(w, r) {
		return
	}
	var req createMatchRequest
	if !a.decodeJSON(w, r, &req) {
		return
	}
	// A pinned seed is a replay tool on a dev deployment and a fairness hole
	// on a public one: whoever fixes the seed already knows the board.
	if req.Seed != nil && !a.allowExplicitSeed {
		httpError(w, http.StatusBadRequest, "explicit seed is not accepted by this deployment")
		return
	}
	lang := req.Language
	if lang == "" {
		lang = "en"
	}
	switch lang {
	case "en", "ru", "uk":
	default:
		httpError(w, http.StatusBadRequest, "language must be en, ru or uk")
		return
	}
	if req.PlayerIDs != nil {
		if req.PlayerIDs[0] == 0 || req.PlayerIDs[1] == 0 || req.PlayerIDs[0] == req.PlayerIDs[1] {
			httpError(w, http.StatusBadRequest, "player_ids must be two distinct positive ids")
			return
		}
		if _, ok, err := a.profiles.Get(req.PlayerIDs[0]); err != nil {
			httpError(w, http.StatusInternalServerError, "profile store error")
			return
		} else if !ok {
			httpError(w, http.StatusNotFound, "player 0 not found")
			return
		}
		if _, ok, err := a.profiles.Get(req.PlayerIDs[1]); err != nil {
			httpError(w, http.StatusInternalServerError, "profile store error")
			return
		} else if !ok {
			httpError(w, http.StatusNotFound, "player 1 not found")
			return
		}
	}
	id, seed, tokens, userIDs, access, err := a.createRoom(lang, req.Seed, req.PlayerIDs, req.SuddenDeath)
	if err != nil {
		if errors.Is(err, errRoomCapacity) {
			httpError(w, http.StatusTooManyRequests, "too many active matches")
			return
		}
		log.Printf("create match failed: %v", err)
		httpError(w, http.StatusInternalServerError, "match creation failed")
		return
	}
	writeJSON(w, http.StatusCreated, createMatchResponse{
		MatchID: id, Seed: seed, Language: lang, SuddenDeath: req.SuddenDeath,
		Tokens: tokens, UserIDs: userIDs,
		MatchCode: access.Code, ReadCapability: access.ReadCap,
	})
}

// createRoom provisions a new live room and returns its public join info.
// It is shared by the direct-create endpoint and the matchmaker. playerIDs,
// when non-nil, bind the seats to registered profiles. suddenDeath enables
// the opt-in tiebreak (docs/M1-SUDDEN-DEATH.md).
func (a *API) createRoom(lang string, seed *uint64, playerIDs *[2]uint64, suddenDeath bool) (uint64, uint64, [2]string, [2]uint64, matchAccess, error) {
	var s uint64
	if seed != nil {
		s = *seed
	} else {
		var err error
		if s, err = randomSeed(); err != nil {
			return 0, 0, [2]string{}, [2]uint64{}, matchAccess{}, err
		}
	}
	tok0, err := randomHex(16)
	if err != nil {
		return 0, 0, [2]string{}, [2]uint64{}, matchAccess{}, err
	}
	tok1, err := randomHex(16)
	if err != nil {
		return 0, 0, [2]string{}, [2]uint64{}, matchAccess{}, err
	}
	// The match code and the read capability are 128 bits of crypto/rand each.
	// They are issued here rather than at result time so a client can be given
	// the handle for a match that has not finished yet, and so the pair exists
	// even for a match that never completes.
	code, err := randomHex(16)
	if err != nil {
		return 0, 0, [2]string{}, [2]uint64{}, matchAccess{}, err
	}
	readCap, err := randomHex(16)
	if err != nil {
		return 0, 0, [2]string{}, [2]uint64{}, matchAccess{}, err
	}
	access := matchAccess{Code: code, ReadCap: readCap}

	id := a.counter.Add(1)
	a.resultsMu.Lock()
	a.matchCaps[id] = access
	a.resultCodes[code] = id
	a.resultsMu.Unlock()
	userIDs := [2]uint64{id*2 + 1, id*2 + 2} // deterministic synthetic ids
	if playerIDs != nil {
		// Bind seats that supplied a profile; seats without one (zero)
		// keep their synthetic id so user ids stay non-zero on the wire.
		for i := range userIDs {
			if playerIDs[i] != 0 {
				userIDs[i] = playerIDs[i]
			}
		}
	}
	room, err := matchroom.New(matchroom.Config{
		MatchID:     id,
		Seed:        s,
		Language:    lang,
		UserIDs:     userIDs,
		Token0:      tok0,
		Token1:      tok1,
		SuddenDeath: suddenDeath,
		TokenTTL:    a.tokenTTL,
	})
	if err != nil {
		return 0, 0, [2]string{}, [2]uint64{}, matchAccess{}, err
	}
	a.mu.Lock()
	if a.maxRooms > 0 && len(a.rooms) >= a.maxRooms {
		a.mu.Unlock()
		return 0, 0, [2]string{}, [2]uint64{}, matchAccess{}, errRoomCapacity
	}
	a.rooms[id] = room
	a.mu.Unlock()
	if playerIDs != nil {
		a.profiledMu.Lock()
		a.profiled[id] = true
		a.profiledMu.Unlock()
	}
	a.m.matchesCreated.Add(1)
	a.publishTelemetry(telemetryEvent{
		Type:        "match_created",
		MatchID:     id,
		Seed:        s,
		Language:    lang,
		SuddenDeath: suddenDeath,
	})
	go a.runRoomTicker(id, room)
	return id, s, [2]string{tok0, tok1}, userIDs, access, nil
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
				a.clearSeatWindows(id)
				a.profiledMu.Lock()
				delete(a.profiled, id)
				a.profiledMu.Unlock()
				room.Close()
				return
			}
		}
	}
}

// handleTokenRotate rotates a seat token: the caller presents the current
// token (proving they hold it) and receives a fresh one; the presented
// token stops authenticating. This bounds the exposure window of seat
// credentials. With WORDARENA_SEAT_TOKEN_TTL_SECONDS set, rotation also
// refreshes the expiry deadline.
func (a *API) handleTokenRotate(w http.ResponseWriter, r *http.Request) {
	if a.rejectIfDraining(w) {
		return
	}
	id, err := parseID(r.PathValue("id"))
	if err != nil {
		httpError(w, http.StatusBadRequest, "invalid match id")
		return
	}
	var req struct {
		Token string `json:"token"`
	}
	if !a.decodeJSON(w, r, &req) || req.Token == "" {
		httpError(w, http.StatusBadRequest, "token required")
		return
	}
	a.mu.Lock()
	room, ok := a.rooms[id]
	a.mu.Unlock()
	if !ok {
		httpError(w, http.StatusNotFound, "match not found or finished")
		return
	}
	seat, ok := room.SeatForToken(req.Token)
	if !ok {
		httpError(w, http.StatusUnauthorized, "invalid or expired token")
		return
	}
	next, err := randomHex(16)
	if err != nil {
		httpError(w, http.StatusInternalServerError, "token generation failed")
		return
	}
	room.SetToken(seat, next)
	a.publishTelemetry(telemetryEvent{
		Type:    "seat_token_rotated",
		MatchID: id,
		Seat:    telemetryInt(int(seat)),
		UserID:  room.UserID(seat),
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"match_id": id,
		"seat":     int(seat),
		"user_id":  room.UserID(seat),
		"token":    next,
	})
}

// handleSnapshot serves the authoritative board of a live match. It is the
// one read endpoint that exposes in-progress game state, so it requires a
// seat credential: with sequential match ids an unauthenticated observer could
// otherwise enumerate live matches and watch every board, lock and score.
// That is a cheating channel, not just a privacy leak (docs/ARCHITECTURE.md
// makes the server the only source of competitive truth, and a spectator feed
// must be an explicit product decision, never a side effect).
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
	tok := bearerToken(r)
	if tok == "" {
		httpError(w, http.StatusUnauthorized, "seat token required: send Authorization: Bearer <token> or ?token=<token>")
		return
	}
	if _, ok := room.SeatForToken(tok); !ok {
		httpError(w, http.StatusForbidden, "invalid or expired seat token")
		return
	}
	dsnap := room.Match().Snapshot()
	snap := protocol.SnapshotToProto(dsnap, [2]uint64{room.UserID(0), room.UserID(1)})
	writeJSON(w, http.StatusOK, map[string]any{
		"server_tick":   snap.ServerTick,
		"wave":          snap.CurrentWave,
		"state_version": snap.StateVersion,
		"phase":         dsnap.Phase,
		"sudden_death":  dsnap.SuddenDeath,
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

// lookupResult serves a finished match result from the in-memory cache,
// falling back to the durable result store on a miss (survives restarts).
// A durable hit is cached so subsequent reads stay fast.
func (a *API) lookupResult(id uint64) (matchResult, bool) {
	a.resultsMu.Lock()
	res, ok := a.results[id]
	a.resultsMu.Unlock()
	if ok {
		return res, true
	}
	if a.resultRepo == nil {
		return matchResult{}, false
	}
	res, ok, err := a.resultRepo.Get(id)
	if err != nil {
		log.Printf("result lookup=store_error id=%d err=%v", id, err)
		return matchResult{}, false
	}
	if !ok {
		return matchResult{}, false
	}
	a.resultsMu.Lock()
	if _, exists := a.results[id]; !exists {
		a.results[id] = res
	}
	a.resultsMu.Unlock()
	return res, true
}

// isNumericRef reports whether a path segment is a legacy sequential match id
// rather than a match code. Codes are hex from crypto/rand and 32 characters
// long, so a purely decimal segment of any other length is not one either.
func isNumericRef(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// lookupResultByCode resolves an unguessable match code, consulting the
// in-process index first and the durable store on a miss so a code issued
// before a restart still works.
func (a *API) lookupResultByCode(code string) (matchResult, bool) {
	a.resultsMu.Lock()
	id, ok := a.resultCodes[code]
	a.resultsMu.Unlock()
	if ok {
		return a.lookupResult(id)
	}
	if a.resultRepo == nil {
		return matchResult{}, false
	}
	res, ok, err := a.resultRepo.GetByCode(code)
	if err != nil {
		log.Printf("result lookup=store_error_by_code err=%v", err)
		return matchResult{}, false
	}
	if !ok {
		return matchResult{}, false
	}
	a.resultsMu.Lock()
	if _, exists := a.results[res.MatchID]; !exists {
		a.results[res.MatchID] = res
	}
	if res.Code != "" {
		a.resultCodes[res.Code] = res.MatchID
	}
	a.resultsMu.Unlock()
	return res, true
}

// resolveMatchRef resolves the {id} path segment of the result and replay
// endpoints. The segment is either a match code - the intended form - or, while
// the transition window in docs/WIRE-PROTOCOL.md is open, a legacy sequential
// match id.
//
// With WORDARENA_REQUIRE_READ_CAPABILITY=true the numeric form is refused
// outright. It answers 404 rather than 400 on purpose: a distinct status would
// confirm that a given number is shaped like a live id and let a caller measure
// the counter, which is the enumeration S-2 describes.
func (a *API) resolveMatchRef(ref string) (matchResult, bool) {
	if isNumericRef(ref) {
		if a.requireReadCap {
			return matchResult{}, false
		}
		id, err := parseID(ref)
		if err != nil {
			return matchResult{}, false
		}
		return a.lookupResult(id)
	}
	return a.lookupResultByCode(ref)
}

// readCapSatisfied reports whether the request carries the per-match read
// capability. The check is constant-time: the capability is a bearer credential
// and a byte-at-a-time comparison would let a caller recover it by timing.
func (a *API) readCapSatisfied(r *http.Request, res matchResult) bool {
	if !a.requireReadCap {
		return true
	}
	if res.ReadCap == "" {
		// A row written before this change carries no capability. Refusing is
		// the safe default; those matches predate the guarantee.
		return false
	}
	got := bearerToken(r)
	if got == "" {
		got = r.URL.Query().Get("cap")
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(res.ReadCap)) == 1
}

// handleResult serves the persisted outcome of a finished match.
func (a *API) handleResult(w http.ResponseWriter, r *http.Request) {
	res, ok := a.resolveMatchRef(r.PathValue("id"))
	if !ok {
		httpError(w, http.StatusNotFound, "result not found or match still active")
		return
	}
	if !a.readCapSatisfied(r, res) {
		httpError(w, http.StatusUnauthorized, "read capability required: send Authorization: Bearer <read_capability> or ?cap=<read_capability>")
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// handleReplay serves the full deterministic event log of a finished match
// (audit + replay tooling).
func (a *API) handleReplay(w http.ResponseWriter, r *http.Request) {
	res, ok := a.resolveMatchRef(r.PathValue("id"))
	if !ok {
		httpError(w, http.StatusNotFound, "replay not found or match still active")
		return
	}
	if !a.readCapSatisfied(r, res) {
		httpError(w, http.StatusUnauthorized, "read capability required: send Authorization: Bearer <read_capability> or ?cap=<read_capability>")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"match_id": res.MatchID,
		"seed":     res.Seed,
		"language": res.Language,
		"over":     res.Over,
		"scores":   res.Scores,
		"events":   res.Events,
	})
}

// handleQueueCreate enqueues a player for basic 1v1 matchmaking. An optional
// player_id binds the queue entry to a registered profile so the eventual
// match folds its outcome into that profile's stats.
func (a *API) handleQueueCreate(w http.ResponseWriter, r *http.Request) {
	if a.rejectIfDraining(w) {
		return
	}
	if !a.allowMutation(w, r) {
		return
	}
	var req struct {
		Language string `json:"language"`
		PlayerID uint64 `json:"player_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	lang := req.Language
	if lang == "" {
		lang = "en"
	}
	switch lang {
	case "en", "ru", "uk":
	default:
		httpError(w, http.StatusBadRequest, "language must be en, ru or uk")
		return
	}
	if req.PlayerID != 0 {
		if _, ok, err := a.profiles.Get(req.PlayerID); err != nil {
			httpError(w, http.StatusInternalServerError, "profile store error")
			return
		} else if !ok {
			httpError(w, http.StatusNotFound, "player not found")
			return
		}
	}
	e := a.mm.enqueue(lang, req.PlayerID, func(l string, pids *[2]uint64) (uint64, uint64, [2]string, [2]uint64, matchAccess, error) {
		return a.createRoom(l, nil, pids, false)
	})
	writeJSON(w, http.StatusAccepted, e)
}

// handleQueuePoll returns the current queue entry state (waiting / matched /
// expired).
func (a *API) handleQueuePoll(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	e, ok := a.mm.poll(id)
	if !ok {
		httpError(w, http.StatusNotFound, "queue entry not found")
		return
	}
	if e.Status == "expired" {
		httpError(w, http.StatusGone, "queue entry expired")
		return
	}
	writeJSON(w, http.StatusOK, e)
}

// handlePlayerCreate registers a player profile (M1 player profile).
func (a *API) handlePlayerCreate(w http.ResponseWriter, r *http.Request) {
	if a.rejectIfDraining(w) {
		return
	}
	if !a.allowMutation(w, r) {
		return
	}
	var req struct {
		Nickname string `json:"nickname"`
		Language string `json:"language"`
	}
	if !a.decodeJSON(w, r, &req) {
		return
	}
	nick := req.Nickname
	// Character policy lives in one place so the same rule applies to the
	// in-memory and Postgres backends: no control characters (they would
	// forge log lines), no spaces (the UI renders names inline), letters and
	// digits plus _-. only, Latin or Cyrillic.
	if !security.ValidateNickname(nick) {
		httpError(w, http.StatusBadRequest, "nickname must be 1..32 characters using letters, digits, '-', '_' or '.'")
		return
	}
	lang := req.Language
	if lang == "" {
		lang = "en"
	}
	switch lang {
	case "en", "ru", "uk":
	default:
		httpError(w, http.StatusBadRequest, "language must be en, ru or uk")
		return
	}
	p, err := a.profiles.Create(nick, lang)
	if err != nil {
		httpError(w, http.StatusInternalServerError, "profile store error")
		return
	}
	a.publishTelemetry(telemetryEvent{Type: "profile_created", UserID: p.ID, Language: p.Language})
	writeJSON(w, http.StatusCreated, p)
}

// handlePlayerGet serves one player profile plus lifetime stats.
func (a *API) handlePlayerGet(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r.PathValue("id"))
	if err != nil {
		httpError(w, http.StatusBadRequest, "invalid player id")
		return
	}
	p, ok, err := a.profiles.Get(id)
	if err != nil {
		httpError(w, http.StatusInternalServerError, "profile store error")
		return
	}
	if !ok {
		httpError(w, http.StatusNotFound, "player not found")
		return
	}
	writeJSON(w, http.StatusOK, p)
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
	// Carry the handle pair into the durable row so the read stays possible
	// after a restart, when the in-memory matchCaps entry is gone. This is the
	// piece S-2 identified as needing a schema change rather than a gate.
	if acc := a.accessFor(m.ID); acc.Code != "" {
		res.Code = acc.Code
		res.ReadCap = acc.ReadCap
	}
	for _, ev := range m.Events() {
		res.Events = append(res.Events, replayEvent{
			Seq: ev.Seq, Tick: ev.Tick, Seat: int(ev.Seat), CellIDs: ev.CellIDs,
			Word: ev.Word, Result: ev.WordResultString, ScoreAdded: ev.ScoreAdded,
			TotalScore: ev.TotalScore, IsSteal: ev.IsSteal, StateVer: ev.StateVersion,
		})
	}
	a.resultsMu.Lock()
	if _, exists := a.results[res.MatchID]; !exists {
		a.results[res.MatchID] = res
	}
	a.resultsMu.Unlock()
	a.reapResults()
	a.m.matchesFinished.Add(1)
	a.publishTelemetry(telemetryEvent{
		Type:         "match_finished",
		MatchID:      res.MatchID,
		Seed:         res.Seed,
		Language:     res.Language,
		WinnerSeat:   telemetryInt(res.WinnerSeat),
		IsTie:        telemetryBool(res.IsTie),
		Score0:       telemetryInt64(res.Scores[0]),
		Score1:       telemetryInt64(res.Scores[1]),
		StateVersion: telemetryInt(res.StateVer),
		ServerTick:   telemetryInt(res.ServerTick),
	})

	// Mirror the finished result to durable storage (if configured). The
	// in-memory cache above stays the fast path; a miss reads through here.
	if a.resultRepo != nil {
		if err := a.resultRepo.Put(res); err != nil {
			log.Printf("match lifecycle=result_store_error id=%d err=%v", res.MatchID, err)
		}
	}

	// Fold the outcome into profile stats when the match used explicit
	// player_ids (anonymous/synthetic seats are no-ops in the profile store).
	if a.isProfiledMatch(res.MatchID) {
		for seat := match.Seat(0); seat < 2; seat++ {
			outcome := "loss"
			switch {
			case res.IsTie:
				outcome = "draw"
			case int(res.WinnerSeat) == int(seat):
				outcome = "win"
			}
			if err := a.profiles.Record(room.UserID(seat), m.Score(seat), outcome); err != nil {
				log.Printf("match lifecycle=profile_store_error id=%d seat=%d err=%v",
					res.MatchID, seat, err)
			}
		}
	}

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
	if a.rejectIfDraining(w) {
		return
	}
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

	// Origin gate. Native clients send no Origin header and are unaffected;
	// a browser page from a foreign site is refused even if it holds a valid
	// token, which is what makes the token-leak blast radius finite.
	if !a.origins.Allow(r) {
		httpError(w, http.StatusForbidden, "origin not allowed")
		return
	}
	conn, err := websocket.Accept(w, r, a.wsAcceptOptions())
	if err != nil {
		return
	}
	if a.maxWSBytes > 0 {
		conn.SetReadLimit(a.maxWSBytes)
	}
	a.publishTelemetry(telemetryEvent{Type: "ws_connected", MatchID: id, Seat: telemetryInt(int(seat)), UserID: room.UserID(seat)})
	defer a.publishTelemetry(telemetryEvent{Type: "ws_disconnected", MatchID: id, Seat: telemetryInt(int(seat)), UserID: room.UserID(seat)})
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
		if !a.allowIntent(id, int(seat)) {
			a.publishTelemetry(telemetryEvent{
				Type:           "intent_rate_limited",
				MatchID:        id,
				Seat:           telemetryInt(int(seat)),
				UserID:         room.UserID(seat),
				ClientSequence: telemetryUint32(sw.ClientSequence),
			})
			_ = conn.Close(websocket.StatusPolicyViolation, "intent rate limit exceeded")
			break
		}
		frame, err := room.SubmitWithSeq(seat, sw.ClientSequence, ids)
		if err != nil {
			_ = conn.Close(websocket.StatusInternalError, "submit failed")
			break
		}
		a.m.intentsReceived.Add(1)
		if frame.Result == match.ResultAccepted {
			a.m.wordsAccepted.Add(1)
		} else {
			a.m.wordsRejected.Add(1)
		}
		a.publishTelemetry(telemetryEvent{
			Type:           "word_validated",
			MatchID:        id,
			Seat:           telemetryInt(int(seat)),
			UserID:         room.UserID(seat),
			ClientSequence: telemetryUint32(frame.ClientSeq),
			Word:           frame.Word,
			Result:         wordResultName(frame.Result),
			ScoreAdded:     telemetryInt64(frame.ScoreAdded),
			TotalScore:     telemetryInt64(frame.TotalScore),
			IsSteal:        telemetryBool(frame.IsSteal),
			StateVersion:   telemetryInt(frame.StateVer),
			ServerTick:     telemetryInt(frame.Tick),
		})
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

// bearerToken extracts a seat credential from the Authorization header,
// falling back to the query parameter used by WebSocket and the M0 web page.
// The header is preferred so tokens stop appearing in URL-shaped logs.
func bearerToken(r *http.Request) string {
	if h := r.Header.Get("Authorization"); h != "" {
		if len(h) >= 7 && strings.EqualFold(h[:7], "bearer ") {
			return strings.TrimSpace(h[7:])
		}
	}
	return r.URL.Query().Get("token")
}

// wsAcceptOptions returns the handshake options for an upgrade that has
// already passed the service origin gate.
//
// InsecureSkipVerify is set because the decision is made once, in
// OriginPolicy, against the deployment allowlist (including loopback
// origins, which coder/websocket would otherwise reject for the web-m0 proof
// page). Duplicating the rule as a second pattern list is how the two
// definitions would drift apart; the single gate is enforced before Accept
// and is covered directly by TestWSOriginPolicy.
func (a *API) wsAcceptOptions() *websocket.AcceptOptions {
	return &websocket.AcceptOptions{InsecureSkipVerify: true}
}

func wordResultName(r match.WordResult) string {
	switch r {
	case match.ResultAccepted:
		return "accepted"
	case match.ResultRejectedNotInDict:
		return "rejected_not_in_dict"
	case match.ResultBlockedByRule:
		return "blocked_by_rule"
	case match.ResultInvalidInput:
		return "invalid_input"
	case match.ResultMatchNotActive:
		return "match_not_active"
	default:
		return "unspecified"
	}
}

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
