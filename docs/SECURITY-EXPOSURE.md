# M1 public exposure — abuse, capacity, rollback notes

Companion to the Q8 decision (docs/PRODUCT-DECISIONS.md, 2026-09-13:
**open**, staged). Stage 1 is the Cloudflare quick tunnel deployed by
infra/expose-quick.sh; stage 2 (stable domain) re-uses these notes and
re-runs the review for a permanent URL.

## What is exposed

`https://<random>.trycloudflare.com` -> Cloudflare edge (TLS terminated
there) -> outbound-only tunnel on the OCI host -> the match service on
`127.0.0.1:18080`. The origin listen address does NOT change; no public
TCP port is opened; the host's firewall sees only the tunnel's outbound
QUIC/TLS flow. Exposed surface: the same REST + WebSocket API that
host-local tools already use (players, queue, matches, results, metrics).

## What is NOT exposed

- Metrics/telemetry: `/metrics/prometheus` and `/healthz`/`/readyz` are
  trivially low-signal, but the smoke uses them through the public URL;
  for stage 2, block `/metrics*` at the edge (Caddy path deny or
  Cloudflare WAF rule).
- Nothing new is reachable that was not already reachable at
  127.0.0.1:18080 on the host. No filesystem, no admin endpoints, no
  migrations (the migrate tool is offline, run only by the operator).

## Abuse cases (security-change documentation per project rules)

1. **Unauthenticated match creation / queue spam.** Anyone with the URL
   can POST /v1/matches, /v1/queue, /v1/players.
   - Existing controls: per-caller fixed-window mutation rate limiting
     (server/internal/security, bounded memory — deliberately not a
     sliding window, which is itself a memory-amplification vector),
     bounded request bodies, server-authoritative simulation (a spammer
     cannot influence another match's outcome, scoring or legality).
   - Exposure delta: the URL leaves the host. Mitigations: (a) the quick
     tunnel URL is a random subdomain and is not published anywhere
     except the operator channel; (b) rotating the tunnel (restart)
     invalidates it instantly; (c) no PII, no payments, no rankings —
     the worst case is wasted CPU on simulated matches on a dev VM.
   - Stage 2 must add: per-IP limits at the edge (Cloudflare WAF rate
     limit rule on the match-mutation POSTs) and a decision on whether
     public match creation requires an access token (the service's
     token model already gates the match lifecycle after creation).
2. **WebSocket handshake from browsers.** The OriginPolicy
   (WORDARENA_WS_ALLOWED_ORIGINS, default "private") rejects every
   browser Origin that is not loopback/private; the public URL's origin
   is not private, so a malicious web page cannot open a match socket to
   the service using a stolen token. The Unity client is native (no
   Origin header) and is unaffected — this is the "origin allowlisting
   for the Unity client" item from Q8: nothing to allowlist, the default
   policy already admits exactly the native client.
3. **DoS against the dev VM.** The tunnel adds a Cloudflare edge in
   front, which absorbs volumetric floods; the remaining application
   layer is rate-limited per caller. A determined single-IP hammer can
   still load one VM — accepted for stage 1 (dev service, operator can
   kill the tunnel in one command); stage 2 adds edge-level per-IP
   thresholds.
4. **Token theft / shoulder-surfing.** Match tokens are created by the
   service and returned over TLS; they are session-scoped and never
   logged. No change from the loopback era, but the TLS path is now real
   public internet traffic — the Unity client uses the platform trust
   store (Cloudflare edge certificate = public CA), no custom pins.

## Capacity notes (infrastructure-change requirement)

- Measured baseline: the M1 exit gate (3 seeds, 1 round each) finishes
  in well under a minute of CPU on the host; smoke 27/0 passes on
  15:07Z 2026-09-13.
- Expected load stage 1: operator + one or two real devices + the
  emulator smoke (at most one concurrent match) + the headless-bot
  opponent for the 28b loop (CPU work is the same simulation the exit
  gate already runs). Comfortably inside the VM's budget.
- The tunnel itself is one lightweight process (cloudflared, outbound
  only, restarts on failure via systemd).

## Rollback

```
bash infra/expose-quick.sh --stop
```
= `systemctl disable --now words-tunnel`. The public URL stops resolving
to the service within seconds; the service keeps running loopback-only
with zero configuration change (it was never changed). No data impact:
matches are session-scoped; in-flight matches drop and their clients
reconnect or re-queue.

## Finding 2026-09-17 (batch 37C) - caller identity behind the tunnel

Severity: medium, known surface, mitigations already queued in stage 2.

1. Room-table exhaustion through unauthenticated match creation is
   PRE-EXISTING, not introduced by M2: rooms outlive their clients by
   design (32F measured a 180 s drain), so a caller creating matches at the
   mutation budget (120/min) can hold ~360 concurrent rooms and push the
   service to its room cap. `pve:true` (WORDARENA_ALLOW_BOT_SEATS=true on
   the live deployment since 2026-09-14, for the practice mode) only
   simplifies the farm: the server drives a bot in each room without the
   caller keeping a socket open. The ceiling is unchanged.
2. WORDARENA_TRUST_PROXY_HEADERS=true was UNSAFE behind Cloudflare before
   this patch: ClientIP keyed on the FIRST X-Forwarded-For entry, which a
   client controls end-to-end (the edge only appends). Enabling per-IP
   limits on that basis would have let one abuser mint unlimited rate-limit
   identities - worse than the shared budget it replaces.
   Disposition: FIXED in code (batch 37C). ClientIP now prefers
   CF-Connecting-IP, which Cloudflare replaces with the edge-observed
   address; XFF remains the documented fallback for non-Cloudflare proxies.
   Behaviour with trustProxy off (current live config) is unchanged.
3. Interim posture is fail-closed: with trustProxy off, all tunnel callers
   share one 120/min mutation budget (keyed 127.0.0.1). An abuser cannot
   bypass it, only exhaust it for everyone; reads and in-flight matches are
   unaffected, and the budget refills every minute.

Stage-2 prerequisite delivered by this patch; flipping
WORDARENA_TRUST_PROXY_HEADERS on the live service is a deploy-config step
to take together with the /metrics deny decision, not silently.

## Open items

- Stage 2: domain + edge per-IP limits (code prerequisite landed in 37C:
  CF-Connecting-IP-aware caller identity) + /metrics deny + token decision
  (docs/PRODUCT-DECISIONS.md Q8, step 2).
- Re-verify live (smoke + exit gate) after the tunnel is up, from both
  the host (loopback) and a device (public URL).
