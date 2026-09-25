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
	"io"
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
	"github.com/JoTalbot/words/server/internal/pve"
	"github.com/JoTalbot/words/server/internal/security"
	"github.com/coder/websocket"
	"google.golang.org/protobuf/proto"
)

// seatWindowKey identifies one seat of one match.
//
// The key was once the packed integer matchID<<1|seat, which is unique only for
// a two-seat roster. At n seats, match m seat s packed to the same integer as
// match m+1 seat s-2, so two unrelated matches shared one per-second budget and
// a seat could be throttled by activity in another match entirely. The same
// packing let clearSeatWindows name only seats 0 and 1, so every higher seat's
// timestamps stayed in the map for the life of the process - a leak that grows
// with the intents of every roster above two seats. A struct key cannot
// collide, and it lets cleanup name exactly the seats of the match that ended.
type seatWindowKey struct {
	matchID uint64
	seat    int
}

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
	// seatWindows holds recent intent timestamps per seat, keyed by the exact
	// (match, seat) pair. See seatWindowKey for why the key is not an integer.
	seatWindows map[seatWindowKey][]time.Time
	seatWinMu   sync.Mutex

	// behavior holds per-seat behavioral anti-cheat signals (M3 batch 40A,
	// docs/M3-ANTI-CHEAT.md). Measurement only: it never influences
	// admission, scoring, ordering or outcome - the signals exist so a
	// future owner-gated enforcement policy has evidence to weigh and so
	// an operator sees abuse patterns live.
	behavior behaviorTracker

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
	// guilds is the guild registry (M2 batch 32G): memory by default, Postgres
	// when a DSN is set. Guild mutations authenticate with a profile owner
	// token, never with a caller-supplied player id.
	guilds GuildRepo
	// pve maps a match id to the server-driven opponent playing one of its
	// seats (M2 batch 32E). Empty for every match that is not a practice match.
	pveMu sync.Mutex
	pve   map[uint64]pveOpponent
	// allowBotSeats permits a caller to DECLARE seats as simulated players
	// (M2 batch 32D, Q5). Default false: declaring a bot is a QA/tooling and
	// practice capability, not something an arbitrary caller should be able to
	// assert about a match. It cannot be used to hide a human either - the
	// declaration only ever ADDS a disclosure, and no request field can clear
	// one. See docs/M2-BOT-POLICY.md.
	allowBotSeats bool
	// maxSeats caps the roster size a client may request through
	// POST /v1/matches. Batch 32B: the Royale roster path exists and is
	// tested, but a 60-seat match created through an unauthenticated endpoint
	// is a different resource proposition from a 1v1 one (60 seat tokens and
	// 60 WebSocket connections per request), and the abuse review in
	// docs/SECURITY-EXPOSURE.md sized the creation limiter for a single
	// developer deployment. The default therefore stays at 1v1 and an operator
	// opts in explicitly via WORDARENA_MAX_SEATS. This is a deployment
	// default, not a gameplay limit: match.MaxSeats remains the hard bound.
	maxSeats int
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

	// intentProcess is the server-side processing histogram for word intents
	// (M2 batch 35C): microseconds spent inside the authoritative
	// SubmitWithSeq call, excluding socket and frame-cadence wait. It lets a
	// latency run separate "the server is slow" from "the path is long"
	// (docs/M2-LOAD-TESTING.md, generator off-box stage).
	intentProcess intentHist

	// wordLen is the M3 batch 42C word-length-by-outcome histogram: the
	// submitted path length (cells) split by the authoritative outcome class.
	// It is the measurement half of the "cell-path geometry vs dictionary
	// structure" candidate - how often a seat submits implausibly long words,
	// and how those land. Measurement only.
	wordLen wordLenHist
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

	// Bots lists the seats played by declared simulated players, and
	// BotPresent is the same fact in the cheapest possible form
	// (M2 batch 32D, Q5). They are recorded in the authoritative result, not
	// only in the live stream, because the disclosure has to outlive the room:
	// a rating or a reward is computed from this row, possibly days later.
	Bots       []int `json:"bots,omitempty"`
	BotPresent bool  `json:"bot_present"`
	// RatingEligible is false for any match that involved a declared bot.
	// Ratings and rewards do not exist yet (M3), so this field is the rule
	// made enforceable now instead of a promise: when they arrive, the
	// disqualifying fact is already in the durable row rather than in
	// somebody's memory of which queue a match came from.
	RatingEligible bool `json:"rating_eligible"`

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
	// CatchUpBonus is the anti-snowball bonus folded into ScoreAdded, or
	// absent when the opt-in rule (docs/M2-ANTI-SNOWBALL.md) did not apply.
	// Recorded rather than inferred so a replay can prove WHY a score
	// differs, not only that it does.
	CatchUpBonus int64 `json:"catch_up_bonus,omitempty"`
}

// resultTTL is how long finished match results are kept in memory.
const resultTTL = 5 * time.Minute

// serverProtocolVersion is the wire protocol version this server speaks. It is
// echoed in ServerHello during the connect handshake (M2 batch 33). A client
// that declares a newer version than this falls back to what the server
// supports; only one version exists today.
const serverProtocolVersion = 1

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
	// Guilds and owner tokens share the pool. Without this the Postgres
	// deployment would keep guilds in memory and lose them on restart, which is
	// exactly the failure this batch exists to prevent.
	a.guilds = newPgGuildStore(db)
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
		pve:               map[uint64]pveOpponent{},
		stopCh:            make(chan struct{}),
		maxRooms:          envInt("WORDARENA_MAX_ROOMS", 128),
		maxWSBytes:        int64(envInt("WORDARENA_MAX_WS_BYTES", 64<<10)),
		results:           map[uint64]matchResult{},
		matchCaps:         map[uint64]matchAccess{},
		resultCodes:       map[string]uint64{},
		requireReadCap:    envBool("WORDARENA_REQUIRE_READ_CAPABILITY", false),
		mm:                newMatchmaker(),
		intentsPerSec:     envInt("WORDARENA_INTENTS_PER_SEC", 60),
		seatWindows:       map[seatWindowKey][]time.Time{},
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
		maxSeats:          envInt("WORDARENA_MAX_SEATS", match.MinSeats),
		allowBotSeats:     envBool("WORDARENA_ALLOW_BOT_SEATS", false),
		maxBodyBytes:      int64(envInt("WORDARENA_MAX_BODY_BYTES", 16<<10)),
	}
	// Guilds default to memory so a deployment without a DSN still has the
	// feature; NewAPIWithPostgres replaces this with the durable backend.
	if a.guilds == nil {
		a.guilds = newMemGuildStore()
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
			// Give partial Royale lobbies a chance to start short-handed
			// before reaping: pairing otherwise only happens on enqueue,
			// so a lobby that stopped receiving arrivals would expire
			// instead of starting (batch 31C).
			a.mm.tryFormLobbies(a.queueRoomFactory())
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
	key := seatWindowKey{matchID: matchID, seat: seat}
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
		a.behavior.rateLimited.Add(1)
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

// clearSeatWindows drops the rate-limit state for a finished match. The roster
// size is required, because seats are no longer packed into an integer a caller
// could enumerate from the match id alone.
func (a *API) clearSeatWindows(matchID uint64, seats int) {
	a.seatWinMu.Lock()
	defer a.seatWinMu.Unlock()
	for seat := 0; seat < seats; seat++ {
		delete(a.seatWindows, seatWindowKey{matchID: matchID, seat: seat})
	}
	a.behavior.clear(matchID, seats)
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
	// Seats requests a roster size (M2 batch 32B). Absent or zero means 1v1,
	// so every existing client keeps the match it always got. The request is
	// bounded by match.MaxSeats and, more tightly, by the deployment's own
	// WORDARENA_MAX_SEATS - see API.maxSeats for why the default is 2.
	Seats int `json:"seats,omitempty"`
	// BotSeats declares seats played by simulated players (M2 batch 32D, Q5).
	// Gated by WORDARENA_ALLOW_BOT_SEATS (default false). Only a declaration
	// can make a seat a bot; there is no field that makes one stop being one.
	BotSeats []int `json:"bot_seats,omitempty"`
	// PvE asks for a practice match: a 1v1 against an opponent the SERVER
	// drives (M2 batch 32E), so one player can play alone with no second
	// client and no external tooling. The opponent is a declared bot, so the
	// match is disclosed and is not rating-eligible - which is exactly what
	// product decision Q5 makes safe. Gated by WORDARENA_ALLOW_BOT_SEATS.
	PvE bool `json:"pve,omitempty"`
	// AntiSnowball enables the opt-in catch-up rule for this match
	// (docs/M2-ANTI-SNOWBALL.md). Default false: the M0/M1 scoring contract
	// is untouched and no baseline, replay or device assertion moves. Like
	// sudden_death this is a per-match fairness option, not a deployment
	// capability, so it is not gated by an environment variable; the rule's
	// constants are code-level calibration candidates (PD-007), not request
	// fields.
	AntiSnowball bool `json:"anti_snowball,omitempty"`
	// PvEDifficulty names the practice opponent's preset (M2 batch 35B):
	// "easy" (the shipped 32E opponent, and the default), "normal" or
	// "hard" (server/internal/pve/presets.go). It requires pve - a
	// difficulty on a match with no server-driven opponent is a contract
	// error, not a silently ignored field. Like pve it is gated by
	// WORDARENA_ALLOW_BOT_SEATS.
	PvEDifficulty string `json:"pve_difficulty,omitempty"`
}

// createMatchResponse is the JSON body returned on match creation.
type createMatchResponse struct {
	MatchID     uint64 `json:"match_id"`
	Seed        uint64 `json:"seed"`
	Language    string `json:"language"`
	SuddenDeath bool   `json:"sudden_death"`
	// AntiSnowball echoes the catch-up rule the created match will run under
	// (docs/M2-ANTI-SNOWBALL.md), so a caller never has to guess whether the
	// scores it reads back include catch-up bonuses.
	AntiSnowball bool     `json:"anti_snowball"`
	Tokens       []string `json:"tokens"`
	UserIDs      []uint64 `json:"user_ids"`
	// Seats is the roster size of the created match. Batch 32B widened the
	// two-seat response to a roster: the JSON is unchanged for 1v1 (a
	// two-element array either way), and a client can now size its seat loop
	// from the response instead of assuming two.
	Seats int `json:"seats"`
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
	// Guilds (M2 batch 32G, docs/M2-GUILDS.md). Reads are public; every
	// mutation authenticates with X-Player-Token.
	mux.HandleFunc("POST /v1/guilds", a.handleGuildCreate)
	mux.HandleFunc("GET /v1/guilds/{id}", a.handleGuildGet)
	mux.HandleFunc("POST /v1/guilds/{id}/members", a.handleGuildJoin)
	mux.HandleFunc("DELETE /v1/guilds/{id}/members/{player_id}", a.handleGuildMemberDelete)
	mux.HandleFunc("GET /v1/players/{id}/guild", a.handlePlayerGuild)
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
		"intent_process_us":         m.IntentProcess,
		"active_matches":            m.ActiveMatches,
		"telemetry_events_enqueued": m.TelemetryEventsEnqueued,
		"telemetry_events_written":  m.TelemetryEventsWritten,
		"telemetry_events_dropped":  m.TelemetryEventsDropped,
		"telemetry_export_errors":   m.TelemetryExportErrors,

		// Intent word-length histograms (M3 batch 42C, measurement only).
		"intent_wordlen": m.IntentWordLen,

		// Behavioral anti-cheat signals (M3 batch 40A, measurement only,
		// docs/M3-ANTI-CHEAT.md).
		"behavior_metronomic_events":       m.BehaviorMetronomicEvents,
		"behavior_rejection_streak_events": m.BehaviorStreakEvents,
		"behavior_word_probe_events":       m.BehaviorWordProbeEvents,
		"behavior_flash_path_events":       m.BehaviorFlashPathEvents,
		"behavior_multi_signal_events":     m.BehaviorMultiSignalEvents,
		"behavior_closed_with_signals":     m.ClosedWithSignals,
		"behavior_closed_flagged_seats":    m.ClosedFlaggedSeats,
		"behavior_closed_family_seats":     m.ClosedFamilySeats,
		"intent_rate_limited_total":        m.IntentRateLimited,
		"behavior_max_rejection_streak":    m.BehaviorMaxRejectionStreak,

		// Per-match max rejection-streak distribution (M3 batch 45A,
		// measurement only): the longitudinal delta alongside
		// behavior_max_rejection_streak.
		"streak_max_per_match": m.StreakMax,

		// M3 batch 46A longitudinal deltas (measurement only): the per-match
		// maximum word-probe episode depth and distinct signal-family count.
		"probe_max_per_match":   m.ProbeMax,
		"families_max_per_match": m.FamiliesMax,
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
	// A single Decode stops after the first JSON value: trailing junk after
	// it ({"language":"en"},"anti_snowball":true}) would otherwise be
	// silently ignored, so a caller could append anything to a valid body
	// and the request would still parse. Require the value to be the whole
	// body.
	if _, err := dec.Token(); err != io.EOF {
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
	// Batch 32B: roster size. Zero means 1v1, so the field is purely additive.
	seats := req.Seats
	if seats == 0 {
		seats = match.MinSeats
	}
	if seats < match.MinSeats || seats > match.MaxSeats {
		httpError(w, http.StatusBadRequest, fmt.Sprintf("seats must be between %d and %d", match.MinSeats, match.MaxSeats))
		return
	}
	if seats > a.maxSeats {
		// Distinguish "this deployment does not offer that" from "that size
		// does not exist": the first is a policy default an operator can
		// change, the second is a property of the game.
		httpError(w, http.StatusBadRequest, fmt.Sprintf(
			"seats above %d are not enabled on this deployment (WORDARENA_MAX_SEATS)", a.maxSeats))
		return
	}
	if req.PlayerIDs != nil && seats != match.MinSeats {
		// Profile binding is positional and 1v1-only in this release; saying
		// so is better than silently ignoring half the request.
		httpError(w, http.StatusBadRequest, "player_ids is only supported for a 1v1 match")
		return
	}
	if req.PlayerIDs != nil {
		if req.PlayerIDs[0] == 0 || req.PlayerIDs[1] == 0 || req.PlayerIDs[0] == req.PlayerIDs[1] {
			httpError(w, http.StatusBadRequest, "player_ids must be two distinct positive ids")
			return
		}
		if !profileIDInRange(req.PlayerIDs[0]) || !profileIDInRange(req.PlayerIDs[1]) {
			httpError(w, http.StatusBadRequest, "player_ids must be within the signed 64-bit range")
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
	botSeats, ok := a.resolveBotSeats(w, req.BotSeats, seats)
	if !ok {
		return
	}
	if req.PvE {
		// A practice match is 1v1 against a server-driven opponent. Saying so
		// explicitly is better than accepting a roster the mode cannot fill.
		if seats != match.MinSeats {
			httpError(w, http.StatusBadRequest, "pve is only available for a 1v1 match")
			return
		}
		if !a.allowBotSeats {
			httpError(w, http.StatusBadRequest, "pve is not enabled on this deployment (WORDARENA_ALLOW_BOT_SEATS)")
			return
		}
		if len(botSeats) == 0 {
			// Seat 0 is the player, seat 1 is the opponent. The player may say
			// otherwise through bot_seats, but the default must not require
			// them to know seat numbering.
			botSeats = []bool{false, true}
		}
	}
	// A difficulty names the practice opponent; without pve there is no
	// opponent to name it for, so it is a client error, not an ignored field.
	difficulty := pve.DifficultyEasy
	if req.PvEDifficulty != "" {
		if !req.PvE {
			httpError(w, http.StatusBadRequest, "pve_difficulty requires pve")
			return
		}
		d := pve.Difficulty(req.PvEDifficulty)
		if !d.Valid() {
			httpError(w, http.StatusBadRequest, "pve_difficulty must be easy, normal or hard")
			return
		}
		difficulty = d
	}
	var pids []uint64
	if req.PlayerIDs != nil {
		pids = []uint64{req.PlayerIDs[0], req.PlayerIDs[1]}
	}
	info, err := a.createRoomNBots(lang, req.Seed, pids, botSeats, seats, req.SuddenDeath, req.AntiSnowball)
	if err == nil && req.PvE {
		// The opponent is attached after provisioning so a failure to load the
		// dictionary is reported to the caller instead of silently producing a
		// match where seat 1 never moves.
		if err = a.startPvE(info, lang, difficulty); err != nil {
			a.discardRoom(info.ID)
			httpError(w, http.StatusInternalServerError, "practice opponent unavailable")
			log.Printf("pve opponent for match %d: %v", info.ID, err)
			return
		}
	}
	if err != nil {
		if errors.Is(err, errRoomCapacity) {
			httpError(w, http.StatusTooManyRequests, "too many active matches")
			return
		}
		// createRoomN rejects out-of-range seats before allocating; that is a
		// client error and must not be reported as a server failure.
		if strings.Contains(err.Error(), "out of range") {
			httpError(w, http.StatusBadRequest, err.Error())
			return
		}
		log.Printf("create match failed: %v", err)
		httpError(w, http.StatusInternalServerError, "match creation failed")
		return
	}
	writeJSON(w, http.StatusCreated, createMatchResponse{
		MatchID: info.ID, Seed: info.Seed, Language: lang, SuddenDeath: req.SuddenDeath,
		AntiSnowball: req.AntiSnowball,
		Tokens:       info.Tokens, UserIDs: info.UserIDs, Seats: len(info.Tokens),
		MatchCode: info.Access.Code, ReadCapability: info.Access.ReadCap,
	})
}

// createRoom provisions a new live room and returns its public join info.
// It is shared by the direct-create endpoint and the matchmaker. playerIDs,
// when non-nil, bind the seats to registered profiles. suddenDeath enables
// the opt-in tiebreak (docs/M1-SUDDEN-DEATH.md).
// Batch 30C: createRoom is the 1v1 spelling of createRoomN, kept so every
// existing caller and test is unchanged. All the logic lives in the
// roster-shaped function below; there is no second provisioning path.
func (a *API) createRoom(lang string, seed *uint64, playerIDs *[2]uint64, suddenDeath bool) (uint64, uint64, [2]string, [2]uint64, matchAccess, error) {
	var pids []uint64
	if playerIDs != nil {
		pids = []uint64{playerIDs[0], playerIDs[1]}
	}
	info, err := a.createRoomN(lang, seed, pids, 2, suddenDeath)
	if err != nil {
		return 0, 0, [2]string{}, [2]uint64{}, matchAccess{}, err
	}
	return info.ID, info.Seed,
		[2]string{info.Tokens[0], info.Tokens[1]},
		[2]uint64{info.UserIDs[0], info.UserIDs[1]},
		info.Access, nil
}

// resolveBotSeats validates a caller's simulated-player declaration and turns
// it into the seat-indexed form the domain wants (M2 batch 32D, Q5).
//
// Three rules, each of which is a way the feature could otherwise be misused:
// the deployment has to offer the capability at all; a declared seat has to
// exist in the roster being created; and duplicate declarations are refused
// rather than silently deduplicated, because a caller that says the same seat
// twice has a bug and should hear about it. It reports the HTTP error itself
// and returns ok=false when the request must not proceed.
func (a *API) resolveBotSeats(w http.ResponseWriter, declared []int, seats int) ([]bool, bool) {
	if len(declared) == 0 {
		return nil, true
	}
	if !a.allowBotSeats {
		httpError(w, http.StatusBadRequest, "declaring bot seats is not enabled on this deployment (WORDARENA_ALLOW_BOT_SEATS)")
		return nil, false
	}
	out := make([]bool, seats)
	seen := make(map[int]bool, len(declared))
	for _, seat := range declared {
		if seat < 0 || seat >= seats {
			httpError(w, http.StatusBadRequest, fmt.Sprintf("bot seat %d is outside the %d-seat roster", seat, seats))
			return nil, false
		}
		if seen[seat] {
			httpError(w, http.StatusBadRequest, fmt.Sprintf("bot seat %d is declared twice", seat))
			return nil, false
		}
		seen[seat] = true
		out[seat] = true
	}
	return out, true
}

// roomInfo is the join info of a freshly provisioned room, for any roster
// size. Tokens and UserIDs are indexed by seat.
type roomInfo struct {
	ID      uint64
	Seed    uint64
	Tokens  []string
	UserIDs []uint64
	Access  matchAccess
	// BotSeats carries the simulated-player declaration that created this
	// room (Q5). It is part of the join info so a caller - the matchmaker, a
	// test, a practice-mode client - can disclose it without going back to
	// the room, and so the queue can refuse to hand a bot seat a human seat's
	// credential expectations.
	BotSeats []int
}

// createRoomN provisions a room with `seats` seats. playerIDs, when
// non-empty, binds seats to registered profiles positionally (a zero entry
// leaves that seat synthetic). seats must be within the simulation's bounds;
// anything else is rejected before allocating.
func (a *API) createRoomN(lang string, seed *uint64, playerIDs []uint64, seats int, suddenDeath bool) (roomInfo, error) {
	// The catch-up rule is not reachable through this wrapper: it is a
	// per-match option carried by the direct-create endpoint, and every
	// existing caller (matchmaker queue, tests) keeps its default-rule room.
	return a.createRoomNBots(lang, seed, playerIDs, nil, seats, suddenDeath, false)
}

// createRoomNBots is the roster-shaped provisioner with an explicit
// simulated-player declaration (M2 batch 32D, Q5). createRoomN is its
// no-bots spelling, so there is still exactly one provisioning path and no
// existing caller changed.
func (a *API) createRoomNBots(lang string, seed *uint64, playerIDs []uint64, botSeats []bool, seats int, suddenDeath bool, antiSnowball bool) (roomInfo, error) {
	if len(botSeats) > seats {
		return roomInfo{}, fmt.Errorf("bot_seats has %d entries for %d seats", len(botSeats), seats)
	}
	if seats < match.MinSeats || seats > match.MaxSeats {
		return roomInfo{}, fmt.Errorf("seats %d out of range [%d,%d]", seats, match.MinSeats, match.MaxSeats)
	}
	if len(playerIDs) > seats {
		return roomInfo{}, fmt.Errorf("player_ids has %d entries for %d seats", len(playerIDs), seats)
	}
	var s uint64
	if seed != nil {
		s = *seed
	} else {
		var err error
		if s, err = randomSeed(); err != nil {
			return roomInfo{}, err
		}
	}
	tokens := make([]string, seats)
	for i := range tokens {
		tok, err := randomHex(16)
		if err != nil {
			return roomInfo{}, err
		}
		tokens[i] = tok
	}
	// The match code and the read capability are 128 bits of crypto/rand each.
	// They are issued here rather than at result time so a client can be given
	// the handle for a match that has not finished yet, and so the pair exists
	// even for a match that never completes.
	code, err := randomHex(16)
	if err != nil {
		return roomInfo{}, err
	}
	readCap, err := randomHex(16)
	if err != nil {
		return roomInfo{}, err
	}
	access := matchAccess{Code: code, ReadCap: readCap}

	id := a.counter.Add(1)
	a.resultsMu.Lock()
	a.matchCaps[id] = access
	a.resultCodes[code] = id
	a.resultsMu.Unlock()
	// Deterministic synthetic ids. The 1v1 formula (id*2+1, id*2+2) is
	// preserved exactly for two seats so existing fixtures and the live
	// deployment keep the same user ids; a larger roster extends it.
	userIDs := make([]uint64, seats)
	for i := range userIDs {
		userIDs[i] = id*uint64(seats) + uint64(i) + 1
	}
	// Bind seats that supplied a profile; seats without one (zero) keep
	// their synthetic id so user ids stay non-zero on the wire.
	for i := 0; i < len(playerIDs) && i < seats; i++ {
		if playerIDs[i] != 0 {
			userIDs[i] = playerIDs[i]
		}
	}
	room, err := matchroom.New(matchroom.Config{
		MatchID:     id,
		Seed:        s,
		Language:    lang,
		SeatUserIDs: userIDs,
		SeatTokens:  tokens,
		SuddenDeath: suddenDeath,
		TokenTTL:    a.tokenTTL,
		BotSeats:    botSeats,
		// The queue (matchmaker) path provisions with the default rule set:
		// anti-snowball is a per-match option on the direct-create endpoint,
		// not a queue attribute, until a product decision says the option
		// should be user-selectable there.
		AntiSnowball: antiSnowball,
	})
	if err != nil {
		return roomInfo{}, err
	}
	a.mu.Lock()
	if a.maxRooms > 0 && len(a.rooms) >= a.maxRooms {
		a.mu.Unlock()
		return roomInfo{}, errRoomCapacity
	}
	a.rooms[id] = room
	a.mu.Unlock()
	if len(playerIDs) > 0 {
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
		// Disclosing the declaration in telemetry as well as on the wire: an
		// operator should be able to answer "was a bot in this match?" from
		// the event stream without reading the board.
		BotSeats: room.BotSeats(),
	})
	go a.runRoomTicker(id, room)
	return roomInfo{ID: id, Seed: s, Tokens: tokens, UserIDs: userIDs, Access: access, BotSeats: room.BotSeats()}, nil
}

// pveOpponent is a server-driven player attached to one seat of a match. The
// driver is deliberately kept next to the transport rather than inside the
// room: the opponent is a CLIENT of the room (it submits through Room.Submit
// like anyone else), so the simulation keeps one validated door and the
// opponent needs no privileges to play.
type pveOpponent struct {
	seat     match.Seat
	opponent *pve.Opponent
}

// startPvE attaches the practice opponent to every declared bot seat of a
// freshly created match (M2 batch 32E). It returns an error rather than
// creating a silently broken match when the dictionary cannot be loaded.
func (a *API) startPvE(info roomInfo, lang string, difficulty pve.Difficulty) error {
	seat := -1
	for _, s := range info.BotSeats {
		seat = s
		break
	}
	if seat < 0 {
		return fmt.Errorf("pve needs a declared bot seat")
	}
	opponent, err := pve.New(lang, difficulty.Policy())
	if err != nil {
		return err
	}
	if opponent.CandidateCount() == 0 {
		return fmt.Errorf("pve: no %s words of length %d..%d to draw on",
			lang, opponent.Policy().MinLen, opponent.Policy().MaxLen)
	}
	a.pveMu.Lock()
	a.pve[info.ID] = pveOpponent{seat: match.Seat(seat), opponent: opponent}
	a.pveMu.Unlock()
	a.publishTelemetry(telemetryEvent{
		Type: "pve_started", MatchID: info.ID, Language: lang,
		Seat: telemetryInt(seat), BotSeats: info.BotSeats,
		Difficulty: string(difficulty),
	})
	return nil
}

// pveFor returns the opponent attached to a room, if any.
func (a *API) pveFor(id uint64) (pveOpponent, bool) {
	a.pveMu.Lock()
	defer a.pveMu.Unlock()
	o, ok := a.pve[id]
	return o, ok
}

// discardRoom removes a room that failed to finish provisioning.
func (a *API) discardRoom(id uint64) {
	a.mu.Lock()
	delete(a.rooms, id)
	a.mu.Unlock()
	a.pveMu.Lock()
	delete(a.pve, id)
	a.pveMu.Unlock()
}

// tickPvEOpponent gives the practice opponent one chance to act, outside the
// room lock. It is called from the room ticker before the tick advances, so the
// opponent sees the same board a client would and its intent is validated by
// the ordinary submit path - including the ordinary rejection paths. An
// opponent that tries something illegal loses a word, exactly like a player
// would.
func (a *API) tickPvEOpponent(id uint64, room *matchroom.Room) {
	po, ok := a.pveFor(id)
	if !ok {
		return
	}
	cells := po.opponent.Intent(room.Snapshot(), room.Match().Tick(), po.seat)
	if len(cells) == 0 {
		return
	}
	if _, err := room.Submit(po.seat, cells); err != nil {
		// A rejected intent is normal (the board moved on between the decision
		// and the submission); it must never take the room down.
		a.publishTelemetry(telemetryEvent{
			Type: "pve_intent_rejected", MatchID: id, Seat: telemetryInt(int(po.seat)),
		})
	}
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
			// The practice opponent acts through the ordinary submit path
			// (M2 batch 32E); a room without one skips this entirely.
			a.tickPvEOpponent(id, room)
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
				a.clearSeatWindows(id, room.Seats())
				a.pveMu.Lock()
				delete(a.pve, id)
				a.pveMu.Unlock()
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
	snap := protocol.SnapshotToProto(dsnap, room.UserIDs())
	writeJSON(w, http.StatusOK, map[string]any{
		"server_tick":   snap.ServerTick,
		"wave":          snap.CurrentWave,
		"state_version": snap.StateVersion,
		"phase":         dsnap.Phase,
		"sudden_death":  dsnap.SuddenDeath,
		"anti_snowball": dsnap.AntiSnowball,
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
// player_id binds the queue entry to a registered profile queue entry to a registered profile so the eventual
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
		// Bot declares this queue entry as a simulated player (M2 batch 32D,
		// Q5). It is the bot's own, explicit statement about itself; without
		// it the server would have to guess, and a guess is exactly the
		// silent impersonation Q5 rules out. Gated by
		// WORDARENA_ALLOW_BOT_SEATS.
		Bot bool `json:"bot"`
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
	if req.Bot && !a.allowBotSeats {
		httpError(w, http.StatusBadRequest, "declaring a bot queue entry is not enabled on this deployment (WORDARENA_ALLOW_BOT_SEATS)")
		return
	}
	if req.Bot && req.PlayerID != 0 {
		// A bot must not accrue a human's lifetime stats. Refusing the
		// combination is better than accepting it and quietly doing one of the
		// two things the caller asked for.
		httpError(w, http.StatusBadRequest, "a bot queue entry cannot bind a player_id")
		return
	}
	if req.PlayerID != 0 {
		if !profileIDInRange(req.PlayerID) {
			httpError(w, http.StatusBadRequest, "player_id must be within the signed 64-bit range")
			return
		}
		if _, ok, err := a.profiles.Get(req.PlayerID); err != nil {
			httpError(w, http.StatusInternalServerError, "profile store error")
			return
		} else if !ok {
			httpError(w, http.StatusNotFound, "player not found")
			return
		}
	}
	e := a.mm.enqueueBot(lang, req.PlayerID, req.Bot, a.queueRoomFactory())
	writeJSON(w, http.StatusAccepted, e)
}

// queueRoomFactory is the room provisioner the matchmaker injects into every
// pairing attempt. The roster size is len(pids), so the same factory serves
// 1v1 and a short-handed Royale lobby alike.
func (a *API) queueRoomFactory() createRoomFn {
	return func(l string, pids []uint64, bots []bool) (roomInfo, error) {
		return a.createRoomNBots(l, nil, pids, bots, len(pids), false, false)
	}
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
	// Mint the profile's owner token (M2 batch 32G). It is returned exactly
	// once and stored only as a hash: guild mutations authenticate with it, so
	// a response body can never be replayed as another player.
	token, err := randomHex(16)
	if err != nil {
		httpError(w, http.StatusInternalServerError, "token generation failed")
		return
	}
	if err := a.profiles.SetOwnerToken(p.ID, hashOwnerToken(token)); err != nil {
		httpError(w, http.StatusInternalServerError, "profile store error")
		return
	}
	a.publishTelemetry(telemetryEvent{Type: "profile_created", UserID: p.ID, Language: p.Language})
	writeJSON(w, http.StatusCreated, profileCreateResponse{Profile: p, OwnerToken: token})
}

// handlePlayerGet serves one player profile plus lifetime stats.
func (a *API) handlePlayerGet(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r.PathValue("id"))
	if err != nil {
		httpError(w, http.StatusBadRequest, "invalid player id")
		return
	}
	if !profileIDInRange(id) {
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
	botSeats := room.BotSeats()
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
		// Q5: the disclosure is recorded with the authoritative outcome, not
		// only streamed while the match was live. A rating or a reward is
		// computed from this row, possibly after a restart and days later, so
		// a flag that lived only in the room would be worthless exactly when
		// it mattered.
		Bots:           botSeats,
		BotPresent:     len(botSeats) > 0,
		RatingEligible: len(botSeats) == 0,
		recordedAt:     time.Now(),
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
			CatchUpBonus: ev.CatchUpBonus,
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
		BotSeats:     res.Bots,
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
			// Q5: the debug/tooling state view discloses simulated seats too.
			// A disclosure that only one surface carries is a disclosure a
			// client can accidentally bypass.
			"is_bot": p.IsBot,
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
	// Batch 38A: the writer goroutine and every write it performs run on a
	// context the HANDLER also controls. It previously watched r.Context()
	// alone, which is only cancelled when this handler returns - but the
	// handler waits for the writer (<-done below) before it can return, so a
	// client-initiated disconnect deadlocked BOTH goroutines forever (the
	// 37B soak measured exactly +2 leaked goroutines per closed connection).
	wctx, cancelWS := context.WithCancel(ctx)
	defer cancelWS()

	// lastSentVersion is the canonical state version this connection is known
	// to hold, and is the whole of the per-connection delta state (batch
	// 32A). It is written by sendSnapshot below on this goroutine before the
	// writer goroutine starts, and afterwards only by that goroutine, so it
	// needs no lock. It must stay that way: a second caller of sendSnapshot
	// after the goroutine starts would be a data race.
	// useDelta gates whether this connection may be served MatchStateDelta
	// frames (batch 32A) after capability negotiation (M2 batch 33). It starts
	// false: until the client's first message is read the server sends only
	// full snapshots, so a client that negotiates supports_delta=false can
	// never receive a delta it cannot apply. A legacy client (no ClientHello)
	// has it set true on its first intent, preserving the pre-negotiation
	// behaviour exactly.
	useDelta := atomic.Bool{}

	lastSentVersion := 0

	// Immediate canonical snapshot anchors the client (resume semantics:
	// the client reconciles against this snapshot regardless of prior state).
	sendSnapshot := func() {
		userIDs := room.UserIDs()
		snap := protocol.SnapshotToProto(room.Snapshot(), userIDs)
		env := protocol.SnapshotEnvelope(id, snap)
		if err := wsWriteProto(ctx, conn, env); err == nil {
			lastSentVersion = int(snap.StateVersion)
		}
	}
	sendSnapshot()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-wctx.Done():
				return
			case ev := <-sub.Events:
				// Batch 31B: every subscriber receives the same
				// event frame - same seat, same client sequence -
				// so the encoding is identical for all of them and
				// is marshalled once per frame instead of once per
				// connection.
				b, err := ev.Encoded.Bytes(func() ([]byte, error) {
					pb := protocol.WordEventToProto(toMatchEvent(ev), room.UserID(ev.Seat))
					pb.ClientSequence = ev.ClientSeq
					return proto.Marshal(protocol.WordEnvelope(id, pb))
				})
				if err != nil {
					return
				}
				if err := wsWriteBytes(wctx, conn, b); err != nil {
					return
				}
			case sf := <-sub.Snapshots:
				// Batch 32A: a connection that already holds this
				// frame's base is served the delta; one that does not
				// (first frame, dropped frame, reconnect) gets the
				// full snapshot and re-syncs by itself. Either way the
				// payload is rendered and marshalled once per frame for
				// the whole roster, not once per connection.
				b, err := encodeSnapshotFrame(id, room, sf, &lastSentVersion, useDelta.Load())
				if err != nil {
					return
				}
				if err := wsWriteBytes(wctx, conn, b); err != nil {
					return
				}
			}
		}
	}()

	// Reader loop: client intents.
	first := true
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
		// M2 batch 33: the first client message may be a ClientHello that
		// negotiates capabilities (notably whether the client can apply
		// MatchStateDelta frames). A client that does not send one is served
		// exactly as before.
		if first {
			first = false
			if hello := env.GetHello(); hello != nil {
				sd := true
				if hello.Capabilities != nil {
					sd = hello.Capabilities.SupportsDelta
				}
				useDelta.Store(sd)
				helloEnv := &wordarenav1.ServerEnvelope{
					MatchId: id,
					Payload: &wordarenav1.ServerEnvelope_Hello{
						Hello: &wordarenav1.ServerHello{
							ProtocolVersion:    serverProtocolVersion,
							ServerCapabilities: &wordarenav1.ClientCapabilities{SupportsDelta: sd},
							UseDelta:           sd,
						},
					},
				}
				if err := wsWriteProto(ctx, conn, helloEnv); err != nil {
					break
				}
				// A ClientHello carries no competitive intent; wait for the
				// client's next message rather than misreading it as a word.
				continue
			}
			// Legacy first message (an intent or resume): keep the old behaviour
			// - deltas may be served once the connection is in sync.
			useDelta.Store(true)
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
		submitT0 := time.Now()
		frame, err := room.SubmitWithSeq(seat, sw.ClientSequence, ids)
		a.m.intentProcess.record(time.Since(submitT0).Microseconds())
		if err != nil {
			_ = conn.Close(websocket.StatusInternalError, "submit failed")
			break
		}
		// Word-length histogram (M3 batch 42C): record the submitted path
		// length against the authoritative outcome class. Measurement only -
		// nothing here influences the seat, the room or the result. MATCH_NOT_ACTIVE
		// (the designed post-over refusal) is deliberately not recorded.
		a.m.wordLen.record(frame.Result, len(ids))
		a.m.intentsReceived.Add(1)
		if frame.Result == match.ResultAccepted {
			a.m.wordsAccepted.Add(1)
		} else {
			a.m.wordsRejected.Add(1)
		}
		// Behavioral signals (M3 batch 40A): observe the submit outcome and
		// publish any edge-triggered signal. Measurement only - nothing here
		// influences the seat, the room or the result.
		for _, sig := range a.behavior.observe(seatWindowKey{matchID: id, seat: int(seat)}, time.Now(), frame.Result, frame.Word, len(ids)) {
			a.publishTelemetry(telemetryEvent{
				Type:    "behavior_signal",
				Signal:  sig,
				MatchID: id,
				Seat:    telemetryInt(int(seat)),
				UserID:  room.UserID(seat),
			})
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
	// Batch 38A: release the writer BEFORE waiting on it; doing it the other
	// way around was the deadlock (handler waits for done, writer waits for a
	// handler return that never comes).
	cancelWS()
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
	return wsWriteBytes(ctx, conn, b)
}

// wsWriteBytes writes an already-marshalled envelope. The slice may be shared
// between connections (see matchroom.SharedPayload), so it is read-only here.
func wsWriteBytes(ctx context.Context, conn *websocket.Conn, b []byte) error {
	return conn.Write(ctx, websocket.MessageBinary, b)
}

// encodeSnapshotFrame renders one snapshot frame for one connection (batch
// 32A). A delta is used only when the connection is provably in sync - it
// holds exactly the frame's base version - and a full snapshot otherwise, so a
// dropped frame or a late join can never leave a client reconstructing a
// delta onto the wrong state. lastSentVersion is advanced to the version this
// call just handed to the client.
//
// Both encodings are shared boxes owned by the frame, so the roster pays for
// each of them at most once per frame no matter how many connections read
// them.
func encodeSnapshotFrame(id uint64, room *matchroom.Room, sf matchroom.SnapshotFrame, lastSentVersion *int, useDelta bool) ([]byte, error) {
	if useDelta && sf.Base != nil && sf.EncodedDelta != nil && *lastSentVersion == sf.Base.StateVersion {
		b, err := sf.EncodedDelta.Bytes(func() ([]byte, error) {
			d := protocol.DeltaToProto(*sf.Base, sf.Snapshot, room.UserIDs())
			return proto.Marshal(protocol.DeltaEnvelope(id, d))
		})
		if err != nil {
			return nil, err
		}
		*lastSentVersion = sf.Snapshot.StateVersion
		return b, nil
	}
	b, err := sf.Encoded.Bytes(func() ([]byte, error) {
		pb := protocol.SnapshotToProto(sf.Snapshot, room.UserIDs())
		return proto.Marshal(protocol.SnapshotEnvelope(id, pb))
	})
	if err != nil {
		return nil, err
	}
	*lastSentVersion = sf.Snapshot.StateVersion
	return b, nil
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
		d := uint64(c - '0')
		// Without this guard a long digit string wrapped silently
		// (id*10 overflowed), so a nonsense id could alias onto a real one.
		if id > (math.MaxUint64-d)/10 {
			return 0, fmt.Errorf("id out of range")
		}
		id = id*10 + d
	}
	return id, nil
}

// profileIDInRange reports whether a client-supplied profile id can exist at
// all. players.id is BIGSERIAL, i.e. a signed 64-bit identity, while the wire
// type is uint64: an id with the top bit set is not a valid profile reference
// and must be refused at the boundary. Letting it reach the store means the
// Postgres backend fails inside the pgx encoder and the caller is told
// "profile store error" (500) for a malformed request. It is the same
// uint64-vs-BIGINT mismatch that silently dropped durable match results before
// migration 002.
func profileIDInRange(id uint64) bool { return id <= math.MaxInt64 }

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func httpError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
