package security

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestNormalizeOrigin(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"http://127.0.0.1:18080", "http://127.0.0.1:18080", true},
		{"HTTP://Example.COM", "http://example.com", true},
		{"https://example.com/", "https://example.com", true},
		{"example.com", "https://example.com", true},
		{"https://example.com/path", "", false},
		{"ftp://example.com", "", false},
		{"", "", false},
		{"   ", "", false},
	}
	for _, c := range cases {
		got, ok := NormalizeOrigin(c.in)
		if ok != c.ok || got != c.want {
			t.Errorf("NormalizeOrigin(%q) = %q,%v want %q,%v", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestOriginPolicyAllow(t *testing.T) {
	req := func(origin string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/v1/match/ws", nil)
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		return r
	}

	// Default policy (no allowlist): native clients pass, loopback browsers
	// pass, foreign browsers do not.
	p := NewOriginPolicy("")
	if !p.Allow(req("")) {
		t.Error("request without Origin must be allowed (native client)")
	}
	if !p.Allow(req("http://127.0.0.1:18080")) {
		t.Error("loopback origin must be allowed by default")
	}
	if !p.Allow(req("http://localhost:3000")) {
		t.Error("localhost origin must be allowed by default")
	}
	if p.Allow(req("https://evil.example")) {
		t.Error("foreign origin must be rejected by default")
	}

	// Explicit allowlist adds the named origin and keeps the rest denied.
	p2 := NewOriginPolicy("https://arena.example, https://game.example")
	if !p2.Allow(req("https://arena.example")) {
		t.Error("allowlisted origin must be accepted")
	}
	if p2.Allow(req("http://arena.example")) {
		t.Error("scheme mismatch must not be accepted")
	}
	if p2.Allow(req("https://evil.example")) {
		t.Error("non-allowlisted origin must be rejected")
	}

	// "private" is the keyword that re-enables loopback acceptance alongside
	// a named origin.
	p3 := NewOriginPolicy("https://arena.example,private")
	if !p3.Allow(req("http://127.0.0.1:8080")) {
		t.Error("loopback must be accepted when private is listed")
	}

	// A junk Origin header is a rejection, not a parse-time panic.
	if p.Allow(req("not a url")) {
		t.Error("malformed origin must be rejected")
	}
}

func TestLimiterFixedWindow(t *testing.T) {
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	l := NewLimiter(3, time.Minute, 0)
	l.now = func() time.Time { return now }

	for i := 0; i < 3; i++ {
		if !l.Allow("1.2.3.4") {
			t.Fatalf("event %d must be allowed", i+1)
		}
	}
	if l.Allow("1.2.3.4") {
		t.Fatal("4th event in the window must be rejected")
	}
	if d := l.RetryAfter("1.2.3.4"); d <= 0 || d > time.Minute {
		t.Errorf("RetryAfter = %v, want (0, 1m]", d)
	}
	// A different caller is independent.
	if !l.Allow("5.6.7.8") {
		t.Error("other caller must have its own budget")
	}
	if l.RetryAfter("5.6.7.8") != 0 {
		t.Error("caller within budget must report no retry delay")
	}
	// Next window resets the budget.
	now = now.Add(61 * time.Second)
	if !l.Allow("1.2.3.4") {
		t.Error("budget must reset in a new window")
	}
}

func TestLimiterDisabled(t *testing.T) {
	l := NewLimiter(0, time.Minute, 0)
	for i := 0; i < 10000; i++ {
		if !l.Allow("x") {
			t.Fatalf("limit 0 must disable limiting (failed at %d)", i)
		}
	}
}

func TestLimiterKeyCapacityIsBounded(t *testing.T) {
	l := NewLimiter(1, time.Minute, 4)
	for i := 0; i < 4; i++ {
		l.Allow(string(rune('a' + i)))
	}
	keys, _ := l.Stats()
	if keys != 4 {
		t.Fatalf("tracked keys = %d, want 4", keys)
	}
	// Every key is already at its limit, so a new caller must be refused
	// rather than growing the map without bound.
	if l.Allow("zzz") {
		t.Fatal("new caller past capacity must be refused")
	}
	if _, dropsAfter := l.Stats(); dropsAfter == 0 {
		t.Error("capacity rejection must be counted")
	}
	// After the window expires, reaping frees room for new callers.
	l.now = func() time.Time { return time.Now().Add(2 * time.Minute) }
	l.Reap()
	keys, _ = l.Stats()
	if keys != 0 {
		t.Fatalf("tracked keys after reap = %d, want 0", keys)
	}
}

func TestClientIP(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "203.0.113.9:5555"
	r.Header.Set("X-Forwarded-For", "10.0.0.1, 10.0.0.2")
	if got := ClientIP(r, false); got != "203.0.113.9" {
		t.Errorf("untrusted proxy: got %q, want the socket peer", got)
	}
	if got := ClientIP(r, true); got != "10.0.0.1" {
		t.Errorf("trusted proxy: got %q, want the first forwarded entry", got)
	}
	// No port in RemoteAddr must not become a key collision hazard.
	r2 := httptest.NewRequest(http.MethodGet, "/", nil)
	r2.RemoteAddr = "198.51.100.7"
	if got := ClientIP(r2, false); got != "198.51.100.7" {
		t.Errorf("portless RemoteAddr: got %q", got)
	}
}

func TestValidateNickname(t *testing.T) {
	ok := []string{"alice", "smoke-bot", "O.B_2", "Жасмин", "Укр_Мова", "x"}
	for _, n := range ok {
		if !ValidateNickname(n) {
			t.Errorf("ValidateNickname(%q) = false, want true", n)
		}
	}
	bad := []string{
		"",                          // empty
		"                         ", // whitespace only
		"player one",                // space
		"bob\nINFO forged log",      // control character / log injection
		"<script>alert(1)</script>", // markup, not render-safe
		"nické",                     // outside the allowed classes
		string(make([]rune, 33)),    // over length
		"___",                       // no letters
	}
	for _, n := range bad {
		if ValidateNickname(n) {
			t.Errorf("ValidateNickname(%q) = true, want false", n)
		}
	}
}
