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
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

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
    match_id      BIGINT      PRIMARY KEY,
    seed          BIGINT      NOT NULL,
    language      TEXT        NOT NULL,
    over          BOOLEAN     NOT NULL,
    winner_seat   INT         NOT NULL,
    is_tie        BOOLEAN     NOT NULL,
    score0        BIGINT      NOT NULL,
    score1        BIGINT      NOT NULL,
    state_version INT         NOT NULL,
    server_tick   INT         NOT NULL,
    events        JSONB       NOT NULL,
    recorded_at   TIMESTAMPTZ NOT NULL
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
			 score0, score1, state_version, server_tick, events, recorded_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
		ON CONFLICT (match_id) DO NOTHING`,
		res.MatchID, res.Seed, res.Language, res.Over, res.WinnerSeat, res.IsTie,
		res.Scores[0], res.Scores[1], res.StateVer, res.ServerTick, evJSON,
		res.recordedAt); err != nil {
		return fmt.Errorf("postgres: put result: %w", err)
	}
	return nil
}

// MaxMatchID reads the durable high-water mark so a restart continues the
// sequence instead of reusing ids.
func (p *pgResultStore) MaxMatchID() (uint64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var id uint64
	if err := p.db.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(match_id), 0) FROM match_results`).Scan(&id); err != nil {
		return 0, fmt.Errorf("postgres: max match id: %w", err)
	}
	return id, nil
}

func (p *pgResultStore) Get(id uint64) (matchResult, bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var res matchResult
	var evJSON []byte
	err := p.db.QueryRowContext(ctx, `
		SELECT match_id, seed, language, over, winner_seat, is_tie,
		       score0, score1, state_version, server_tick, events, recorded_at
		FROM match_results WHERE match_id = $1`, id).Scan(
		&res.MatchID, &res.Seed, &res.Language, &res.Over, &res.WinnerSeat,
		&res.IsTie, &res.Scores[0], &res.Scores[1], &res.StateVer,
		&res.ServerTick, &evJSON, &res.recordedAt)
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
