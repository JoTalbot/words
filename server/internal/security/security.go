// Package security holds the transport-level abuse protections for the
// authoritative match service: origin policy for browser clients, bounded
// request bodies, per-caller mutation rate limiting and input validation
// helpers.
//
// Nothing in this package may influence match outcome, scoring, ordering or
// legality. Those are decided by the simulation alone (docs/ARCHITECTURE.md
// "server-authoritative model"). A limit here may reject a request earlier,
// never change what a successful request means.
package security

import (
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// Origin policy
// ---------------------------------------------------------------------------

// OriginPolicy decides whether an HTTP request that carries browser
// semantics may reach the WebSocket endpoint.
//
// Native clients (Unity, headless tooling) send no Origin header, so the
// default policy must keep working without one. A browser always sends
// Origin on a WebSocket handshake, which is exactly what makes
// cross-site WebSocket hijacking possible: a page on evil.example can open
// a socket to the game service using a token it somehow holds. Requiring an
// explicit allowlist closes that path without touching authentication.
//
// The zero value allows any request with no Origin header and rejects every
// browser origin; that is the safe default for a loopback dev service.
type OriginPolicy struct {
	// allowed holds normalized origins (scheme://host[:port], lowercase).
	allowed map[string]struct{}
	// allowPrivateHosts permits origins whose host is loopback or a
	// private/interface address, in addition to `allowed`. It exists so a
	// developer's browser on http://127.0.0.1:18080 works without config.
	allowPrivateHosts bool
}

// NewOriginPolicy builds a policy from a comma-separated allowlist (the
// WORDARENA_WS_ALLOWED_ORIGINS value). An empty list means "no browser
// origin is trusted". The special entry "private" (default) additionally
// accepts loopback/private origins.
func NewOriginPolicy(list string) *OriginPolicy {
	p := &OriginPolicy{allowed: map[string]struct{}{}}
	list = strings.TrimSpace(list)
	if list == "" {
		p.allowPrivateHosts = true
		return p
	}
	for _, raw := range strings.Split(list, ",") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		if raw == "private" {
			p.allowPrivateHosts = true
			continue
		}
		if norm, ok := NormalizeOrigin(raw); ok {
			p.allowed[norm] = struct{}{}
		}
	}
	return p
}

// NormalizeOrigin lowercases and strips path/query fragments from an origin.
// It reports false for anything that is not a bare http(s) origin.
func NormalizeOrigin(s string) (string, bool) {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return "", false
	}
	if !strings.Contains(s, "://") {
		s = "https://" + s // bare host[:port] is interpreted as https
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return "", false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", false
	}
	if u.Path != "" && u.Path != "/" {
		return "", false
	}
	return u.Scheme + "://" + u.Host, true
}

// Allow reports whether the request's Origin header is acceptable. A missing
// header is always acceptable (non-browser client); a present one must match.
func (p *OriginPolicy) Allow(r *http.Request) bool {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return true
	}
	norm, ok := NormalizeOrigin(origin)
	if !ok {
		return false
	}
	if _, allowed := p.allowed[norm]; allowed {
		return true
	}
	if p.allowPrivateHosts && isPrivateOrigin(norm) {
		return true
	}
	return false
}

func isPrivateOrigin(norm string) bool {
	u, err := url.Parse(norm)
	if err != nil || u.Hostname() == "" {
		return false
	}
	host := u.Hostname()
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast()
}

// ---------------------------------------------------------------------------
// Per-caller rate limiting
// ---------------------------------------------------------------------------

// Limiter is a fixed-window counter per key with bounded memory.
//
// A sliding window would need to keep a slice per caller, which is itself a
// memory-amplification vector under attack; a fixed window keeps two
// integers. The trade-off is a short burst at a window boundary, which is
// acceptable for abuse protection (it is not a fairness mechanism).
type Limiter struct {
	mu      sync.Mutex
	windows map[string]*limitWindow
	limit   int
	window  time.Duration
	maxKeys int
	now     func() time.Time
	dropped int
}

type limitWindow struct {
	count int
	reset time.Time
}

// ErrTooManyKeys reports that the limiter could not track a new key because
// it is at capacity; callers treat it as "already limited".
var ErrTooManyKeys = errors.New("security: rate limiter key capacity reached")

// NewLimiter returns a limiter allowing `limit` events per caller per
// `window`, tracking at most `maxKeys` callers (0 = 4096). A limit <= 0
// disables limiting (Allow always returns true).
func NewLimiter(limit int, window time.Duration, maxKeys int) *Limiter {
	if maxKeys <= 0 {
		maxKeys = 4096
	}
	if window <= 0 {
		window = time.Minute
	}
	return &Limiter{
		windows: map[string]*limitWindow{},
		limit:   limit,
		window:  window,
		maxKeys: maxKeys,
		now:     time.Now,
	}
}

// Allow records one event for key and reports whether it is within budget.
func (l *Limiter) Allow(key string) bool {
	if l == nil || l.limit <= 0 {
		return true
	}
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	w, ok := l.windows[key]
	if !ok {
		if len(l.windows) >= l.maxKeys {
			l.reapLocked(now)
			if len(l.windows) >= l.maxKeys {
				l.dropped++
				return false
			}
		}
		w = &limitWindow{reset: now.Add(l.window)}
		l.windows[key] = w
	}
	if now.After(w.reset) {
		w.count = 0
		w.reset = now.Add(l.window)
	}
	w.count++
	return w.count <= l.limit
}

// RetryAfter reports how long the caller should wait before retrying.
// It returns 0 when the caller is (or may be) within budget.
func (l *Limiter) RetryAfter(key string) time.Duration {
	if l == nil || l.limit <= 0 {
		return 0
	}
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	w, ok := l.windows[key]
	if !ok || w.count <= l.limit {
		return 0
	}
	d := w.reset.Sub(now)
	if d < 0 {
		return 0
	}
	return d
}

// Stats reports the tracked-key count and the number of keys that had to be
// rejected because the tracker was full (observability for tuning).
func (l *Limiter) Stats() (keys int, capacityRejections int) {
	if l == nil {
		return 0, 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.windows), l.dropped
}

// Reap drops expired windows. Called by the service reaper so a chatty
// attacker cannot inflate the key map indefinitely.
func (l *Limiter) Reap() {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.reapLocked(l.now())
}

func (l *Limiter) reapLocked(now time.Time) {
	for k, w := range l.windows {
		if now.After(w.reset) {
			delete(l.windows, k)
		}
	}
}

// ClientIP returns the caller address used as a rate-limit key.
//
// Forwarded headers are only consulted when trustProxy is set, because
// X-Forwarded-For is attacker-controlled on a directly exposed listener:
// trusting it unconditionally would let a single abuser mint unlimited
// identities.
func ClientIP(r *http.Request, trustProxy bool) string {
	if trustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			first := strings.TrimSpace(strings.Split(xff, ",")[0])
			if first != "" {
				return strings.ToLower(first)
			}
		}
		if xreal := strings.TrimSpace(r.Header.Get("X-Real-IP")); xreal != "" {
			return strings.ToLower(xreal)
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return strings.ToLower(r.RemoteAddr)
	}
	return host
}

// ---------------------------------------------------------------------------
// Input validation
// ---------------------------------------------------------------------------

// ValidateNickname enforces the profile naming policy: 1..32 runes, no
// control characters, and a conservative character class. The character
// class exists to keep nicknames render-safe in the client's immediate-mode
// UI and out of logs (newlines would forge log lines), not to be a profanity
// filter.
func ValidateNickname(s string) bool {
	rs := []rune(s)
	if len(rs) < 1 || len(rs) > 32 {
		return false
	}
	for _, r := range rs {
		if r < 0x20 || r == 0x7f {
			return false // control characters: log injection / UI breakage
		}
		if unicodeSpace(r) {
			return false
		}
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '_' || r == '-' || r == '.':
		case r >= 0x0400 && r <= 0x04FF: // Cyrillic (ru/uk dictionaries)
		default:
			return false
		}
	}
	// Reject all-symbol names that render as nothing useful.
	letters := 0
	for _, r := range rs {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= 0x0400 && r <= 0x04FF) {
			letters++
		}
	}
	return letters >= 1
}

func unicodeSpace(r rune) bool {
	return r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == 0x0b || r == 0x0c || r == 0xa0
}
