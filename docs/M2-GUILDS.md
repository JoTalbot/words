# M2 — Guild foundation (batch 32G)

Status: design note + the first durable slice (guilds, rosters, membership rules
and the identity primitive they need). Register: M2 row "guild foundation".

## Why this row needed a note before code

The roadmap flagged this row as "needs a design note first - it touches
persistence and identity", and reading the code says why. Everything durable the
server has today belongs to one of two categories:

- **match state**, which is protected by unguessable per-seat capability tokens
  and lives only as long as the match; and
- **player profiles**, which are *anonymous*: `POST /v1/players` assigns an id
  and anyone may read any profile. Nothing authenticates a caller as a player.

A guild is neither. It is a durable, *privileged* relationship between players:
a membership is not a seat in one match, and "who may add, remove or rename a
member" is exactly the question the current model cannot answer. Building a guild
roster on top of `player_id` alone would let anyone join, leave or manage any
guild as anyone — a design that cannot be fixed later by a permission check,
because the abuse would already be in the data.

So the note does two things: it fixes the guild model, and it names the smallest
identity primitive that makes the model enforceable. It deliberately does NOT
attempt the full M2 auth scope (accounts, sessions, recovery, per-account
ratings); that remains open and this note does not pretend otherwise.

## The identity primitive

`POST /v1/players` now returns, exactly once, an **owner token** for the new
profile:

```json
{"id": 41, "nickname": "katya", "language": "uk", "owner_token": "9f2c…"}
```

- 128 bits from `crypto/rand`; only the SHA-256 hash is stored
  (`players.owner_token_hash`), so a database read cannot impersonate a player.
- Presented as `X-Player-Token: <token>` on every guild mutation. The token
  *is* the identity: no endpoint takes a `player_id` from the caller and trusts
  it, which is what makes impersonation structurally impossible rather than
  policed.
- **Profiles that predate this batch have no token and cannot use a guild.** Mint
  a token for an existing profile would mean letting any caller claim an
  unclaimed id, which is precisely the hole this primitive closes. Alpha
  profiles are disposable; a real product would add account recovery instead
  (out of scope, listed below).

This is deliberately the *least* auth that makes guilds honest. It is not a
session model, it has no expiry, and it is not a substitute for the M2 identity
work — it is the piece that guilds cannot exist without.

## The model

```
guilds         id, name, name_key, tag, language, founder_player_id, created_at
guild_members  guild_id, player_id, role, joined_at   PK(guild_id, player_id)
```

Invariants, each of which is a test:

1. **One guild per player.** `UNIQUE (player_id)` — a player is in one guild or
   none. Multi-guild membership is a different feature (and a different abuse
   surface); it is not something to discover later from the data.
2. **Names and tags are unique**, case-insensitively (`name_key = lower(name)`),
   so a roster screenshot cannot be forged with `KATYA` vs `katya`. Name: 3–24
   characters, letters/digits/`-` `_` `.`, first character a letter or digit.
   Tag: 2–5 characters, `A–Z0–9`, stored uppercased.
3. **The founder owns the guild.** Roles are `owner` and `member`; only the owner
   may remove another member. There is no self-kick — leaving is the operation
   for that, and conflating them is how "I accidentally kicked myself" happens.
4. **Ownership is never orphaned.** If the owner leaves while others remain, the
   earliest-joined member becomes owner. If the owner is the last member, the
   guild is dissolved and its id stops existing. There is no headless guild, and
   no guild survives on a member who left.
5. **Size is capped** (`guildMaxMembers`, 50 in this slice) so a single guild
   cannot become an unbounded broadcast target later. The cap is a deployment
   constant, not a schema constraint, because the right number is a product
   question.
6. **A guild is readable by anyone, mutable only by its members.** `GET` needs no
   token: rosters are public information in a social game, and making them
   secret would make moderation harder, not easier.

## Endpoints

| method | path | token | meaning |
|---|---|---|---|
| `POST` | `/v1/guilds` | required | create a guild (`{"name":…,"tag":…,"language":…}`); caller becomes owner |
| `GET` | `/v1/guilds/{id}` | none | guild + roster (player ids, nicknames, roles) |
| `POST` | `/v1/guilds/{id}/members` | required | join; open join in this slice |
| `DELETE` | `/v1/guilds/{id}/members/{player_id}` | required | leave (`{player_id}` = caller) or remove (owner only) |
| `GET` | `/v1/players/{id}/guild` | none | which guild a player is in, with their role |

Refusals carry the reason, per the same convention as the seats and bot
surfaces: `401` no/invalid token, `403` not the owner, `404` unknown guild or
member, `409` already in a guild / guild full / name or tag taken, `400`
validation.

## What is deliberately NOT in this slice

Each of these is a real feature with a real decision inside it, and none of them
is needed for the foundation to be honest:

- **Guild chat.** Message storage, retention, reporting and moderation duty are
  a product/safety decision (and a legal one), not an engineering default. A
  guild that can talk needs a policy for what happens when it abuses that. The
  foundation is useful without it.
- **Invite-only join, join requests, co-owners.** Open join is the simplest
  honest rule; the others change the model (invites are durable artifacts with
  their own lifecycle) and are worth doing deliberately.
- **Guild-vs-guild matchmaking, guild ratings, rewards.** All of these depend on
  the rating system (M3), and Q5 clause 3 already fixes what a bot match means
  for eligibility.
- **Name moderation, rename, dissolution by an operator.** Name validation is
  mechanical (length, charset, uniqueness); *judging* a name is a human process
  that does not exist yet.
- **Bots in guilds.** A bot has no profile at all — Q5's "a bot may not bind a
  human profile" already makes it impossible for a simulated seat to hold
  membership, so there is nothing to add here.

## Abuse analysis (what stops a griefer)

| attack | why it fails |
|---|---|
| act as another player | every mutation derives identity from the token, never from a body field |
| claim an existing profile | tokens are minted only at profile creation; legacy profiles cannot be claimed |
| hoard guild names | name/tag uniqueness, one guild per player per creation, plus the per-caller mutation limit (120/min, measured in 32F) |
| squat inside a guild | owner can remove any member; a member who removes themselves is the leave path |
| capture a guild by waiting for the owner to leave | ownership transfers to the earliest-joined member — deterministic, visible in the roster, and never to an empty guild |
| grow a guild into a broadcast amplifier | `guildMaxMembers` cap, and a bounded roster response |

## Follow-ups this note creates (not blockers)

1. Which of the three "not in this slice" features comes first, and with what
   evidence — chat is the candidate with real obligations attached.
2. The size cap number (50 is a placeholder chosen to be small enough to be
   safe and large enough to be useful).
3. Whether guild membership should ever affect matchmaking (it currently does
   not, and Q-series does not cover it).
4. Whether the owner token should be the seed of the real M2 identity work
   (rotation, recovery, multiple devices) or be replaced outright by sessions.
   This note deliberately leaves that open rather than growing a token into an
   account system by accident.

## Reproducing the guarantees

```sh
cd server && go test ./cmd/game/ -run 'Guild|PlayerToken' -v
cd server && go test ./cmd/migrate/ -run Baseline   # migration 005 vs the service schema
```

## Measured cost

A guild read is one row plus one roster query; a mutation is one row plus a
membership row. Nothing about guilds is on the match path — the room ticker, the
wire format and the byte budget measured in 32F are untouched by this batch, and
`docs/M2-LOAD-TESTING.md` remains the reference for capacity.
