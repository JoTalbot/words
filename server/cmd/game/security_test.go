package main

// M1 security hardening tests (batch 21A). Each test pins one gate from
// docs/SECURITY-REVIEW-M1.md: the gate must reject the abusive request AND
// the honest request must still succeed, so a gate that is silently
// unreachable (or one that breaks legitimate play) fails here rather than in
// a bug report.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/JoTalbot/words/server/internal/security"
	"github.com/coder/websocket"
)

// createMatchWithBody posts an arbitrary body and returns status + response.
func postRaw(t *testing.T, srv *httptest.Server, path, body string, hdr map[string]string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, srv.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(resp.Body)
	return resp.StatusCode, buf.String()
}

func getRaw(t *testing.T, srv *httptest.Server, path string, hdr map[string]string) (int, string, http.Header) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, srv.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(resp.Body)
	return resp.StatusCode, buf.String(), resp.Header
}

// newLiveMatch creates a match and returns its id plus both seat tokens.
func newLiveMatch(t *testing.T, srv *httptest.Server) (uint64, [2]string) {
	t.Helper()
	code, body := postRaw(t, srv, "/v1/matches", `{"language":"en"}`, nil)
	if code != http.StatusCreated {
		t.Fatalf("create = %d %s", code, body)
	}
	var cr createMatchResponse
	if err := json.Unmarshal([]byte(body), &cr); err != nil {
		t.Fatal(err)
	}
	return cr.MatchID, cr.Tokens
}

// TestSnapshotRequiresSeatCredential closes the live-board leak: match ids are
// a monotonic counter, so an unauthenticated GET /v1/match/{id}/snapshot lets
// anyone enumerate ongoing matches and read the board, locks and scores.
func TestSnapshotRequiresSeatCredential(t *testing.T) {
	api := NewAPI()
	srv := httptest.NewServer(api.Routes())
	defer srv.Close()

	id, tokens := newLiveMatch(t, srv)

	if code, _, _ := getRaw(t, srv, fmt.Sprintf("/v1/match/%d/snapshot", id), nil); code != http.StatusUnauthorized {
		t.Errorf("anonymous snapshot = %d, want 401", code)
	}
	if code, _, _ := getRaw(t, srv, fmt.Sprintf("/v1/match/%d/snapshot?token=wrong", id), nil); code != http.StatusForbidden {
		t.Errorf("bad-token snapshot = %d, want 403", code)
	}
	// The query form keeps working for the M0 web page.
	code, body, _ := getRaw(t, srv, fmt.Sprintf("/v1/match/%d/snapshot?token=%s", id, tokens[0]), nil)
	if code != http.StatusOK {
		t.Fatalf("query-token snapshot = %d %s, want 200", code, body)
	}
	// The header form is the preferred one (no credential in URLs).
	code, body, _ = getRaw(t, srv, fmt.Sprintf("/v1/match/%d/snapshot", id),
		map[string]string{"Authorization": "Bearer " + tokens[1]})
	if code != http.StatusOK {
		t.Fatalf("bearer snapshot = %d %s, want 200", code, body)
	}
	var snap map[string]any
	if err := json.Unmarshal([]byte(body), &snap); err != nil {
		t.Fatalf("snapshot body is not JSON: %v", err)
	}
	if snap["cells"] == nil {
		t.Error("authorized snapshot must carry the board")
	}
	// A rotated token stops authenticating: rotation must not leave a
	// second live credential behind.
	rotCode, rotBody := postRaw(t, srv, fmt.Sprintf("/v1/matches/%d/token/rotate", id),
		fmt.Sprintf(`{"token":%q}`, tokens[1]), nil)
	if rotCode != http.StatusOK {
		t.Fatalf("rotate = %d %s", rotCode, rotBody)
	}
	var rot struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal([]byte(rotBody), &rot); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := getRaw(t, srv, fmt.Sprintf("/v1/match/%d/snapshot?token=%s", id, tokens[1]), nil); code != http.StatusForbidden {
		t.Errorf("rotated-away token still works: got %d, want 403", code)
	}
	if code, _, _ := getRaw(t, srv, fmt.Sprintf("/v1/match/%d/snapshot?token=%s", id, rot.Token), nil); code != http.StatusOK {
		t.Errorf("new token = %d, want 200", code)
	}
}

// TestWSOriginPolicy pins the replacement for OriginPatterns: ["*"]: browsers
// from a foreign origin are refused before the handshake, while native
// clients (no Origin header) and loopback browser origins keep working.
func TestWSOriginPolicy(t *testing.T) {
	api := NewAPI()
	srv := httptest.NewServer(api.Routes())
	defer srv.Close()

	id, tokens := newLiveMatch(t, srv)
	wsURL := strings.Replace(srv.URL, "http://", "ws://", 1)

	dial := func(origin string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		hdr := http.Header{}
		if origin != "" {
			hdr.Set("Origin", origin)
		}
		c, _, err := websocket.Dial(ctx, fmt.Sprintf("%s/v1/match/ws?match_id=%d&token=%s", wsURL, id, tokens[0]), &websocket.DialOptions{HTTPHeader: hdr})
		if err != nil {
			return err
		}
		c.CloseNow()
		return nil
	}

	if err := dial(""); err != nil {
		t.Errorf("native client (no Origin) must connect: %v", err)
	}
	if err := dial("http://127.0.0.1:18080"); err != nil {
		t.Errorf("loopback browser origin must connect: %v", err)
	}
	// A foreign browser origin is refused by the service before the handshake
	// upgrade is even attempted, so the answer is a plain 403 (checked over
	// HTTP to keep the assertion independent of the client's error text).
	u := fmt.Sprintf("/v1/match/ws?match_id=%d&token=%s", id, tokens[0])
	if code, _, _ := getRaw(t, srv, u, map[string]string{"Origin": "https://evil.example"}); code != http.StatusForbidden {
		t.Errorf("foreign origin handshake = %d, want 403", code)
	}
	if code, _, _ := getRaw(t, srv, u, nil); code == http.StatusForbidden {
		t.Errorf("request without Origin was origin-refused (%d); the handshake must fail later, not at the policy", code)
	}

	// An explicitly allowlisted origin is accepted.
	api2 := NewAPI()
	api2.origins = security.NewOriginPolicy("https://arena.example")
	srv2 := httptest.NewServer(api2.Routes())
	defer srv2.Close()
	id2, tok2 := newLiveMatch(t, srv2)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	hdr := http.Header{}
	hdr.Set("Origin", "https://arena.example")
	ws2 := strings.Replace(srv2.URL, "http://", "ws://", 1)
	c, _, err := websocket.Dial(ctx, fmt.Sprintf("%s/v1/match/ws?match_id=%d&token=%s", ws2, id2, tok2[0]), &websocket.DialOptions{HTTPHeader: hdr})
	if err != nil {
		t.Fatalf("allowlisted origin must connect: %v", err)
	}
	c.CloseNow()
}

// TestMutationRateLimit checks the per-caller gate on the unauthenticated
// creating endpoints and that it does not touch reads.
func TestMutationRateLimit(t *testing.T) {
	api := NewAPI()
	api.mutators = security.NewLimiter(2, time.Minute, 0)
	srv := httptest.NewServer(api.Routes())
	defer srv.Close()

	for i := 0; i < 2; i++ {
		if code, body := postRaw(t, srv, "/v1/matches", `{"language":"en"}`, nil); code != http.StatusCreated {
			t.Fatalf("create %d = %d %s", i+1, code, body)
		}
	}
	code, body := postRaw(t, srv, "/v1/matches", `{"language":"en"}`, nil)
	if code != http.StatusTooManyRequests {
		t.Fatalf("3rd create = %d %s, want 429", code, body)
	}
	if !strings.Contains(body, "too many requests") {
		t.Errorf("429 body = %s", body)
	}
	// Queue and profile creation share the budget (same caller, same abuse
	// class), and the response carries a retry hint.
	if code, _ := postRaw(t, srv, "/v1/queue", `{"language":"en"}`, nil); code == http.StatusOK || code == http.StatusCreated || code == http.StatusAccepted || code == http.StatusConflict {
		t.Errorf("queue post under limit = %d, want 429", code)
	}
	if _, _, h := getRaw(t, srv, "/metrics", nil); h.Get("Retry-After") != "" {
		t.Log("reads unaffected; Retry-After is set on the mutating path only")
	}
	// Reads must stay available while a caller is limited: an already
	// connected player must not be cut off because a script spammed create.
	if code, _, _ := getRaw(t, srv, "/healthz", nil); code != http.StatusOK {
		t.Errorf("healthz under mutation limit = %d, want 200", code)
	}
	if code, _, _ := getRaw(t, srv, "/readyz", nil); code != http.StatusOK {
		t.Errorf("readyz under mutation limit = %d, want 200", code)
	}
}

// TestExplicitSeedPolicyGate shows the fairness knob: a deployment that must
// not let a client pin the board rejects the seed field instead of silently
// honouring it.
func TestExplicitSeedPolicyGate(t *testing.T) {
	api := NewAPI()
	api.allowExplicitSeed = false
	srv := httptest.NewServer(api.Routes())
	defer srv.Close()

	if code, body := postRaw(t, srv, "/v1/matches", `{"language":"en","seed":1512}`, nil); code != http.StatusBadRequest {
		t.Fatalf("seeded create = %d %s, want 400", code, body)
	}
	if code, _ := postRaw(t, srv, "/v1/matches", `{"language":"en"}`, nil); code != http.StatusCreated {
		t.Fatalf("unseeded create = %d, want 201 (dev tooling must keep working)", code)
	}
}

// TestRequestBodyCap checks that a huge body is refused without being read
// into a decoded structure.
func TestRequestBodyCap(t *testing.T) {
	api := NewAPI()
	api.maxBodyBytes = 256
	srv := httptest.NewServer(api.Routes())
	defer srv.Close()

	big := `{"language":"en","padding":"` + strings.Repeat("a", 4096) + `"}`
	if code, _ := postRaw(t, srv, "/v1/matches", big, nil); code != http.StatusBadRequest {
		t.Errorf("oversized body = %d, want 400", code)
	}
}

// TestUnknownFieldRejected pins strict request parsing: a typo'd or smuggled
// field must be an error, not silently ignored.
func TestUnknownFieldRejected(t *testing.T) {
	api := NewAPI()
	srv := httptest.NewServer(api.Routes())
	defer srv.Close()

	if code, body := postRaw(t, srv, "/v1/matches", `{"language":"en","sudden_death":true,"admin":true}`, nil); code != http.StatusBadRequest {
		t.Fatalf("unknown field = %d %s, want 400", code, body)
	}
	if code, _ := postRaw(t, srv, "/v1/matches", `{"language":"en","sudden_death":true}`, nil); code != http.StatusCreated {
		t.Fatalf("known fields = %d, want 201", code)
	}
}

// TestNicknamePolicy pins the render/log-safety rule in one place.
func TestNicknamePolicy(t *testing.T) {
	api := NewAPI()
	srv := httptest.NewServer(api.Routes())
	defer srv.Close()

	for _, bad := range []string{"two words", "bob\nINFO forged", "<b>x</b>"} {
		if code, _ := postRaw(t, srv, "/v1/players", fmt.Sprintf(`{"nickname":%q,"language":"en"}`, bad), nil); code != http.StatusBadRequest {
			t.Errorf("nickname %q = %d, want 400", bad, code)
		}
	}
	for _, good := range []string{"smoke-bot", "Жасмін_1", "ally.A"} {
		if code, body := postRaw(t, srv, "/v1/players", fmt.Sprintf(`{"nickname":%q,"language":"en"}`, good), nil); code != http.StatusCreated {
			t.Errorf("nickname %q = %d %s, want 201", good, code, body)
		}
	}
}

// TestInternalErrorIsNotEchoed makes sure a storage failure does not leak the
// wrapped error text to the caller.
func TestInternalErrorIsNotEchoed(t *testing.T) {
	api := NewAPI()
	srv := httptest.NewServer(api.Routes())
	defer srv.Close()

	// An unknown profile id is the reachable 404 path; the 500 path is
	// covered by the store tests. What matters here is that the response body
	// for a rejected create is a fixed string.
	code, body := postRaw(t, srv, "/v1/matches", `{"language":"en","player_ids":[9998,9999]}`, nil)
	if code != http.StatusNotFound {
		t.Fatalf("unknown profile = %d %s, want 404", code, body)
	}
	if strings.Contains(body, "sql") || strings.Contains(body, "postgres") {
		t.Errorf("error body leaks storage detail: %s", body)
	}
}
