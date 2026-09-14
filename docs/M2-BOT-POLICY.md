# M2 — Simulated players: disclosure and eligibility policy

Status: **decided and implemented** (M2 batch 32D). Register entry: Q5 in
`docs/PRODUCT-DECISIONS.md`.

## The rule in one sentence

A seat played by a simulated player is **declared by the server, disclosed to
every observer on every surface, and recorded as rating- and reward-ineligible**
in the authoritative result — and the server never creates one to fill a seat.

## Why it is decided this way

The product requirement (Q5) is negative: bots must not silently impersonate
human players in ranked competition. Written as requirements that can be
enforced, that becomes three clauses, each of which has an opposite that no
later feature could repair:

| Clause | If we got it wrong |
|---|---|
| Declared, never inferred | A human mislabelled a bot is insulting and corrupts their stats; a bot mislabelled human is exactly the masquerade Q5 forbids. Guessing produces both. |
| Disclosed on every surface | A disclosure a client can forget, bypass or clear is not a disclosure, and a player who cannot know whether they played a person has been deceived by the product. |
| Never rating/reward eligible | A rating that can be gained against a bot is not a rating. This is farmable, and it is farmable at scale. |

The clauses are ordered: declaration makes disclosure possible, disclosure makes
eligibility auditable.

## Where the fact lives

```
client request (gated)            match creation             room
  bot_seats: [1,3]        ──────▶  Config.BotSeats   ──────▶  match.PlayerState.IsBot
  POST /v1/queue {"bot":true}                                   │
                                                                ▼
wire: PlayerState.is_bot  ◀── snapshot + delta + HTTP state view + telemetry
result: bots / bot_present / rating_eligible  ◀── durable (migration 004)
```

Nothing derives bot-ness from an absent profile. `PlayerState.IsBot` is set once,
at construction, from an explicit declaration; every other surface reads it.

The declaration is part of `Match.Fingerprint`, so a match in which a seat was a
bot cannot replay equal to one in which it was not — a silently substituted
opponent would otherwise be invisible to the replay audit.

## What a caller may and may not say

| Request | Effect |
|---|---|
| `POST /v1/matches` with no `bot_seats` | every seat is a human seat (default) |
| `POST /v1/matches` with `bot_seats: [1,3]` | seats 1 and 3 are declared bots, disclosed to all seats |
| `bot_seats` with a seat outside the roster, negative, or twice | `400` — the caller has a bug and should hear about it rather than have it silently deduplicated |
| `bot_seats` on a deployment with `WORDARENA_ALLOW_BOT_SEATS` off (the default) | `400`, naming the switch |
| `POST /v1/queue` with `"bot": true` | the queue entry declares itself; the room it lands in discloses that seat |
| `"bot": true` together with `player_id` | `400` — a bot must not accrue a human's lifetime stats |
| any request that tries to *clear* a declaration | no such field exists |

`WORDARENA_ALLOW_BOT_SEATS` defaults to **false**: declaring bot-ness is a
QA/practice capability, not something an arbitrary caller asserts about a match.
The gated default and the named refusal follow the same pattern as
`WORDARENA_MAX_SEATS` (batch 32B).

## The live flow this fixes

`headless-bot -partner` joins through the **queue** — that is how the M1 device
smoke pairs a Unity client on an emulator with a bot opponent. Before this batch
that bot was indistinguishable from a human in the one place bots actually
appear today. Now the partner declares itself, the room marks its seat, and the
device client is told. The device leg therefore exercises the disclosure path
rather than bypassing it.

## Operational consequence for bot-driven test legs

`headless-bot` declares itself a bot on **every** queue call — the exit gate
(`-via-queue`, where both seats are the bot) and the device-smoke partner. There
is intentionally no flag to suppress it: the program is a simulated player, and
a simulated player that joins without saying so is the impersonation this policy
forbids.

The consequence is deliberate, not incidental:

- a deployment that hosts a bot-driven leg must set
  `WORDARENA_ALLOW_BOT_SEATS=true`. The live dev service is such a deployment
  (the exit gate and the device smoke run against it); a stock deployment
  refuses the enqueue with a message naming the switch;
- matches produced by those legs are recorded as **bot matches** and are not
  rating-eligible. That is the honest description of what they are. The exit
  gate's 43:45 / 37:60 / 44:40 baselines remain valid as *physics*, and are no
  longer mistakable for ranked results;
- the device smoke therefore exercises the disclosure path rather than bypassing
  it: the Unity client is told that its opponent is a bot, on the wire, during
  the run.

## What it deliberately does not do

- **No backfill.** The server never creates a bot to fill an under-filled
  lobby. The human-only short-handed start (batch 31C) remains the answer to low
  population: a small honest match beats a full dishonest one. This is the
  clause that keeps the feature honest, and it is the reason the "bot backfill"
  question on the roadmap row is closed as *decided against*, not deferred.
- **No bot mode.** A labelled practice/casual bot mode, a separate bot rating
  pool, and bot difficulty are feature decisions this policy makes safe to take
  later; none of them is taken here.
- **No detection.** The server does not attempt to classify a seat as a bot
  from behaviour. Detection would be a guess, and clause 1 is precisely that
  guesses are not allowed to decide who is who.

## Validation

`server/cmd/game/botpolicy_test.go`:

| Test | Clause |
|---|---|
| `TestHumanMatchDisclosesNoBots` | A match nobody declared a bot in is bot-free and rating-eligible — the compatibility statement. |
| `TestDeclaredBotIsVisibleToEverySeat` | Every observer sees the disclosure in the snapshot **and** the HTTP state view; the room agrees. |
| `TestBotMatchIsNotRatingEligible` | The result carries the seats, the flag, and `rating_eligible=false`. |
| `TestBotDeclarationIsRefusedByDefault` | A stock deployment cannot be made to assert bot-ness. |
| `TestBotDeclarationIsValidated` | Out-of-range, negative and duplicate seats are refused. |
| `TestBotnessIsNeverInferred` | Anonymous seats are humans. |
| `TestQueueBotMustDeclareItself` | The queue path discloses, the opponent is told, a bot cannot bind a profile, and the deployment switch gates it. |
| `TestMatchmakerNeverInventsBots` | Two humans pair into a bot-free match: no silent filling. |

Plus, outside this file:

- `server/internal/match/botseats_test.go` — the declaration is in the
  fingerprint and is never inferred.
- `server/internal/protocol/delta_test.go` — `is_bot` participates in the delta
  change set (a wire-visible field that could not be delivered would be an
  invisible correctness hole).
- `server/cmd/migrate` — the baseline-schema drift gate covers migration 004.
