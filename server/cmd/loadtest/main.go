// Command loadtest is the M2 infrastructure load harness (docs/M2-LOAD-TESTING.md).
//
// It exists to answer a question no unit test can: how much of the real
// transport one instance holds before something breaks, and WHAT breaks first.
// It is deliberately a client of the public surface - HTTP create plus the
// WebSocket protocol - because a load test that reaches inside the server
// measures the harness, not the deployment.
//
// What it measures, per run:
//
//	matches created / second and create latency percentiles;
//	intent -> WordValidatedEvent round-trip latency (p50/p95/p99/max);
//	frames and BYTES per second per client, split by frame kind, which is the
//	number the 32A batch bounded at 0.6-0.9 KB/s per client and ~50 KB/s for a
//	full lobby;
//	server CPU and RSS sampled from /proc (optional, via -server-pid-file);
//	errors by class, and whether the server still answers /healthz and
//	/metrics afterwards, with the room count it reports.
//
// SAFETY: run this against an ISOLATED instance. It creates rooms (which keep
// ticking to the end of the match even if every client disconnects) and it
// submits real intents. The live systemd service must never be a target.
//
// Usage:
//
//	go run ./cmd/loadtest -url http://127.0.0.1:18080 -matches 20 -duration 60s
//	go run ./cmd/loadtest -matches 3 -seats 60 -duration 30s -json /tmp/lobby.json
//	go run ./cmd/loadtest -matches 8 -seats 2 -duration 45s -server-pid-file /tmp/game.pid
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	wordarenav1 "github.com/JoTalbot/words/server/gen/wordarena/net/v1"
	"github.com/JoTalbot/words/server/internal/dictionary"
	"github.com/JoTalbot/words/server/internal/match"
	"github.com/coder/websocket"
	"google.golang.org/protobuf/proto"
)

type frameKind string

const (
	frameSnapshot  frameKind = "snapshot"
	frameDelta     frameKind = "delta"
	frameWordEvent frameKind = "word_event"
)

// clientStats is one seat's view of the run. Counters are atomics because the
// reader goroutine and the report both touch them.
type clientStats struct {
	frames atomic.Int64
	bytes  atomic.Int64

	mu       sync.Mutex
	byKind   map[frameKind]int64
	byKindB  map[frameKind]int64
	rejected int64
	accepted int64
	words    int64
}

func newClientStats() *clientStats {
	return &clientStats{byKind: map[frameKind]int64{}, byKindB: map[frameKind]int64{}}
}

func (s *clientStats) add(k frameKind, n int64) {
	s.frames.Add(1)
	s.bytes.Add(n)
	s.mu.Lock()
	s.byKind[k]++
	s.byKindB[k] += n
	s.mu.Unlock()
}

// report is the machine-readable outcome of a run.
type report struct {
	Started   time.Time `json:"started"`
	DurationS float64   `json:"duration_s"`
	URL       string    `json:"url"`
	Matches   int       `json:"matches"`
	Seats     int       `json:"seats"`
	Clients   int       `json:"clients"`

	Created         int     `json:"created"`
	CreateP50Ms     float64 `json:"create_p50_ms"`
	CreateP95Ms     float64 `json:"create_p95_ms"`
	CreateMaxMs     float64 `json:"create_max_ms"`
	CreateErr       int     `json:"create_errors"`
	CreateThrottled int     `json:"create_throttled"`
	DialErr         int     `json:"dial_errors"`
	IntentsSent     int     `json:"intents_sent"`
	IntentsAcked    int     `json:"intents_acked"`
	IntentsAccepted int     `json:"intents_accepted"`
	IntentsRejected int     `json:"intents_rejected"`
	SkippedNoWord   int     `json:"skipped_no_word"`
	IntentsPerSec   float64 `json:"intents_per_sec"`
	AckedPerSec     float64 `json:"acked_per_sec"`

	LatencyP50Ms float64 `json:"latency_p50_ms"`
	LatencyP95Ms float64 `json:"latency_p95_ms"`
	LatencyP99Ms float64 `json:"latency_p99_ms"`
	LatencyMaxMs float64 `json:"latency_max_ms"`

	Frames         int64            `json:"frames"`
	Bytes          int64            `json:"bytes"`
	BytesPerSec    float64          `json:"bytes_per_sec_total"`
	BytesPerClient float64          `json:"bytes_per_sec_per_client"`
	ResultsByName  map[string]int   `json:"results_by_name"`
	FramesByKind   map[string]int64 `json:"frames_by_kind"`
	BytesByKind    map[string]int64 `json:"bytes_by_kind"`

	ReadErrors  int `json:"read_errors"`
	WriteErrors int `json:"write_errors"`

	ServerCPUSeconds float64 `json:"server_cpu_seconds,omitempty"`
	ServerCPUPercent float64 `json:"server_cpu_percent,omitempty"`
	ServerRSSStartMB float64 `json:"server_rss_start_mb,omitempty"`
	ServerRSSEndMB   float64 `json:"server_rss_end_mb,omitempty"`

	ActiveMatchesAfter int     `json:"active_matches_after"`
	ActiveMatchesEnd   int     `json:"active_matches_end"`
	DrainSeconds       float64 `json:"drain_seconds,omitempty"`
	HealthzAfter       string  `json:"healthz_after"`
	MetricsAfter       string  `json:"metrics_after"`

	Notes []string `json:"notes,omitempty"`
}

type matchSeat struct {
	userID  uint64
	token   string
	readyCh chan struct{}
}

type gameMatch struct {
	id     uint64
	seats  []matchSeat
	closed atomic.Int32
}

// harness holds everything shared by the goroutines of one run.
type harness struct {
	url        string
	httpClient *http.Client
	intentGap  time.Duration
	stopCh     chan struct{}

	// submitNoise allows the fallback that submits a non-word when the board
	// offers nothing spellable. Off by default: a load run should measure the
	// work a real match does, and the rejection path is cheaper for the server.
	submitNoise bool
	// words is the dictionary the harness spells from (set by loadWords).
	words []string
	// results is the per-outcome histogram of the intents the harness sent.
	// A load run that mostly exercises the rejection path measures the wrong
	// thing, so the breakdown is part of the report rather than a log detail.
	resultMu sync.Mutex
	results  map[wordarenav1.WordResult]int

	dialErr   atomic.Int64
	readErr   atomic.Int64
	writeErr  atomic.Int64
	sent      atomic.Int64
	accepted  atomic.Int64
	rejected  atomic.Int64
	skipped   atomic.Int64
	throttled atomic.Int64

	createLat []float64
	latMu     sync.Mutex
	intentLat []float64
}

// loadWords loads the dictionary once so intents are real words rather than
// guesses. A harness that mostly submits nonsense would measure the rejection
// path and nothing else: the work a real match does (claim, score, event
// broadcast, state version) would never happen.
func (h *harness) loadWords(lang string, minLen, maxLen int) error {
	snap, err := dictionary.LoadSnapshot(dictionary.Language(lang))
	if err != nil {
		return err
	}
	for _, w := range snap.Words() {
		if n := len([]rune(w)); n >= minLen && n <= maxLen {
			h.words = append(h.words, w)
		}
	}
	// Shortest first, then lexicographic. On a 12-cell 1v1 board a client that
	// always tries the longest word it can spell exhausts the board after two
	// or three claims and then submits noise; preferring short words keeps the
	// load realistic for the whole run and makes two runs comparable.
	sort.Slice(h.words, func(i, j int) bool {
		li, lj := len([]rune(h.words[i])), len([]rune(h.words[j]))
		if li != lj {
			return li < lj
		}
		return h.words[i] < h.words[j]
	})
	if len(h.words) == 0 {
		return fmt.Errorf("dictionary %s has no words of length %d..%d", lang, minLen, maxLen)
	}
	return nil
}

func (h *harness) recordCreate(d time.Duration) {
	h.latMu.Lock()
	h.createLat = append(h.createLat, float64(d.Microseconds())/1000)
	h.latMu.Unlock()
}

func (h *harness) recordIntent(d time.Duration) {
	h.latMu.Lock()
	h.intentLat = append(h.intentLat, float64(d.Microseconds())/1000)
	h.latMu.Unlock()
}

// createMatch provisions one room through the real HTTP surface.
//
// A 429 is not an error in this harness: the server rate-limits state-changing
// calls per caller on purpose (WORDARENA_MUTATIONS_PER_MIN, default 120), and a
// load test that bursts creates will meet that limit before it meets any other.
// The harness honours Retry-After and counts the throttle, because which ceiling
// you hit first is one of the things the run is supposed to find out.
func (h *harness) createMatch(seats int, lang string) (*gameMatch, error) {
	if seats < 2 {
		return nil, fmt.Errorf("seats must be at least 2")
	}
	body := fmt.Sprintf(`{"language":%q`, lang)
	if seats > 2 {
		body += fmt.Sprintf(`,"seats":%d`, seats)
	}
	body += "}"

	const maxAttempts = 12
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		start := time.Now()
		resp, err := h.httpClient.Post(h.url+"/v1/matches", "application/json", strings.NewReader(body))
		if err != nil {
			lastErr = err
			time.Sleep(200 * time.Millisecond)
			continue
		}
		if resp.StatusCode == http.StatusTooManyRequests {
			wait := 2 * time.Second
			if ra := resp.Header.Get("Retry-After"); ra != "" {
				if secs, err := strconv.Atoi(ra); err == nil && secs > 0 {
					wait = time.Duration(secs) * time.Second
				}
			}
			resp.Body.Close()
			h.throttled.Add(1)
			time.Sleep(wait)
			continue
		}
		if resp.StatusCode != http.StatusCreated {
			msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			lastErr = fmt.Errorf("create: status %d body %s", resp.StatusCode, strings.TrimSpace(string(msg)))
			break
		}
		var out struct {
			MatchID uint64   `json:"match_id"`
			Tokens  []string `json:"tokens"`
			UserIDs []uint64 `json:"user_ids"`
		}
		err = json.NewDecoder(resp.Body).Decode(&out)
		resp.Body.Close()
		if err != nil {
			lastErr = err
			break
		}
		h.recordCreate(time.Since(start))
		if len(out.Tokens) != seats || len(out.UserIDs) != seats {
			return nil, fmt.Errorf("create: %d seats returned, want %d", len(out.Tokens), seats)
		}
		m := &gameMatch{id: out.MatchID}
		for i := 0; i < seats; i++ {
			m.seats = append(m.seats, matchSeat{userID: out.UserIDs[i], token: out.Tokens[i], readyCh: make(chan struct{})})
		}
		return m, nil
	}
	if lastErr == nil {
		lastErr = errors.New("create: still throttled after retries")
	}
	return nil, lastErr
}

// playSeat is one client: it holds the newest snapshot it has, submits an
// intent whenever its own timer fires, and measures the round trip. It never
// assumes anything about the board beyond what it has received, so an intent
// that the server rejects is a legitimate outcome and is counted, not an error.
func (h *harness) playSeat(ctx context.Context, m *gameMatch, seat int, stats *clientStats) {
	wsURL := "ws" + strings.TrimPrefix(h.url, "http") +
		fmt.Sprintf("/v1/match/ws?match_id=%d&token=%s", m.id, m.seats[seat].token)
	dialCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(dialCtx, wsURL, nil)
	if err != nil {
		h.dialErr.Add(1)
		return
	}
	defer conn.Close(websocket.StatusNormalClosure, "loadtest done")
	conn.SetReadLimit(1 << 20)

	// The reader owns the socket; the writer only ever writes. Sends are
	// sequenced so an ack can be matched to its intent without a shared map.
	var seq atomic.Uint32
	type waiting struct {
		seq  uint32
		sent time.Time
	}
	acks := make(chan waiting, 64)

	stateMu := sync.Mutex{}
	var latest *wordarenav1.MatchStateSnapshot
	var pending []waiting

	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		for {
			_, data, err := conn.Read(ctx)
			if err != nil {
				return
			}
			var env wordarenav1.ServerEnvelope
			if err := proto.Unmarshal(data, &env); err != nil {
				h.readErr.Add(1)
				return
			}
			n := int64(len(data))
			switch {
			case env.GetSnapshot() != nil:
				snap := env.GetSnapshot()
				stats.add(frameSnapshot, n)
				stateMu.Lock()
				latest = snap
				stateMu.Unlock()
			case env.GetSnapshotDelta() != nil:
				stats.add(frameDelta, n)
			case env.GetWordEvent() != nil:
				stats.add(frameWordEvent, n)
				ev := env.GetWordEvent()
				if ev.GetUserId() == m.seats[seat].userID {
					if ev.GetResult() == wordarenav1.WordResult_ACCEPTED {
						h.accepted.Add(1)
					} else {
						h.rejected.Add(1)
					}
					h.resultMu.Lock()
					h.results[ev.GetResult()]++
					h.resultMu.Unlock()
					select {
					case acks <- waiting{seq: ev.GetClientSequence()}:
					case <-ctx.Done():
					}
				}
			}
		}
	}()

	// Submit intents on the caller's cadence: this is the load, and its shape
	// (a real word over currently free cells) is what makes the server do real
	// work rather than reject cheap nonsense.
	ticker := time.NewTicker(h.intentGap)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-h.stopCh:
			return
		case <-readDone:
			return
		case w := <-acks:
			stateMu.Lock()
			kept := make([]waiting, 0, len(pending))
			var matched waiting
			found := false
			for _, p := range pending {
				if p.seq == w.seq && !found {
					matched, found = p, true
					continue
				}
				kept = append(kept, p)
			}
			pending = kept
			stateMu.Unlock()
			if found {
				h.recordIntent(time.Since(matched.sent))
			}
		case <-ticker.C:
			stateMu.Lock()
			snap := latest
			stateMu.Unlock()
			if snap == nil || snap.GetOver() {
				continue
			}
			path, _ := h.pickIntent(snap)
			if len(path) == 0 {
				// Nothing spellable on the free cells right now. A real player
				// waits for the board to change; submitting noise here would
				// inflate the intent count while measuring only the cheap
				// rejection path.
				h.skipped.Add(1)
				if !h.submitNoise {
					continue
				}
				path = h.noisePath(snap)
				if len(path) == 0 {
					continue
				}
			}
			s := seq.Add(1)
			env := &wordarenav1.ClientEnvelope{
				MatchId: m.id,
				Payload: &wordarenav1.ClientEnvelope_SubmitWord{
					SubmitWord: &wordarenav1.SubmitWordIntent{
						MatchId:        m.id,
						ClientSequence: s,
						LetterIndices:  path,
					},
				},
			}
			data, err := proto.Marshal(env)
			if err != nil {
				continue
			}
			writeCtx, cancelWrite := context.WithTimeout(ctx, 10*time.Second)
			err = conn.Write(writeCtx, websocket.MessageBinary, data)
			cancelWrite()
			if err != nil {
				h.writeErr.Add(1)
				return
			}
			h.sent.Add(1)
			stateMu.Lock()
			pending = append(pending, waiting{seq: s, sent: time.Now()})
			if len(pending) > 128 {
				pending = pending[len(pending)-128:]
			}
			stateMu.Unlock()
		}
	}
}

// pickIntent picks a word over free cells from the snapshot the client holds.
// It mirrors what an honest client would do: only free, unlocked cells, and a
// path of distinct cells. The board is small (12-60 cells), so a direct scan is
// both fast enough and obviously correct.
func (h *harness) pickIntent(snap *wordarenav1.MatchStateSnapshot) ([]uint32, string) {
	type letterCell struct {
		id     uint32
		letter byte
	}
	var free []letterCell
	for _, c := range snap.GetCells() {
		if c.GetIsLocked() || c.GetOwnerUserId() != 0 || c.GetLetter() == "" {
			continue
		}
		free = append(free, letterCell{id: c.GetCellId(), letter: c.GetLetter()[0]})
	}
	if len(free) < 3 {
		return nil, ""
	}

	// Spell the first dictionary word the board allows, over distinct free
	// cells. Deterministic per board: the same snapshot always yields the same
	// intent, so two runs of the harness submit the same shape of work.
	for _, w := range h.words {
		used := map[uint32]bool{}
		path := make([]uint32, 0, len(w))
		ok := true
		for i := 0; i < len(w); i++ {
			found := false
			for _, fc := range free {
				if used[fc.id] || fc.letter != w[i] {
					continue
				}
				used[fc.id] = true
				path = append(path, fc.id)
				found = true
				break
			}
			if !found {
				ok = false
				break
			}
		}
		if ok {
			return path, w
		}
	}
	return nil, ""
}

// noisePath is the opt-in fallback: the first three free cells, whatever they
// spell. Only used with -submit-invalid.
func (h *harness) noisePath(snap *wordarenav1.MatchStateSnapshot) []uint32 {
	path := make([]uint32, 0, 3)
	for _, c := range snap.GetCells() {
		if c.GetIsLocked() || c.GetOwnerUserId() != 0 || c.GetLetter() == "" {
			continue
		}
		path = append(path, c.GetCellId())
		if len(path) == 3 {
			break
		}
	}
	if len(path) < 3 {
		return nil
	}
	return path
}

func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(math.Ceil(p/100*float64(len(sorted)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

// procSample reads CPU seconds and RSS from /proc for the server process.
func procSample(pid int) (cpuSeconds float64, rssMB float64, err error) {
	stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, 0, err
	}
	fields := strings.Fields(string(stat))
	if len(fields) < 15 {
		return 0, 0, errors.New("short /proc stat")
	}
	utime, _ := strconv.ParseFloat(fields[13], 64)
	stime, _ := strconv.ParseFloat(fields[14], 64)
	cpuSeconds = (utime + stime) / 100.0 // USER_HZ is 100 on Linux

	status, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return cpuSeconds, 0, err
	}
	for _, line := range strings.Split(string(status), "\n") {
		if strings.HasPrefix(line, "VmRSS:") {
			f := strings.Fields(line)
			if len(f) >= 2 {
				kb, _ := strconv.ParseFloat(f[1], 64)
				rssMB = kb / 1024
			}
			break
		}
	}
	return cpuSeconds, rssMB, nil
}

func serverSnapshot(url string) (healthz, metrics string, active int) {
	cl := &http.Client{Timeout: 5 * time.Second}
	if resp, err := cl.Get(url + "/healthz"); err == nil {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		healthz = strings.TrimSpace(string(msg))
	} else {
		healthz = "error: " + err.Error()
	}
	if resp, err := cl.Get(url + "/metrics"); err == nil {
		var out struct {
			ActiveMatches int `json:"active_matches"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&out)
		resp.Body.Close()
		active = out.ActiveMatches
		metrics = "ok"
	} else {
		metrics = "error: " + err.Error()
	}
	return
}

// config is one load run, separated from flag parsing so the same code can be
// driven by a test (see loadtest_test.go) instead of only by a human.
type config struct {
	URL           string
	Matches       int
	Seats         int
	Duration      time.Duration
	IntentGap     time.Duration
	Language      string
	JSONOut       string
	PIDFile       string
	Quiet         bool
	WaitDrain     time.Duration
	SubmitInvalid bool
	SkipClients   bool
	CreateRate    float64
	Out           io.Writer
}

func main() {
	url := flag.String("url", "http://127.0.0.1:18080", "target base URL (an ISOLATED instance, never the live service)")
	matches := flag.Int("matches", 4, "concurrent matches to create and hold")
	seats := flag.Int("seats", 2, "seats per match (2..60)")
	duration := flag.Duration("duration", 30*time.Second, "how long to hold and drive the load")
	intentGap := flag.Duration("intent-gap", 2*time.Second, "per-client interval between intents")
	lang := flag.String("language", "en", "match language")
	jsonOut := flag.String("json", "", "write the machine-readable report to this path")
	drainWait := flag.Duration("wait-drain", 0, "after closing clients, wait up to this long for active_matches to reach 0")
	submitInvalid := flag.Bool("submit-invalid", false, "when the board cannot spell any dictionary word, submit the first free cells anyway (exercises the rejection path; off by default so the run measures real work)")
	createRate := flag.Float64("create-rate", 0, "maximum match creations per second (0 = as fast as the server allows); use a paced value to stay under a deployment's own mutation limit instead of waiting out its Retry-After")
	skipClients := flag.Bool("skip-clients", false, "provision the matches and hold them WITHOUT connecting clients: measures what an abandoned room costs the server, which is the dominant cost when players leave")
	pidFile := flag.String("server-pid-file", "", "file containing the target server's pid, for CPU/RSS sampling")
	quiet := flag.Bool("quiet", false, "only print the final summary")
	flag.Parse()

	cfg := config{
		URL: *url, Matches: *matches, Seats: *seats, Duration: *duration,
		IntentGap: *intentGap, Language: *lang, JSONOut: *jsonOut, PIDFile: *pidFile,
		Quiet: *quiet, WaitDrain: *drainWait, SubmitInvalid: *submitInvalid,
		SkipClients: *skipClients, CreateRate: *createRate, Out: os.Stdout,
	}
	if _, err := run(cfg); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// run performs one load run and returns its report.
func run(cfg config) (*report, error) {
	if cfg.Seats < 2 || cfg.Seats > match.MaxSeats {
		return nil, fmt.Errorf("seats must be between 2 and %d", match.MaxSeats)
	}
	if cfg.Matches < 1 {
		return nil, errors.New("matches must be at least 1")
	}
	out := cfg.Out
	if out == nil {
		out = io.Discard
	}

	rep := &report{
		Started: time.Now(), URL: cfg.URL, Matches: cfg.Matches, Seats: cfg.Seats,
		Clients: cfg.Matches * cfg.Seats,
	}
	if cfg.SkipClients {
		rep.Clients = 0
	}
	h := &harness{
		url: cfg.URL, httpClient: &http.Client{Timeout: 20 * time.Second},
		intentGap: cfg.IntentGap, stopCh: make(chan struct{}),
		results:     map[wordarenav1.WordResult]int{},
		submitNoise: cfg.SubmitInvalid,
	}
	if !cfg.SkipClients {
		if err := h.loadWords(cfg.Language, 3, 5); err != nil {
			return nil, fmt.Errorf("load dictionary: %w", err)
		}
	}

	var pid int
	if cfg.PIDFile != "" {
		if b, err := os.ReadFile(cfg.PIDFile); err == nil {
			pid, _ = strconv.Atoi(strings.TrimSpace(string(b)))
		}
	}
	var cpuStart float64
	if pid > 0 {
		if c, m, err := procSample(pid); err == nil {
			cpuStart = c
			rep.ServerRSSStartMB = round1(m)
		}
	}

	if !cfg.Quiet {
		fmt.Fprintf(out, "load: %d matches x %d seats = %d clients, %s, intent every %s\n",
			cfg.Matches, cfg.Seats, rep.Clients, cfg.Duration, cfg.IntentGap)
	}

	// --- provisioning phase, timed on its own ---
	rooms := make([]*gameMatch, 0, cfg.Matches)
	var nextCreate time.Time
	for i := 0; i < cfg.Matches; i++ {
		if cfg.CreateRate > 0 {
			if wait := time.Until(nextCreate); wait > 0 {
				time.Sleep(wait)
			}
			nextCreate = time.Now().Add(time.Duration(float64(time.Second) / cfg.CreateRate))
		}
		m, err := h.createMatch(cfg.Seats, cfg.Language)
		if err != nil {
			rep.CreateErr++
			rep.Notes = append(rep.Notes, fmt.Sprintf("create %d failed: %v", i, err))
			continue
		}
		rooms = append(rooms, m)
	}
	rep.Created = len(rooms)
	rep.CreateThrottled = int(h.throttled.Load())
	if rep.Created == 0 {
		return rep, errors.New("no matches could be created; nothing to load")
	}
	{
		sort.Float64s(h.createLat)
		rep.CreateP50Ms = round3(percentile(h.createLat, 50))
		rep.CreateP95Ms = round3(percentile(h.createLat, 95))
		if len(h.createLat) > 0 {
			rep.CreateMaxMs = round3(h.createLat[len(h.createLat)-1])
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var wg sync.WaitGroup
	seatStats := make([]*clientStats, len(rooms)*cfg.Seats)
	if !cfg.SkipClients {
		for i, m := range rooms {
			for s := 0; s < cfg.Seats; s++ {
				st := newClientStats()
				seatStats[i*cfg.Seats+s] = st
				wg.Add(1)
				go func(m *gameMatch, s int, st *clientStats) {
					defer wg.Done()
					h.playSeat(ctx, m, s, st)
				}(m, s, st)
			}
		}
	}

	// --- hold the load ---
	deadline := time.After(cfg.Duration)
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	startHold := time.Now()
loop:
	for {
		select {
		case <-deadline:
			break loop
		case <-tick.C:
			if !cfg.Quiet {
				fmt.Fprintf(out, "  t=%4.0fs intents=%d acked=%d bytes=%.1f MB\n", time.Since(startHold).Seconds(),
					h.sent.Load(), h.accepted.Load(), float64(totalBytes(seatStats))/1e6)
			}
		}
	}
	rep.DurationS = round1(time.Since(startHold).Seconds())

	// --- drain: stop driving, keep sockets open briefly so in-flight acks land ---
	time.Sleep(2 * time.Second)
	close(h.stopCh)
	cancel()
	wg.Wait()

	// --- aggregate ---
	for _, st := range seatStats {
		if st == nil {
			continue
		}
		rep.Frames += st.frames.Load()
		rep.Bytes += st.bytes.Load()
		st.mu.Lock()
		for k, v := range st.byKind {
			if rep.FramesByKind == nil {
				rep.FramesByKind = map[string]int64{}
			}
			rep.FramesByKind[string(k)] += v
		}
		for k, v := range st.byKindB {
			if rep.BytesByKind == nil {
				rep.BytesByKind = map[string]int64{}
			}
			rep.BytesByKind[string(k)] += v
		}
		st.mu.Unlock()
	}
	h.latMu.Lock()
	sort.Float64s(h.intentLat)
	rep.IntentsAcked = len(h.intentLat)
	rep.LatencyP50Ms = round3(percentile(h.intentLat, 50))
	rep.LatencyP95Ms = round3(percentile(h.intentLat, 95))
	rep.LatencyP99Ms = round3(percentile(h.intentLat, 99))
	if len(h.intentLat) > 0 {
		rep.LatencyMaxMs = round3(h.intentLat[len(h.intentLat)-1])
	}
	h.latMu.Unlock()

	h.resultMu.Lock()
	rep.ResultsByName = map[string]int{}
	for k, v := range h.results {
		rep.ResultsByName[k.String()] = v
	}
	h.resultMu.Unlock()
	if cfg.SkipClients {
		rep.Notes = append(rep.Notes, "clients were skipped: this run measures the cost of holding rooms (the ticker), which is what an abandoned match costs the box")
	}
	rep.IntentsSent = int(h.sent.Load())
	rep.SkippedNoWord = int(h.skipped.Load())
	rep.IntentsAccepted = int(h.accepted.Load())
	rep.IntentsRejected = int(h.rejected.Load())
	rep.DialErr = int(h.dialErr.Load())
	rep.CreateThrottled = int(h.throttled.Load())
	rep.ReadErrors = int(h.readErr.Load())
	rep.WriteErrors = int(h.writeErr.Load())

	if rep.DurationS > 0 {
		rep.IntentsPerSec = round1(float64(rep.IntentsSent) / rep.DurationS)
		rep.AckedPerSec = round1(float64(rep.IntentsAcked) / rep.DurationS)
		rep.BytesPerSec = round1(float64(rep.Bytes) / rep.DurationS)
		if rep.Clients > 0 {
			rep.BytesPerClient = round1(float64(rep.Bytes) / rep.DurationS / float64(rep.Clients))
		}
	}

	if pid > 0 {
		if c, m, err := procSample(pid); err == nil {
			rep.ServerCPUSeconds = round2(c - cpuStart)
			rep.ServerCPUPercent = round2((c - cpuStart) / rep.DurationS * 100)
			rep.ServerRSSEndMB = round1(m)
		}
	}

	// --- post-conditions: the server must still be healthy and honest ---
	healthz, metrics, active := serverSnapshot(cfg.URL)
	rep.HealthzAfter = healthz
	rep.MetricsAfter = metrics
	rep.ActiveMatchesAfter = active
	rep.ActiveMatchesEnd = active

	// Rooms do not stop when clients leave: a match runs to its end. Measuring
	// the drain makes that visible instead of leaving it to be discovered in
	// production, and it is the difference between "this box holds N matches"
	// and "this box holds N matches plus every abandoned one until it ends".
	if cfg.WaitDrain > 0 {
		drainStart := time.Now()
		for time.Since(drainStart) < cfg.WaitDrain {
			time.Sleep(time.Second)
			_, _, n := serverSnapshot(cfg.URL)
			rep.ActiveMatchesEnd = n
			if n == 0 {
				break
			}
		}
		rep.DrainSeconds = round1(time.Since(drainStart).Seconds())
	}

	printReport(out, rep)
	if cfg.JSONOut != "" {
		b, _ := json.MarshalIndent(rep, "", "  ")
		if err := os.WriteFile(cfg.JSONOut, b, 0o644); err != nil {
			return rep, fmt.Errorf("write %s: %w", cfg.JSONOut, err)
		}
	}
	return rep, nil
}

func totalBytes(stats []*clientStats) int64 {
	var n int64
	for _, s := range stats {
		if s != nil {
			n += s.bytes.Load()
		}
	}
	return n
}

func printReport(out io.Writer, r *report) {
	fmt.Fprintf(out, "\n=== load report ===\n")
	fmt.Fprintf(out, "clients           %d (%d matches x %d seats) for %.1fs\n", r.Clients, r.Created, r.Seats, r.DurationS)
	fmt.Fprintf(out, "create            p50 %.1f ms  p95 %.1f ms  max %.1f ms  errors %d  throttled(429) %d\n",
		r.CreateP50Ms, r.CreateP95Ms, r.CreateMaxMs, r.CreateErr, r.CreateThrottled)
	fmt.Fprintf(out, "dial errors       %d\n", r.DialErr)
	fmt.Fprintf(out, "intents           sent %d (%.1f/s)  acked %d (%.1f/s)  accepted %d  rejected %d  skipped-no-word %d\n",
		r.IntentsSent, r.IntentsPerSec, r.IntentsAcked, r.AckedPerSec, r.IntentsAccepted, r.IntentsRejected, r.SkippedNoWord)
	fmt.Fprintf(out, "intent round trip p50 %.1f ms  p95 %.1f ms  p99 %.1f ms  max %.1f ms\n",
		r.LatencyP50Ms, r.LatencyP95Ms, r.LatencyP99Ms, r.LatencyMaxMs)
	fmt.Fprintf(out, "wire              %d frames  %.2f MB  %.1f bytes/s per client  (%.1f KB/s total)\n",
		r.Frames, float64(r.Bytes)/1e6, r.BytesPerClient, r.BytesPerSec/1024)
	if len(r.FramesByKind) > 0 {
		fmt.Fprintf(out, "frames by kind    ")
		keys := make([]string, 0, len(r.FramesByKind))
		for k := range r.FramesByKind {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(out, "%s=%d ", k, r.FramesByKind[k])
		}
		fmt.Fprintln(out)
	}
	// Bytes per frame: the 32A batch bounded per-client bandwidth, and this is
	// the same number seen from the client side rather than predicted.
	if r.Frames > 0 {
		fmt.Fprintf(out, "avg frame         %.0f bytes\n", float64(r.Bytes)/float64(r.Frames))
	}
	if r.ServerCPUPercent > 0 || r.ServerRSSEndMB > 0 {
		fmt.Fprintf(out, "server            CPU %.1f%% of one core (%.1fs)  RSS %.1f MB -> %.1f MB\n",
			r.ServerCPUPercent, r.ServerCPUSeconds, r.ServerRSSStartMB, r.ServerRSSEndMB)
	}
	fmt.Fprintf(out, "errors            read %d  write %d\n", r.ReadErrors, r.WriteErrors)
	fmt.Fprintf(out, "after the run     active_matches=%d  healthz=%s  metrics=%s\n",
		r.ActiveMatchesAfter, r.HealthzAfter, r.MetricsAfter)
	if r.DrainSeconds > 0 {
		fmt.Fprintf(out, "drain             %.1fs to active_matches=%d after every client left\n",
			r.DrainSeconds, r.ActiveMatchesEnd)
	}
	if r.IntentsSent > 0 {
		fmt.Fprintf(out, "accept rate       %.1f%% of submitted intents were accepted words\n",
			100*float64(r.IntentsAccepted)/float64(r.IntentsSent))
	}
	if len(r.ResultsByName) > 0 {
		keys := make([]string, 0, len(r.ResultsByName))
		for k := range r.ResultsByName {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		fmt.Fprintf(out, "intent outcomes   ")
		for _, k := range keys {
			fmt.Fprintf(out, "%s=%d ", k, r.ResultsByName[k])
		}
		fmt.Fprintln(out)
	}
	for _, n := range r.Notes {
		fmt.Fprintf(out, "note              %s\n", n)
	}
}

func round1(f float64) float64 { return math.Round(f*10) / 10 }
func round2(f float64) float64 { return math.Round(f*100) / 100 }
func round3(f float64) float64 { return math.Round(f*1000) / 1000 }
