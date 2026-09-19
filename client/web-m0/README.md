# M0 browser transport diagnostic

This is a transport proof, **not** a playable client or a protobuf decoder.
A pass proves that an authenticated real browser socket receives a nonempty
binary frame. Gameplay/replay assertions live in the Go/headless/device suites.

The page uses its own HTTP(S) origin by default (HTTP -> WS, HTTPS -> WSS).
For a different test server or a `file://` page, supply `server` as an explicit
HTTP(S)/WS(S) base URL alongside `match_id` and `token`. Never put a production
seat token in a shared URL. The page disables referrers and does not render
credentials. Origin allowlisting on the server is still enforced.

Run the repeatable smoke against an **isolated** game instance:

```sh
python3 -m pip install playwright==1.63.0
python3 -m playwright install --with-deps chromium
# Run a game build separately with WORDARENA_ADDR=127.0.0.1:18110.
WORDARENA_SERVER_URL=http://127.0.0.1:18110 python3 client/web-m0/test-smoke.py
```

The smoke creates one disposable English match, or uses both supplied
`WORDARENA_MATCH_ID` and `WORDARENA_TOKEN`. Missing server configuration is a
failure, never a fallback to the live port. Five checks: real OPEN + binary
frame, invalid token refused, invalid URL scheme refused, unreachable endpoint
refused, missing credentials refused. `WORDARENA_SCREENSHOT` optionally captures
the positive state. Temporary page-server logs are suppressed to avoid token
leaks. CI runs the same smoke with a same-commit server on a dedicated port.

Batch 44B replaces the old tolerant check that accepted connection errors as
PASS and ignored the documented `WORDARENA_SERVER_URL` setting.
