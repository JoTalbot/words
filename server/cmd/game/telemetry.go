package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const telemetryEventVersion = 1

// telemetryEvent is the append-only operational event exported by the M1
// telemetry baseline. It deliberately mirrors server-authoritative facts;
// client fields such as client_sequence are correlation aids only.
type telemetryEvent struct {
	Version        int       `json:"version"`
	Time           time.Time `json:"time"`
	Type           string    `json:"type"`
	MatchID        uint64    `json:"match_id,omitempty"`
	Seed           uint64    `json:"seed,omitempty"`
	Language       string    `json:"language,omitempty"`
	SuddenDeath    bool      `json:"sudden_death,omitempty"`
	Seat           *int      `json:"seat,omitempty"`
	UserID         uint64    `json:"user_id,omitempty"`
	ClientSequence *uint32   `json:"client_sequence,omitempty"`
	Word           string    `json:"word,omitempty"`
	Result         string    `json:"result,omitempty"`
	ScoreAdded     *int64    `json:"score_added,omitempty"`
	TotalScore     *int64    `json:"total_score,omitempty"`
	IsSteal        *bool     `json:"is_steal,omitempty"`
	StateVersion   *int      `json:"state_version,omitempty"`
	ServerTick     *int      `json:"server_tick,omitempty"`
	WinnerSeat     *int      `json:"winner_seat,omitempty"`
	IsTie          *bool     `json:"is_tie,omitempty"`
	Score0         *int64    `json:"score0,omitempty"`
	Score1         *int64    `json:"score1,omitempty"`
	// BotSeats discloses the declared simulated-player seats of a match
	// (M2 batch 32D, Q5). An operator should be able to answer "was a bot in
	// this match?" from the event stream instead of reconstructing it.
	BotSeats []int `json:"bot_seats,omitempty"`
	// Difficulty names the practice opponent's preset for pve_started
	// events (M2 batch 35B), so an operator can see what a player practiced
	// against without reconstructing the request.
	Difficulty string `json:"difficulty,omitempty"`
	// GuildID names the guild a guild lifecycle event is about (M2 batch
	// 32G). Guild events reuse UserID for the acting player, so the stream
	// answers "who did what to which guild" without a second subject field.
	GuildID uint64 `json:"guild_id,omitempty"`
	// Signal names the behavioral anti-cheat signal for behavior_signal
	// events (M3 batch 40A). Measurement only - see docs/M3-ANTI-CHEAT.md.
	Signal string `json:"signal,omitempty"`
}

type telemetryStats struct {
	EventsEnqueued uint64
	EventsWritten  uint64
	EventsDropped  uint64
	ExportErrors   uint64
}

type telemetrySink interface {
	Publish(telemetryEvent)
	Stats() telemetryStats
	Close() error
}

type noopTelemetrySink struct{}

func (noopTelemetrySink) Publish(telemetryEvent) {}
func (noopTelemetrySink) Stats() telemetryStats  { return telemetryStats{} }
func (noopTelemetrySink) Close() error           { return nil }

func telemetryInt(v int) *int { return &v }

func telemetryInt64(v int64) *int64 { return &v }

func telemetryUint32(v uint32) *uint32 { return &v }

func telemetryBool(v bool) *bool { return &v }

// jsonlTelemetrySink writes events as newline-delimited JSON. Publish never
// blocks the caller; when the bounded buffer is full, the event is dropped and
// the drop counter makes backpressure visible in /metrics.
type jsonlTelemetrySink struct {
	mu     sync.RWMutex
	closed bool

	ch   chan telemetryEvent
	done chan struct{}

	file *os.File
	w    *bufio.Writer

	enqueued atomic.Uint64
	written  atomic.Uint64
	dropped  atomic.Uint64
	errors   atomic.Uint64
}

func newTelemetrySinkFromEnv() telemetrySink {
	path := os.Getenv("WORDARENA_TELEMETRY_JSONL")
	if path == "" {
		return noopTelemetrySink{}
	}
	buf := envInt("WORDARENA_TELEMETRY_BUFFER", 4096)
	if buf <= 0 {
		buf = 1
	}
	s, err := newJSONLTelemetrySink(path, buf)
	if err != nil {
		// Telemetry must never become a gameplay dependency. A bad export path
		// is visible in logs and the service continues with counters only.
		log.Printf("telemetry export disabled: %v", err)
		return noopTelemetrySink{}
	}
	log.Printf("telemetry export: jsonl path=%s buffer=%d", path, buf)
	return s
}

func newJSONLTelemetrySink(path string, buffer int) (*jsonlTelemetrySink, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("empty telemetry path")
	}
	if buffer <= 0 {
		buffer = 1
	}
	if dir := filepath.Dir(path); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create telemetry dir: %w", err)
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open telemetry jsonl: %w", err)
	}
	s := &jsonlTelemetrySink{
		ch:   make(chan telemetryEvent, buffer),
		done: make(chan struct{}),
		file: f,
		w:    bufio.NewWriter(f),
	}
	go s.run()
	return s, nil
}

func (s *jsonlTelemetrySink) Publish(ev telemetryEvent) {
	if ev.Type == "" {
		return
	}
	if ev.Version == 0 {
		ev.Version = telemetryEventVersion
	}
	if ev.Time.IsZero() {
		ev.Time = time.Now().UTC()
	} else {
		ev.Time = ev.Time.UTC()
	}

	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		s.dropped.Add(1)
		return
	}
	select {
	case s.ch <- ev:
		s.enqueued.Add(1)
	default:
		s.dropped.Add(1)
	}
}

func (s *jsonlTelemetrySink) run() {
	defer close(s.done)
	enc := json.NewEncoder(s.w)
	for ev := range s.ch {
		if err := enc.Encode(ev); err != nil {
			s.errors.Add(1)
			continue
		}
		if err := s.w.Flush(); err != nil {
			s.errors.Add(1)
			continue
		}
		s.written.Add(1)
	}
	if err := s.w.Flush(); err != nil {
		s.errors.Add(1)
	}
}

func (s *jsonlTelemetrySink) Stats() telemetryStats {
	return telemetryStats{
		EventsEnqueued: s.enqueued.Load(),
		EventsWritten:  s.written.Load(),
		EventsDropped:  s.dropped.Load(),
		ExportErrors:   s.errors.Load(),
	}
}

func (s *jsonlTelemetrySink) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	close(s.ch)
	s.mu.Unlock()
	<-s.done
	return s.file.Close()
}

func (a *API) publishTelemetry(ev telemetryEvent) {
	if a.telemetry == nil {
		return
	}
	a.telemetry.Publish(ev)
}

// apiMetricsSnapshot is a stable internal shape shared by JSON and Prometheus
// metric exporters.
type apiMetricsSnapshot struct {
	MatchesCreated          uint64
	MatchesFinished         uint64
	IntentsReceived         uint64
	WordsAccepted           uint64
	WordsRejected           uint64
	IntentProcess           intentHistSnapshot
	IntentWordLen           wordLenSnapshot
	ActiveMatches           int
	TelemetryEventsEnqueued uint64
	TelemetryEventsWritten  uint64
	TelemetryEventsDropped  uint64
	TelemetryExportErrors   uint64

	// Behavioral anti-cheat signals (M3 batches 40A/41A/42A/42B, measurement only).
	BehaviorMetronomicEvents   uint64
	BehaviorStreakEvents       uint64
	BehaviorWordProbeEvents    uint64
	BehaviorFlashPathEvents    uint64
	BehaviorMultiSignalEvents  uint64
	IntentRateLimited          uint64
	BehaviorMaxRejectionStreak int64
	ClosedWithSignals          uint64
	ClosedFlaggedSeats         uint64
	ClosedFamilySeats          uint64
}

func (a *API) metricsSnapshot() apiMetricsSnapshot {
	var ts telemetryStats
	if a.telemetry != nil {
		ts = a.telemetry.Stats()
	}
	return apiMetricsSnapshot{
		MatchesCreated:          a.m.matchesCreated.Load(),
		MatchesFinished:         a.m.matchesFinished.Load(),
		IntentsReceived:         a.m.intentsReceived.Load(),
		WordsAccepted:           a.m.wordsAccepted.Load(),
		WordsRejected:           a.m.wordsRejected.Load(),
		IntentProcess:           a.m.intentProcess.snapshot(),
		IntentWordLen:           a.m.wordLen.snapshot(),
		ActiveMatches:           a.activeRooms(),
		TelemetryEventsEnqueued: ts.EventsEnqueued,
		TelemetryEventsWritten:  ts.EventsWritten,
		TelemetryEventsDropped:  ts.EventsDropped,
		TelemetryExportErrors:   ts.ExportErrors,

		BehaviorMetronomicEvents:   a.behavior.metronomicEvents.Load(),
		BehaviorStreakEvents:       a.behavior.streakEvents.Load(),
		BehaviorWordProbeEvents:    a.behavior.wordProbeEvents.Load(),
		BehaviorFlashPathEvents:    a.behavior.flashPathEvents.Load(),
		BehaviorMultiSignalEvents:  a.behavior.multiSignalEvents.Load(),
		IntentRateLimited:          a.behavior.rateLimited.Load(),
		BehaviorMaxRejectionStreak: a.behavior.maxRejectionStreak.Load(),
		ClosedWithSignals:          a.behavior.closedWithSignals.Load(),
		ClosedFlaggedSeats:         a.behavior.closedFlaggedSeats.Load(),
		ClosedFamilySeats:          a.behavior.closedFamilySeats.Load(),
	}
}

// handlePrometheusMetrics serves a small Prometheus-compatible text export in
// addition to the JSON /metrics endpoint. It avoids an external dependency and
// keeps metric names stable for CI/dev scraping.
func (a *API) handlePrometheusMetrics(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	m := a.metricsSnapshot()
	writeMetric := func(name, help, typ string, value uint64) {
		_, _ = fmt.Fprintf(w, "# HELP %s %s\n", name, help)
		_, _ = fmt.Fprintf(w, "# TYPE %s %s\n", name, typ)
		_, _ = fmt.Fprintf(w, "%s %d\n", name, value)
	}
	writeMetric("wordarena_matches_created_total", "Total matches created by this process.", "counter", m.MatchesCreated)
	writeMetric("wordarena_matches_finished_total", "Total matches finished by this process.", "counter", m.MatchesFinished)
	writeMetric("wordarena_intents_received_total", "Total word intents received by this process.", "counter", m.IntentsReceived)
	writeMetric("wordarena_words_accepted_total", "Total accepted word intents.", "counter", m.WordsAccepted)
	writeMetric("wordarena_words_rejected_total", "Total rejected word intents.", "counter", m.WordsRejected)
	// The intent processing histogram (M2 batch 35C): server-side µs inside
	// the authoritative submit, so an off-box latency run can decompose
	// client-observed round trips into path latency plus server work.
	_, _ = fmt.Fprintf(w, "# HELP wordarena_intent_process_us Server-side processing time of word intents in microseconds.\n")
	_, _ = fmt.Fprintf(w, "# TYPE wordarena_intent_process_us histogram\n")
	for _, line := range a.m.intentProcess.prometheusLines("wordarena_intent_process_us") {
		_, _ = fmt.Fprintln(w, line)
	}
	// The intent word-length histograms (M3 batch 42C): submitted path
	// length (cells) split by the authoritative outcome class. This is the
	// measurement half of the "cell-path geometry vs dictionary structure"
	// signal candidate, and it is a histogram so a future signal threshold
	// can be calibrated from recorded evidence instead of guessed. Each is
	// emitted explicitly so the drift gate can see the names.
	_, _ = fmt.Fprintf(w, "# HELP wordarena_intent_wordlen_accepted_cells Submitted path length in cells for accepted intents.\n")
	_, _ = fmt.Fprintf(w, "# TYPE wordarena_intent_wordlen_accepted_cells histogram\n")
	for _, line := range a.m.wordLen.accepted.prometheusLines("wordarena_intent_wordlen_accepted_cells") {
		_, _ = fmt.Fprintln(w, line)
	}
	_, _ = fmt.Fprintf(w, "# HELP wordarena_intent_wordlen_rejected_not_in_dict_cells Submitted path length in cells for intents rejected as not in dictionary.\n")
	_, _ = fmt.Fprintf(w, "# TYPE wordarena_intent_wordlen_rejected_not_in_dict_cells histogram\n")
	for _, line := range a.m.wordLen.rejectedNotInDict.prometheusLines("wordarena_intent_wordlen_rejected_not_in_dict_cells") {
		_, _ = fmt.Fprintln(w, line)
	}
	_, _ = fmt.Fprintf(w, "# HELP wordarena_intent_wordlen_blocked_by_rule_cells Submitted path length in cells for intents blocked by rule.\n")
	_, _ = fmt.Fprintf(w, "# TYPE wordarena_intent_wordlen_blocked_by_rule_cells histogram\n")
	for _, line := range a.m.wordLen.blockedByRule.prometheusLines("wordarena_intent_wordlen_blocked_by_rule_cells") {
		_, _ = fmt.Fprintln(w, line)
	}
	_, _ = fmt.Fprintf(w, "# HELP wordarena_intent_wordlen_invalid_input_cells Submitted path length in cells for invalid-input intents.\n")
	_, _ = fmt.Fprintf(w, "# TYPE wordarena_intent_wordlen_invalid_input_cells histogram\n")
	for _, line := range a.m.wordLen.invalidInput.prometheusLines("wordarena_intent_wordlen_invalid_input_cells") {
		_, _ = fmt.Fprintln(w, line)
	}
	writeMetric("wordarena_active_matches", "Currently active matches.", "gauge", uint64(m.ActiveMatches))
	writeMetric("wordarena_telemetry_events_enqueued_total", "Telemetry events accepted into the async exporter buffer.", "counter", m.TelemetryEventsEnqueued)
	writeMetric("wordarena_telemetry_events_written_total", "Telemetry events written by the exporter.", "counter", m.TelemetryEventsWritten)
	writeMetric("wordarena_telemetry_events_dropped_total", "Telemetry events dropped due to exporter backpressure or shutdown.", "counter", m.TelemetryEventsDropped)
	writeMetric("wordarena_telemetry_export_errors_total", "Telemetry exporter write or flush errors.", "counter", m.TelemetryExportErrors)
	writeMetric("wordarena_behavior_metronomic_events_total", "Behavioral signal: seats whose submit cadence was machine-regular (measurement only, M3 batch 40A).", "counter", m.BehaviorMetronomicEvents)
	writeMetric("wordarena_behavior_rejection_streak_events_total", "Behavioral signal: streak episodes of consecutive rejected intents crossing the threshold (measurement only).", "counter", m.BehaviorStreakEvents)
	writeMetric("wordarena_behavior_word_probe_events_total", "Behavioral signal: same-word rejection episodes reaching the probe threshold (measurement only).", "counter", m.BehaviorWordProbeEvents)
	writeMetric("wordarena_behavior_flash_path_events_total", "Behavioral signal: seats whose multi-cell submits arrived back-to-back faster than a person gestures (measurement only).", "counter", m.BehaviorFlashPathEvents)
	writeMetric("wordarena_behavior_multi_signal_events_total", "Behavioral signal: seats that fired two or more distinct signal families in one match (measurement only).", "counter", m.BehaviorMultiSignalEvents)
	writeMetric("wordarena_behavior_closed_with_signals_total", "Behavioral signal: matches closed with at least one flagged seat (42B longitudinal aggregate, measurement only).", "counter", m.ClosedWithSignals)
	writeMetric("wordarena_behavior_closed_flagged_seats_total", "Behavioral signal: flagged seats summed over closed matches (42B longitudinal aggregate, measurement only).", "counter", m.ClosedFlaggedSeats)
	writeMetric("wordarena_behavior_closed_family_seats_total", "Behavioral signal: (seat, family) incidences summed over closed matches (42B longitudinal aggregate, measurement only).", "counter", m.ClosedFamilySeats)
	writeMetric("wordarena_intent_rate_limited_total", "WebSocket connections closed by the per-seat intent rate limit.", "counter", m.IntentRateLimited)
	writeMetric("wordarena_behavior_max_rejection_streak", "Longest consecutive-rejection streak observed on any seat this process.", "gauge", uint64(m.BehaviorMaxRejectionStreak))
}
