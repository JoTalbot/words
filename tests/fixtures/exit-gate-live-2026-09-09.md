# M0 exit-gate — live transport evidence (2026-09-09)

Harness: `server/cmd/headless-bot` (exact-cover offline solver + two real
WebSocket clients). Reproduce:

```bash
cd server
go build -o /tmp/headless-bot ./cmd/headless-bot
WORDARENA_ADDR=http://127.0.0.1:18080 /tmp/headless-bot -rounds 2 -seeds 1512,1513,1517 -v
```

Dev host: OCI Ampere A1 (`arm-server-01`), service on 127.0.0.1:18080
(`/tmp/words-game`, commit 1a3d377). Result: `EXIT-GATE: PASS`, 6/6 full
matches (2 rounds x seeds 1512/1513/1517).

Per-match assertions (all true):

| seed | intents | final scores | terminal_snapshots_equal | client_streams_equal | elapsed |
|---|---|---|---|---|---|
| 1512 | 12 | 43:45 | true | true | ~3.0 s |
| 1513 | 12 | 37:60 | true | true | ~3.0 s |
| 1517 | 12 | 44:40 | true | true | ~3.0 s |

Identical scores across both rounds and equal to the accelerated offline
replay of the same intent script prove reproducibility of the event log
(M0 acceptance 5) and identical final state for both clients (M0 exit
gate). The protocol terminal `over=true` snapshot (MatchStateSnapshot.over,
field 8) lets clients deterministically detect match end.

Related: automated CI coverage `TestExitGateRepeatedFullMatches`
(server/cmd/game/exitgate_test.go) runs the same property in-process.
