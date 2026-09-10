package main

// Seat-token rotation tests (M1 session semantics).

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func rotateToken(t *testing.T, srv *httptest.Server, id uint64, tok string) (int, map[string]any) {
	t.Helper()
	resp, err := http.Post(fmt.Sprintf("%s/v1/matches/%d/token/rotate", srv.URL, id),
		"application/json", strings.NewReader(fmt.Sprintf(`{"token":%q}`, tok)))
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	defer resp.Body.Close()
	var out map[string]any
	if resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatal(err)
		}
	}
	return resp.StatusCode, out
}

func wsDialFails(t *testing.T, srv *httptest.Server, id uint64, tok string) bool {
	t.Helper()
	url := "ws" + strings.TrimPrefix(srv.URL, "http") + fmt.Sprintf("/v1/match/ws?match_id=%d&token=%s", id, tok)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _, err := websocket.Dial(ctx, url, nil)
	return err != nil
}

func TestSeatTokenRotation(t *testing.T) {
	api := NewAPI()
	srv := httptest.NewServer(api.Routes())
	defer srv.Close()

	seed := uint64(1512)
	id, tokens, userIDs := createMatch(t, srv, "en", &seed)

	code, out := rotateToken(t, srv, id, tokens[0])
	if code != http.StatusOK {
		t.Fatalf("rotate status = %d, want 200", code)
	}
	newTok, _ := out["token"].(string)
	if newTok == "" || newTok == tokens[0] {
		t.Fatalf("rotated token = %q, want a fresh token", newTok)
	}
	if int(out["seat"].(float64)) != 0 || uint64(out["user_id"].(float64)) != userIDs[0] {
		t.Fatalf("rotate response = %+v, want seat 0 user %d", out, userIDs[0])
	}

	// The old token must no longer authenticate.
	if !wsDialFails(t, srv, id, tokens[0]) {
		t.Fatal("old token must be rejected after rotation")
	}
	// The new token must authenticate.
	c := dial(t, srv, id, newTok)
	defer c.close()
	_ = c.readSnapshot(t)

	// Rotating with an invalid token is rejected.
	code, _ = rotateToken(t, srv, id, "not-a-real-token")
	if code != http.StatusUnauthorized {
		t.Fatalf("invalid-token rotate status = %d, want 401", code)
	}
	// Rotating the (now stale) old token is rejected too.
	code, _ = rotateToken(t, srv, id, tokens[0])
	if code != http.StatusUnauthorized {
		t.Fatalf("stale-token rotate status = %d, want 401", code)
	}
}

func TestSeatTokenRotateUnknownMatch(t *testing.T) {
	api := NewAPI()
	srv := httptest.NewServer(api.Routes())
	defer srv.Close()
	code, _ := rotateToken(t, srv, 999999, "whatever")
	if code != http.StatusNotFound {
		t.Fatalf("missing-match rotate status = %d, want 404", code)
	}
}

func TestSeatTokenRotationDoesNotAffectOtherSeat(t *testing.T) {
	api := NewAPI()
	srv := httptest.NewServer(api.Routes())
	defer srv.Close()

	seed := uint64(1512)
	id, tokens, _ := createMatch(t, srv, "en", &seed)

	if _, out := rotateToken(t, srv, id, tokens[0]); out["token"] == nil {
		t.Fatal("seat 0 rotate failed")
	}
	// Seat 1's token is untouched and still dials.
	c := dial(t, srv, id, tokens[1])
	defer c.close()
	_ = c.readSnapshot(t)
}

func TestSeatTokenTTLExpiry(t *testing.T) {
	api := NewAPI()
	api.tokenTTL = 150 * time.Millisecond
	srv := httptest.NewServer(api.Routes())
	defer srv.Close()

	seed := uint64(1512)
	id, tokens, _ := createMatch(t, srv, "en", &seed)

	// Token valid before expiry.
	if wsDialFails(t, srv, id, tokens[0]) {
		t.Fatal("fresh token must dial")
	}

	// Rotate seat 1 shortly before the original deadline.
	time.Sleep(100 * time.Millisecond)
	_, out := rotateToken(t, srv, id, tokens[1])
	newTok, _ := out["token"].(string)
	if newTok == "" {
		t.Fatal("seat 1 rotate failed")
	}

	// Now past the original deadline (150 ms): the un-rotated token is
	// expired everywhere.
	time.Sleep(120 * time.Millisecond)
	if !wsDialFails(t, srv, id, tokens[0]) {
		t.Fatal("expired token must be rejected")
	}
	code, _ := rotateToken(t, srv, id, tokens[0])
	if code != http.StatusUnauthorized {
		t.Fatalf("expired-token rotate status = %d, want 401", code)
	}

	// The rotated token has a refreshed deadline and still authenticates.
	if wsDialFails(t, srv, id, newTok) {
		t.Fatal("rotated token must have a refreshed deadline")
	}
}
