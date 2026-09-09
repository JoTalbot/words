# Replay fixture — M0 deterministic match (seed 1512, en)
# Source: docs/WIRE-PROTOCOL.md §1; server/internal/prng golden.
# Validation command: cd server && go test ./internal/match/ -run TestReplay -v

match_id: 1
seed: 1512
language: en
tokens: ["tok0", "tok1"]
user_ids: [1, 2]

# First wave board (16 cells) generated deterministically from seed 1512.
# Submissions use letter_indices spelling dictionary words.
# Expected first event: WordValidatedEvent with ClientSequence echoed.

replay_assertions:
  - same seed -> identical board sequence
  - simultaneous claims resolve identically for both clients
  - replay of event log -> identical final score
  - reconnect within GraceTicks (300) resumes without reload

status: archive (exit-gate evidence for M0)
