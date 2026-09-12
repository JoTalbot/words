package main

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// players.id is BIGSERIAL, i.e. a signed 64-bit identity. The wire type for
// player_ids is uint64, so a client can name an id that the profile store can
// never hold. Reaching the store with such a value is a bug twice over: the
// Postgres backend fails inside the pgx encoder and the handler answers 500
// "profile store error" for what is a malformed request, and the same class of
// uint64-vs-BIGINT mismatch is exactly what silently dropped half of all
// durable match results before migration 002 (see store_u64_test.go).
//
// The id must therefore be range-checked at the HTTP boundary, before any
// store call, and rejected with 400.
func TestCreateMatchRejectsPlayerIDAboveInt64(t *testing.T) {
	api := NewAPI()
	srv := httptest.NewServer(api.Routes())
	defer srv.Close()

	// Two real profiles so the request is otherwise well formed and the only
	// defect under test is the out-of-range id.
	p0, _ := createPlayer(t, srv, "range-lo", "en")
	p1, _ := createPlayer(t, srv, "range-hi", "en")

	cases := []struct {
		name string
		body string
	}{
		{"seat 0 above MaxInt64", `{"language":"en","player_ids":[9223372036854775808,` + strconv.FormatUint(p1.ID, 10) + `]}`},
		{"seat 1 above MaxInt64", `{"language":"en","player_ids":[` + strconv.FormatUint(p0.ID, 10) + `,9223372036854775808]}`},
		{"both seats above MaxInt64", `{"language":"en","player_ids":[9223372036854775808,18446744073709551615]}`},
		{"max uint64", `{"language":"en","player_ids":[18446744073709551615,` + strconv.FormatUint(p1.ID, 10) + `]}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, body := postRaw(t, srv, "/v1/matches", tc.body, nil)
			if code != http.StatusBadRequest {
				t.Fatalf("out-of-range player_ids = %d %s, want 400", code, body)
			}
			// The boundary must answer before touching the store, so the
			// response cannot be the storage-failure string.
			if strings.Contains(body, "profile store error") {
				t.Errorf("out-of-range id reached the store: %s", body)
			}
			if strings.Contains(body, "sql") || strings.Contains(body, "postgres") {
				t.Errorf("error body leaks storage detail: %s", body)
			}
		})
	}
}

// MaxInt64 itself is representable as BIGINT, so it must not be rejected by the
// range check. It is an unknown profile, so the reachable answer is 404.
func TestCreateMatchAcceptsPlayerIDAtInt64Boundary(t *testing.T) {
	api := NewAPI()
	srv := httptest.NewServer(api.Routes())
	defer srv.Close()

	body := `{"language":"en","player_ids":[9223372036854775807,9223372036854775806]}`
	code, resp := postRaw(t, srv, "/v1/matches", body, nil)
	if code != http.StatusNotFound {
		t.Fatalf("in-range unknown profile = %d %s, want 404", code, resp)
	}
	if strings.Contains(resp, "profile store error") {
		t.Errorf("in-range id reached the encoder path: %s", resp)
	}
}

// The same wire-type mismatch existed on the queue entry point: player_id is a
// uint64 straight from JSON and was passed to the profile store unchecked.
func TestQueueCreateRejectsPlayerIDAboveInt64(t *testing.T) {
	api := NewAPI()
	srv := httptest.NewServer(api.Routes())
	defer srv.Close()

	for _, body := range []string{
		`{"language":"en","player_id":9223372036854775808}`,
		`{"language":"en","player_id":18446744073709551615}`,
	} {
		code, resp := postRaw(t, srv, "/v1/queue", body, nil)
		if code != http.StatusBadRequest {
			t.Fatalf("queue player_id %s = %d %s, want 400", body, code, resp)
		}
		if strings.Contains(resp, "profile store error") {
			t.Errorf("out-of-range queue id reached the store: %s", resp)
		}
	}
}

// GET /v1/players/{id} parsed the path value by hand. A digit string too long
// for uint64 wrapped around instead of failing, so an id that cannot exist
// could silently alias onto one that does.
func TestPlayerGetRejectsOverflowingID(t *testing.T) {
	api := NewAPI()
	srv := httptest.NewServer(api.Routes())
	defer srv.Close()

	cases := []struct {
		name string
		id   string
		want int
	}{
		// Wraps to 17634066569014878719 before the guard, not a real profile.
		{"21 nines wraps uint64", "999999999999999999999", http.StatusBadRequest},
		{"max uint64 as decimal", "18446744073709551615", http.StatusBadRequest},
		{"one past max uint64", "18446744073709551616", http.StatusBadRequest},
		{"above MaxInt64", "9223372036854775808", http.StatusBadRequest},
		// 2^64+1: the unguarded parser wrapped this to 1, so the handler would
		// have served profile 1 to a caller that never held it.
		{"2^64+1 wrapped onto profile 1", "18446744073709551617", http.StatusBadRequest},
		{"MaxInt64 is in range but unknown", "9223372036854775807", http.StatusNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := http.Get(srv.URL + "/v1/players/" + tc.id)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != tc.want {
				t.Fatalf("GET /v1/players/%s = %d, want %d", tc.id, resp.StatusCode, tc.want)
			}
		})
	}
}

// parseID is the shared path/query id parser; pin the overflow guard directly
// so a future refactor cannot reintroduce the silent wrap.
func TestParseIDRejectsOverflow(t *testing.T) {
	cases := []struct {
		in   string
		want uint64
		err  bool
	}{
		{"0", 0, false},
		{"1", 1, false},
		{"18446744073709551615", 18446744073709551615, false},
		{"18446744073709551616", 0, true},
		{"99999999999999999999", 0, true},
		{"184467440737095516150", 0, true},
		// 2^64+5 overflows uint64, so parseID itself must refuse it. Measured
		// on the unguarded parser it returned 5 with no error: a client could
		// read a profile whose id it never held.
		{"18446744073709551621", 0, true},
		{"", 0, true},
		{"12a4", 0, true},
		{"-1", 0, true},
	}
	for _, tc := range cases {
		got, err := parseID(tc.in)
		if tc.err {
			if err == nil {
				t.Errorf("parseID(%q) = %d, want an error", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseID(%q) unexpected error: %v", tc.in, err)
		} else if got != tc.want {
			t.Errorf("parseID(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}
