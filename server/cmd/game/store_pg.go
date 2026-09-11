package main

// PostgreSQL backends for profiles and match results (M1 durable storage).
// Activated by WORDARENA_POSTGRES_DSN (see openPostgres). Uses pgx v5 via
// database/sql so the API surface stays small; schema is created idempotently
// on connect.

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// match_id and seed are Go uint64 but were declared BIGINT (int64) in the
// baseline schema, so any value with the top bit set failed to encode and the
// match result was silently dropped. Migration 002 widens both columns to
// NUMERIC(20,0), the exact unsigned 64-bit type. These two helpers are the only
// place that representation is decided: binding and scanning as decimal text
// keeps the round trip lossless and driver-independent, and leaves the stored
// value identical to the seed the HTTP API reports as JSON.
func u64Param(v uint64) string { return strconv.FormatUint(v, 10) }

func scanU64(col, s string) (uint64, error) {
	v, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("postgres: %s %q is not a uint64: %w", col, s, err)
	}
	return v, nil
}

const pgSchema = `
CREATE TABLE IF NOT EXISTS players (
    id             BIGSERIAL PRIMARY KEY,
    nickname       TEXT        NOT NULL,
    language       TEXT        NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    matches_played BIGINT      NOT NULL DEFAULT 0,
    wins           BIGINT      NOT NULL DEFAULT 0,
    losses         BIGINT      NOT NULL DEFAULT 0,
    draws          BIGINT      NOT NULL DEFAULT 0,
    total_score    BIGINT      NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS match_results (
    -- NUMERIC(20,0) is the exact uint64 type: BIGINT (int64) silently rejected
    -- every seed with the top bit set. See 002_match_results_uint64.sql.
    match_id      NUMERIC(20,0) PRIMARY KEY,
    seed          NUMERIC(20,0) NOT NULL,
    language      TEXT        NOT NULL,
    over          BOOLEAN     NOT NULL,
    winner_seat   INT         NOT NULL,
    is_tie        BOOLEAN     NOT NULL,
    score0        BIGINT      NOT NULL,
    score1        BIGINT      NOT NULL,
    state_version INT         NOT NULL,
    server_tick   INT         NOT NULL,
    events        JSONB       NOT NULL,
    recorded_at   TIMESTAMPTZ NOT NULL,
    -- Unguessable handle + per-match read credential (security finding S-2).
    -- Nullable: rows written before migration 003 have neither, and the service
    -- refuses a capability-gated read of such a row rather than inventing one.
    match_code      TEXT,
    read_capability TEXT
);
`

// openPostgres connects, pings and applies the schema. The returned *sql.DB
// is owned by the caller (closed on shutdown).
func openPostgres(dsn string) (*sql.DB, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("postgres: open: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("postgres: ping: %w", err)
	}
	if _, err := db.ExecContext(ctx, pgSchema); err != nil {
		db.Close()
		return nil, fmt.Errorf("postgres: migrate: %w", err)
	}
	return db, nil
}

// pgProfileStore is the Postgres ProfileRepo implementation. Stats are
// updated with SQL increments so concurrent matches fold safely.
type pgProfileStore struct {
	db *sql.DB
}

func (p *pgProfileStore) Create(nickname, lang string) (Profile, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var pr Profile
	err := p.db.QueryRowContext(ctx, `
		INSERT INTO players (nickname, language) VALUES ($1, $2)
		RETURNING id, nickname, language, created_at,
		          matches_played, wins, losses, draws, total_score`,
		nickname, lang).Scan(&pr.ID, &pr.Nickname, &pr.Language, &pr.CreatedAt,
		&pr.MatchesPlayed, &pr.Wins, &pr.Losses, &pr.Draws, &pr.TotalScore)
	if err != nil {
		return Profile{}, fmt.Errorf("postgres: create profile: %w", err)
	}
	return pr, nil
}

func (p *pgProfileStore) Get(id uint64) (Profile, bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var pr Profile
	err := p.db.QueryRowContext(ctx, `
		SELECT id, nickname, language, created_at,
		       matches_played, wins, losses, draws, total_score
		FROM players WHERE id = $1`, id).Scan(&pr.ID, &pr.Nickname, &pr.Language,
		&pr.CreatedAt, &pr.MatchesPlayed, &pr.Wins, &pr.Losses, &pr.Draws, &pr.TotalScore)
	if err == sql.ErrNoRows {
		return Profile{}, false, nil
	}
	if err != nil {
		return Profile{}, false, fmt.Errorf("postgres: get profile: %w", err)
	}
	return pr, true, nil
}

func (p *pgProfileStore) Record(id uint64, score int64, outcome string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// Unknown ids (synthetic seats) simply update zero rows — a no-op,
	// matching the in-memory backend's semantics.
	if _, err := p.db.ExecContext(ctx, `
		UPDATE players SET
			matches_played = matches_played + 1,
			total_score    = total_score + $2,
			wins           = wins   + CASE WHEN $3 = 'win'  THEN 1 ELSE 0 END,
			losses         = losses + CASE WHEN $3 = 'loss' THEN 1 ELSE 0 END,
			draws          = draws  + CASE WHEN $3 = 'draw' THEN 1 ELSE 0 END
		WHERE id = $1`, id, score, outcome); err != nil {
		return fmt.Errorf("postgres: record profile: %w", err)
	}
	return nil
}

func (p *pgProfileStore) Close() error { return nil } // shared db closed by the API

// pgResultStore is the Postgres ResultRepo implementation.
type pgResultStore struct {
	db *sql.DB
}

// resultColumns is the projection Get and GetByCode share. Keeping it in one
// place is what stops the two reads from drifting: a column added to one and
// forgotten in the other would scan into the wrong fields, and the scan targets
// below are positional.
//
// match_code and read_capability are COALESCEd because rows written before
// migration 003 have neither, and a NULL scanned into a Go string is an error
// rather than an empty value.
const resultColumns = `match_id::text, seed::text, language, over, winner_seat, is_tie,
       score0, score1, state_version, server_tick, events, recorded_at,
       COALESCE(match_code, ''), COALESCE(read_capability, '')`

// scanResult reads the resultColumns projection. Every field the caller cares
// about is filled here so the two read paths stay identical by construction.
func scanResult(row *sql.Row) (matchResult, []byte, error) {
	var res matchResult
	var rawID, rawSeed string
	var evJSON []byte
	err := row.Scan(
		&rawID, &rawSeed, &res.Language, &res.Over, &res.WinnerSeat,
		&res.IsTie, &res.Scores[0], &res.Scores[1], &res.StateVer,
		&res.ServerTick, &evJSON, &res.recordedAt, &res.Code, &res.ReadCap)
	if err != nil {
		return matchResult{}, nil, err
	}
	if res.MatchID, err = scanU64("match_id", rawID); err != nil {
		return matchResult{}, nil, err
	}
	if res.Seed, err = scanU64("seed", rawSeed); err != nil {
		return matchResult{}, nil, err
	}
	return res, evJSON, nil
}

func (p *pgResultStore) Put(res matchResult) error {
	evJSON, err := json.Marshal(res.Events)
	if err != nil {
		return fmt.Errorf("postgres: marshal events: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := p.db.ExecContext(ctx, `
		INSERT INTO match_results
			(match_id, seed, language, over, winner_seat, is_tie,
			 score0, score1, state_version, server_tick, events, recorded_at,
			 match_code, read_capability)
		VALUES ($1::numeric,$2::numeric,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,
		        NULLIF($13, ''), NULLIF($14, ''))
		ON CONFLICT (match_id) DO NOTHING`,
		u64Param(res.MatchID), u64Param(res.Seed), res.Language, res.Over,
		res.WinnerSeat, res.IsTie,
		res.Scores[0], res.Scores[1], res.StateVer, res.ServerTick, evJSON,
		res.recordedAt, res.Code, res.ReadCap); err != nil {
		return fmt.Errorf("postgres: put result: %w", err)
	}
	return nil
}

// MaxMatchID reads the durable high-water mark so a restart continues the
// sequence instead of reusing ids.
func (p *pgResultStore) MaxMatchID() (uint64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var raw string
	if err := p.db.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(match_id), 0)::text FROM match_results`).Scan(&raw); err != nil {
		return 0, fmt.Errorf("postgres: max match id: %w", err)
	}
	id, err := scanU64("max match id", raw)
	if err != nil {
		return 0, err
	}
	return id, nil
}

func (p *pgResultStore) Get(id uint64) (matchResult, bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, evJSON, err := scanResult(p.db.QueryRowContext(ctx,
		`SELECT `+resultColumns+` FROM match_results WHERE match_id = $1::numeric`,
		u64Param(id)))
	if err == sql.ErrNoRows {
		return matchResult{}, false, nil
	}
	if err != nil {
		return matchResult{}, false, fmt.Errorf("postgres: get result: %w", err)
	}
	if err := json.Unmarshal(evJSON, &res.Events); err != nil {
		return matchResult{}, false, fmt.Errorf("postgres: unmarshal events: %w", err)
	}
	return res, true, nil
}

// GetByCode resolves an unguessable match code. This is the read path that
// survives a restart once WORDARENA_REQUIRE_READ_CAPABILITY is set: the
// in-process code index is gone after a restart, so the code has to be findable
// in the durable store, and the capability has to come back with the row for the
// gate to compare against.
func (p *pgResultStore) GetByCode(code string) (matchResult, bool, error) {
	if code == "" {
		return matchResult{}, false, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, evJSON, err := scanResult(p.db.QueryRowContext(ctx,
		`SELECT `+resultColumns+` FROM match_results WHERE match_code = $1`, code))
	if err == sql.ErrNoRows {
		return matchResult{}, false, nil
	}
	if err != nil {
		return matchResult{}, false, fmt.Errorf("postgres: get result by code: %w", err)
	}
	if err := json.Unmarshal(evJSON, &res.Events); err != nil {
		return matchResult{}, false, fmt.Errorf("postgres: unmarshal events: %w", err)
	}
	return res, true, nil
}
