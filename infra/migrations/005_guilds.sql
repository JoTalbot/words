-- Guild foundation, and the identity primitive it needs (M2 batch 32G).
--
-- A guild is a durable, privileged relationship between players, unlike the
-- two kinds of durable state that existed before this migration: match results
-- (protected by per-match capability tokens, alive only for the match) and
-- player profiles (anonymous: anyone may read any profile, nothing
-- authenticates a caller as a player). Building a roster on a caller-supplied
-- player_id would let anyone manage any guild as anyone, and that abuse would
-- already be in the data by the time a permission check was added. Hence the
-- first statement below.
--
-- Owner tokens are stored as a SHA-256 hash, never in the clear, for the same
-- reason seat tokens are unguessable: a database read must not be enough to act
-- as a player. owner_token_hash is NULLable on purpose - profiles created
-- before this migration have no token and therefore cannot use a guild, which
-- is the honest reading. Minting a token for an existing profile would mean
-- letting any caller claim an unclaimed id, which is exactly the hole this
-- column closes.
--
-- Indexes are declared in the same idempotent style as the tables so that a
-- service-created schema (pgSchema) and a migrated one stay identical; the
-- drift gate compares them.

ALTER TABLE players
    ADD COLUMN IF NOT EXISTS owner_token_hash TEXT;

CREATE UNIQUE INDEX IF NOT EXISTS players_owner_token_hash_key
    ON players (owner_token_hash)
    WHERE owner_token_hash IS NOT NULL;

CREATE TABLE IF NOT EXISTS guilds (
    id                BIGSERIAL   PRIMARY KEY,
    name              TEXT        NOT NULL,
    -- name_key is lower(name): invariant 2 makes names unique
    -- case-insensitively, so a roster screenshot cannot be forged with
    -- "KATYA" next to "katya".
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

-- Invariant 1: one guild per player. The constraint, not the application, is
-- what makes a concurrent double-join impossible.
CREATE UNIQUE INDEX IF NOT EXISTS guild_members_one_guild_per_player
    ON guild_members (player_id);
