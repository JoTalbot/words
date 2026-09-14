package main

// PostgreSQL backends for profiles and match results (M1 durable storage).
// Activated by WORDARENA_POSTGRES_DSN (see openPostgres). Uses pgx v5 via
// database/sql so the API surface stays small; schema is created idempotently
// on connect.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// errOwnerTokenNotSet reports a profile that cannot receive an owner token:
// either it does not exist or it already has one (M2 batch 32G).
var errOwnerTokenNotSet = errors.New("profile has no owner token slot (unknown profile or token already set)")

// isUniqueViolation reports whether err is Postgres' unique-constraint error
// (SQLSTATE 23505). The guild invariants lean on the unique indexes rather than
// on an application check, so recognising the violation is how the repo reports
// a race it lost instead of surfacing a 500.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "23505"
	}
	return false
}

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
    total_score    BIGINT      NOT NULL DEFAULT 0,
    -- SHA-256 of the profile's owner token (migration 005). NULL for profiles
    -- created before guilds existed: they cannot use a guild, which is the
    -- honest reading rather than a claimable id.
    owner_token_hash TEXT
);

CREATE UNIQUE INDEX IF NOT EXISTS players_owner_token_hash_key
    ON players (owner_token_hash)
    WHERE owner_token_hash IS NOT NULL;

CREATE TABLE IF NOT EXISTS guilds (
    id                BIGSERIAL   PRIMARY KEY,
    name              TEXT        NOT NULL,
    name_key          TEXT        NOT NULL,
    tag               TEXT        NOT NULL,
    language          TEXT        NOT NULL,
    founder_player_id BIGINT      NOT NULL,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS guilds_name_key_key ON guilds (name_key);
CREATE UNIQUE INDEX IF NOT EXISTS guilds_tag_key      ON guilds (tag);

CREATE TABLE IF NOT EXISTS guild_members (
    guild_id  BIGINT      NOT NULL REFERENCES guilds (id) ON DELETE CASCADE,
    player_id BIGINT      NOT NULL,
    role      TEXT        NOT NULL,
    joined_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (guild_id, player_id)
);

CREATE UNIQUE INDEX IF NOT EXISTS guild_members_one_guild_per_player
    ON guild_members (player_id);

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
    read_capability TEXT,
    -- Simulated-player disclosure (M2 batch 32D, Q5). bot_present is the
    -- cheapest possible form of the fact, so a future rating or reward job can
    -- exclude bot matches with one predicate instead of parsing JSON; bots
    -- carries the seats themselves for audit. Rows written before migration
    -- 004 default to no bots, which is the correct reading: those matches were
    -- created before a bot could be declared at all.
    bot_present     BOOLEAN NOT NULL DEFAULT false,
    bots            JSONB   NOT NULL DEFAULT '[]'::jsonb
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

// Owner-token storage for the Postgres profile store (M2 batch 32G). The token
// itself never reaches the database: only its SHA-256, so a leaked dump cannot
// be replayed as a player.
func (p *pgProfileStore) SetOwnerToken(id uint64, hash string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := p.db.ExecContext(ctx,
		`UPDATE players SET owner_token_hash = $1
		  WHERE id = $2 AND owner_token_hash IS NULL`, hash, id)
	if err != nil {
		return fmt.Errorf("postgres: set owner token: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return errOwnerTokenNotSet
	}
	return nil
}

func (p *pgProfileStore) PlayerIDByTokenHash(hash string) (uint64, bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var id uint64
	err := p.db.QueryRowContext(ctx, `SELECT id FROM players WHERE owner_token_hash = $1`, hash).Scan(&id)
	if err == sql.ErrNoRows {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("postgres: resolve owner token: %w", err)
	}
	return id, true, nil
}

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
       COALESCE(match_code, ''), COALESCE(read_capability, ''),
       bot_present, COALESCE(bots::text, '[]')`

// scanResult reads the resultColumns projection. Every field the caller cares
// about is filled here so the two read paths stay identical by construction.
func scanResult(row *sql.Row) (matchResult, []byte, error) {
	var res matchResult
	var rawID, rawSeed string
	var evJSON []byte
	var rawBots string
	err := row.Scan(
		&rawID, &rawSeed, &res.Language, &res.Over, &res.WinnerSeat,
		&res.IsTie, &res.Scores[0], &res.Scores[1], &res.StateVer,
		&res.ServerTick, &evJSON, &res.recordedAt, &res.Code, &res.ReadCap,
		&res.BotPresent, &rawBots)
	if err == nil && rawBots != "" {
		if jerr := json.Unmarshal([]byte(rawBots), &res.Bots); jerr != nil {
			return matchResult{}, nil, fmt.Errorf("postgres: bots column is not JSON: %w", jerr)
		}
	}
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
	botJSON, err := json.Marshal(res.Bots)
	if err != nil {
		return fmt.Errorf("postgres: marshal bots: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := p.db.ExecContext(ctx, `
		INSERT INTO match_results
			(match_id, seed, language, over, winner_seat, is_tie,
			 score0, score1, state_version, server_tick, events, recorded_at,
			 match_code, read_capability, bot_present, bots)
		VALUES ($1::numeric,$2::numeric,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,
		        NULLIF($13, ''), NULLIF($14, ''), $15, $16::jsonb)
		ON CONFLICT (match_id) DO NOTHING`,
		u64Param(res.MatchID), u64Param(res.Seed), res.Language, res.Over,
		res.WinnerSeat, res.IsTie,
		res.Scores[0], res.Scores[1], res.StateVer, res.ServerTick, evJSON,
		res.recordedAt, res.Code, res.ReadCap, res.BotPresent, botJSON); err != nil {
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

// ---- guilds (M2 batch 32G) ----
//
// The Postgres backend keeps the same invariants as the in-memory one, but the
// unique indexes - not the application - are what make them hold under
// concurrency: two callers racing to join the same guild cannot both win the
// one-guild-per-player index, and the losing transaction reports ErrAlreadyInGuild
// rather than writing a second row.

// pgGuildStore is the Postgres GuildRepo/owner-token implementation. It shares
// the connection pool with the profile and result stores.
type pgGuildStore struct {
	db *sql.DB
}

func newPgGuildStore(db *sql.DB) *pgGuildStore { return &pgGuildStore{db: db} }

func (pg *pgGuildStore) Close() error { return nil } // shared db closed by the API

// CreateGuild inserts the guild and its owner in one transaction, so a guild
// can never exist without its founder in the roster (invariant 4).
func (pg *pgGuildStore) CreateGuild(name, tag, lang string, founder uint64) (Guild, error) {
	name = strings.TrimSpace(name)
	tag = strings.ToUpper(strings.TrimSpace(tag))
	tx, err := pg.db.Begin()
	if err != nil {
		return Guild{}, err
	}
	defer func() { _ = tx.Rollback() }()

	var g Guild
	err = tx.QueryRow(
		`INSERT INTO guilds (name, name_key, tag, language, founder_player_id)
		 VALUES ($1, $2, $3, $4, $5)
		 RETURNING id, name, tag, language, founder_player_id, created_at`,
		name, normalizeGuildName(name), tag, lang, founder,
	).Scan(&g.ID, &g.Name, &g.Tag, &g.Language, &g.FounderPlayerID, &g.CreatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return Guild{}, ErrGuildNameTaken
		}
		return Guild{}, err
	}
	if _, err := tx.Exec(
		`INSERT INTO guild_members (guild_id, player_id, role) VALUES ($1, $2, $3)`,
		g.ID, u64Param(founder), guildRoleOwner); err != nil {
		if isUniqueViolation(err) {
			return Guild{}, ErrAlreadyInGuild
		}
		return Guild{}, err
	}
	if err := tx.Commit(); err != nil {
		return Guild{}, err
	}
	g.MemberCount = 1
	return g, nil
}

func (pg *pgGuildStore) GetGuild(id uint64) (GuildDetail, bool, error) {
	var d GuildDetail
	err := pg.db.QueryRow(
		`SELECT id, name, tag, language, founder_player_id, created_at
		   FROM guilds WHERE id = $1`, id,
	).Scan(&d.Guild.ID, &d.Guild.Name, &d.Guild.Tag, &d.Guild.Language,
		&d.Guild.FounderPlayerID, &d.Guild.CreatedAt)
	if err == sql.ErrNoRows {
		return GuildDetail{}, false, nil
	}
	if err != nil {
		return GuildDetail{}, false, err
	}
	members, err := pg.guildMembers(d.Guild.ID)
	if err != nil {
		return GuildDetail{}, false, err
	}
	d.Members = members
	d.Guild.MemberCount = len(members)
	return d, true, nil
}

func (pg *pgGuildStore) guildMembers(guildID uint64) ([]GuildMember, error) {
	rows, err := pg.db.Query(
		`SELECT player_id, role, joined_at FROM guild_members
		  WHERE guild_id = $1
		  ORDER BY joined_at, player_id`, guildID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []GuildMember
	for rows.Next() {
		var m GuildMember
		var pid string
		if err := rows.Scan(&pid, &m.Role, &m.JoinedAt); err != nil {
			return nil, err
		}
		m.PlayerID, err = scanU64("player_id", pid)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (pg *pgGuildStore) JoinGuild(guildID, player uint64) (GuildDetail, error) {
	tx, err := pg.db.Begin()
	if err != nil {
		return GuildDetail{}, err
	}
	defer func() { _ = tx.Rollback() }()

	// Lock the guild row so the cap check and the insert see one snapshot.
	var exists bool
	if err := tx.QueryRow(`SELECT true FROM guilds WHERE id = $1 FOR UPDATE`, guildID).Scan(&exists); err != nil {
		if err == sql.ErrNoRows {
			return GuildDetail{}, ErrGuildNotFound
		}
		return GuildDetail{}, err
	}
	var count int
	if err := tx.QueryRow(`SELECT count(*) FROM guild_members WHERE guild_id = $1`, guildID).Scan(&count); err != nil {
		return GuildDetail{}, err
	}
	if count >= guildMaxMembers {
		return GuildDetail{}, ErrGuildFull
	}
	if _, err := tx.Exec(
		`INSERT INTO guild_members (guild_id, player_id, role) VALUES ($1, $2, $3)`,
		guildID, u64Param(player), guildRoleMember); err != nil {
		if isUniqueViolation(err) {
			return GuildDetail{}, ErrAlreadyInGuild
		}
		return GuildDetail{}, err
	}
	if err := tx.Commit(); err != nil {
		return GuildDetail{}, err
	}
	d, _, err := pg.GetGuild(guildID)
	return d, err
}

func (pg *pgGuildStore) LeaveGuild(guildID, player uint64) (GuildDetail, bool, error) {
	tx, err := pg.db.Begin()
	if err != nil {
		return GuildDetail{}, false, err
	}
	defer func() { _ = tx.Rollback() }()

	var role string
	err = tx.QueryRow(
		`DELETE FROM guild_members WHERE guild_id = $1 AND player_id = $2 RETURNING role`,
		guildID, u64Param(player)).Scan(&role)
	if err == sql.ErrNoRows {
		// Distinguish "no such guild" from "not a member" for the caller.
		var ok bool
		if err := tx.QueryRow(`SELECT true FROM guilds WHERE id = $1`, guildID).Scan(&ok); err == sql.ErrNoRows {
			return GuildDetail{}, false, ErrGuildNotFound
		}
		return GuildDetail{}, false, ErrNotMember
	}
	if err != nil {
		return GuildDetail{}, false, err
	}

	var remaining int
	if err := tx.QueryRow(`SELECT count(*) FROM guild_members WHERE guild_id = $1`, guildID).Scan(&remaining); err != nil {
		return GuildDetail{}, false, err
	}
	if remaining == 0 {
		// Invariant 4: an empty guild stops existing, so it cannot squat on a
		// name forever.
		if _, err := tx.Exec(`DELETE FROM guilds WHERE id = $1`, guildID); err != nil {
			return GuildDetail{}, false, err
		}
		if err := tx.Commit(); err != nil {
			return GuildDetail{}, false, err
		}
		return GuildDetail{}, true, nil
	}
	if role == guildRoleOwner {
		// Invariant 4: ownership transfers to the earliest-joined member.
		if _, err := tx.Exec(
			`UPDATE guild_members SET role = $1
			  WHERE (guild_id, player_id) = (
			      SELECT guild_id, player_id FROM guild_members
			       WHERE guild_id = $2 ORDER BY joined_at, player_id LIMIT 1)`,
			guildRoleOwner, guildID); err != nil {
			return GuildDetail{}, false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return GuildDetail{}, false, err
	}
	d, _, err := pg.GetGuild(guildID)
	return d, false, err
}

func (pg *pgGuildStore) RemoveMember(guildID, actor, target uint64) (GuildDetail, error) {
	tx, err := pg.db.Begin()
	if err != nil {
		return GuildDetail{}, err
	}
	defer func() { _ = tx.Rollback() }()

	var actorRole string
	err = tx.QueryRow(
		`SELECT role FROM guild_members WHERE guild_id = $1 AND player_id = $2`,
		guildID, u64Param(actor)).Scan(&actorRole)
	if err == sql.ErrNoRows || actorRole != guildRoleOwner {
		return GuildDetail{}, ErrNotOwner
	}
	if err != nil {
		return GuildDetail{}, err
	}
	if actor == target {
		// There is no self-kick: leaving is the operation for that.
		return GuildDetail{}, ErrNotOwner
	}
	res, err := tx.Exec(
		`DELETE FROM guild_members WHERE guild_id = $1 AND player_id = $2`,
		guildID, u64Param(target))
	if err != nil {
		return GuildDetail{}, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return GuildDetail{}, err
	}
	if n == 0 {
		return GuildDetail{}, ErrNotMember
	}
	if err := tx.Commit(); err != nil {
		return GuildDetail{}, err
	}
	d, _, err := pg.GetGuild(guildID)
	return d, err
}

func (pg *pgGuildStore) GuildOf(player uint64) (Membership, bool, error) {
	var m Membership
	var gid string
	err := pg.db.QueryRow(
		`SELECT m.guild_id, g.name, g.tag, m.role
		   FROM guild_members m JOIN guilds g ON g.id = m.guild_id
		  WHERE m.player_id = $1`, u64Param(player),
	).Scan(&gid, &m.Name, &m.Tag, &m.Role)
	if err == sql.ErrNoRows {
		return Membership{}, false, nil
	}
	if err != nil {
		return Membership{}, false, err
	}
	m.GuildID, err = scanU64("guild_id", gid)
	if err != nil {
		return Membership{}, false, err
	}
	return m, true, nil
}
