package main

// Authorization tests for the result and replay endpoints (batch 21g, closing
// docs/SECURITY-REVIEW-M1.md finding S-2).
//
// Before this change both endpoints were unauthenticated and keyed by a
// monotonic counter, so a one-line loop read the final score, the winner and the
// full word-by-word event log of every finished match on the host. The property
// these tests pin is narrow and specific: once WORDARENA_REQUIRE_READ_CAPABILITY
// is set, walking sequential ids must stop working at all, and reading by code
// must require the per-match capability.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// seedFinished installs a finished result and its handle pair directly, which is
// what recordResult does when a match ends. Driving a whole match over two
// WebSockets would test the match, not the authorization on the read.
func seedFinished(t *testing.T, api *API, id uint64, code, readCap string) matchResult {
	t.Helper()
	res := matchResult{
		MatchID: id, Seed: 1512, Language: "en", Over: true,
		WinnerSeat: 0, IsTie: false, Scores: [2]int64{43, 45},
		StateVer: 12, ServerTick: 91, Code: code, ReadCap: readCap,
	}
	api.resultsMu.Lock()
	api.results[id] = res
	if code != "" {
		api.matchCaps[id] = matchAccess{Code: code, ReadCap: readCap}
		api.resultCodes[code] = id
	}
	api.resultsMu.Unlock()
	return res
}

func getResult(t *testing.T, srv *httptest.Server, ref, headerCap, queryCap string) (*http.Response, string) {
	t.Helper()
	url := fmt.Sprintf("%s/v1/matches/%s/result", srv.URL, ref)
	if queryCap != "" {
		url += "?cap=" + queryCap
	}
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	if headerCap != "" {
		req.Header.Set("Authorization", "Bearer "+headerCap)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	return resp, string(body)
}

func TestIsNumericRef(t *testing.T) {
	for ref, want := range map[string]bool{
		"9161":                             true,
		"0":                                true,
		"":                                 false,
		"9161a":                            false,
		"a1b2c3d4e5f60718293a4b5c6d7e8f90": false, // a real 128-bit hex code
		"18446744073709551615":             true,
		" 9161":                            false,
	} {
		if got := isNumericRef(ref); got != want {
			t.Errorf("isNumericRef(%q) = %v, want %v", ref, got, want)
		}
	}
}

// TestResultEndpointTransitionWindowIsBackwardCompatible covers the state the
// deployment is in today: the flag is off, so existing clients that only know
// the numeric id keep working, and the code form already works too.
func TestResultEndpointTransitionWindowIsBackwardCompatible(t *testing.T) {
	api := NewAPI()
	api.requireReadCap = false
	srv := httptest.NewServer(api.Routes())
	defer srv.Close()

	seedFinished(t, api, 9161, "c0ffee00c0ffee00c0ffee00c0ffee00", "deadbeefdeadbeefdeadbeefdeadbeef")

	if resp, _ := getResult(t, srv, "9161", "", ""); resp.StatusCode != http.StatusOK {
		t.Errorf("legacy numeric id during the transition = %d, want 200", resp.StatusCode)
	}
	if resp, _ := getResult(t, srv, "c0ffee00c0ffee00c0ffee00c0ffee00", "", ""); resp.StatusCode != http.StatusOK {
		t.Errorf("match code without the flag = %d, want 200", resp.StatusCode)
	}
}

// TestResultEndpointHardenedBlocksSequentialIDs is the actual S-2 guarantee. The
// match exists and its result is readable by code, but the sequential id form
// must not resolve - and it must answer 404, not 400 or 401, because a distinct
// status would confirm the id is well formed and let a caller measure the
// counter.
func TestResultEndpointHardenedBlocksSequentialIDs(t *testing.T) {
	api := NewAPI()
	api.requireReadCap = true
	srv := httptest.NewServer(api.Routes())
	defer srv.Close()

	seedFinished(t, api, 9161, "c0ffee00c0ffee00c0ffee00c0ffee00", "deadbeefdeadbeefdeadbeefdeadbeef")

	resp, _ := getResult(t, srv, "9161", "deadbeefdeadbeefdeadbeefdeadbeef", "")
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("sequential id while hardened = %d, want 404 (enumeration must not resolve even with a valid capability)", resp.StatusCode)
	}
	// An unknown id must be indistinguishable from a known one.
	if resp, _ := getResult(t, srv, "9162", "deadbeefdeadbeefdeadbeefdeadbeef", ""); resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown sequential id = %d, want the same 404", resp.StatusCode)
	}
}

func TestResultEndpointHardenedRequiresTheCapability(t *testing.T) {
	api := NewAPI()
	api.requireReadCap = true
	srv := httptest.NewServer(api.Routes())
	defer srv.Close()

	const code = "c0ffee00c0ffee00c0ffee00c0ffee00"
	const cap = "deadbeefdeadbeefdeadbeefdeadbeef"
	seedFinished(t, api, 9161, code, cap)

	if resp, _ := getResult(t, srv, code, "", ""); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("code with no capability = %d, want 401", resp.StatusCode)
	}
	if resp, _ := getResult(t, srv, code, "deadbeefdeadbeefdeadbeefdeadbee0", ""); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("code with a wrong capability = %d, want 401", resp.StatusCode)
	}
	if resp, _ := getResult(t, srv, code, cap, ""); resp.StatusCode != http.StatusOK {
		t.Errorf("code with the capability in the header = %d, want 200", resp.StatusCode)
	}
	// The query form exists for tooling that cannot set headers; it is the
	// documented fallback, so it has to work.
	if resp, _ := getResult(t, srv, code, "", cap); resp.StatusCode != http.StatusOK {
		t.Errorf("code with ?cap= = %d, want 200", resp.StatusCode)
	}
	if resp, _ := getResult(t, srv, "ffffffffffffffffffffffffffffffff", cap, ""); resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown code = %d, want 404", resp.StatusCode)
	}
}

// TestResultBodyNeverLeaksTheReadCapability guards the reason ReadCap is tagged
// json:"-". If the capability were echoed in the result body, anyone who could
// read a result once would hold the credential permanently, and the gate would
// be worth nothing.
func TestResultBodyNeverLeaksTheReadCapability(t *testing.T) {
	api := NewAPI()
	api.requireReadCap = false
	srv := httptest.NewServer(api.Routes())
	defer srv.Close()

	const cap = "deadbeefdeadbeefdeadbeefdeadbeef"
	seedFinished(t, api, 9161, "c0ffee00c0ffee00c0ffee00c0ffee00", cap)

	resp, _ := getResult(t, srv, "9161", "", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	req, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("%s/v1/matches/9161/result", srv.URL), nil)
	r2, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer r2.Body.Close()
	var raw map[string]any
	if err := json.NewDecoder(r2.Body).Decode(&raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	body, _ := json.Marshal(raw)
	if strings.Contains(string(body), cap) {
		t.Error("the read capability appears in the result body")
	}
	if _, present := raw["read_capability"]; present {
		t.Error("the result body has a read_capability field")
	}
	// The code itself is fine to echo: the caller already has it, and it is what
	// identifies the match in support conversations.
	if raw["match_code"] != "c0ffee00c0ffee00c0ffee00c0ffee00" {
		t.Errorf("match_code = %v, want the issued code", raw["match_code"])
	}
}

// TestReplayEndpointHasTheSameGate makes sure the fix was applied to both
// endpoints. The replay body is the more sensitive of the two - it is the full
// word-by-word log - so it would be an easy one to miss.
func TestReplayEndpointHasTheSameGate(t *testing.T) {
	api := NewAPI()
	api.requireReadCap = true
	srv := httptest.NewServer(api.Routes())
	defer srv.Close()

	const code = "c0ffee00c0ffee00c0ffee00c0ffee00"
	const cap = "deadbeefdeadbeefdeadbeefdeadbeef"
	seedFinished(t, api, 9161, code, cap)

	replay := func(ref, headerCap string) int {
		req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("%s/v1/matches/%s/replay", srv.URL, ref), nil)
		if err != nil {
			t.Fatal(err)
		}
		if headerCap != "" {
			req.Header.Set("Authorization", "Bearer "+headerCap)
		}
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}

	if got := replay("9161", cap); got != http.StatusNotFound {
		t.Errorf("replay by sequential id while hardened = %d, want 404", got)
	}
	if got := replay(code, ""); got != http.StatusUnauthorized {
		t.Errorf("replay by code without capability = %d, want 401", got)
	}
	if got := replay(code, cap); got != http.StatusOK {
		t.Errorf("replay by code with capability = %d, want 200", got)
	}
}

// TestResultWithoutACapabilityIsRefusedWhenHardened covers rows written before
// migration 003. They have no capability stored, and inventing one on read would
// be worse than refusing: the guarantee is that reading requires a credential
// issued at creation.
func TestResultWithoutACapabilityIsRefusedWhenHardened(t *testing.T) {
	api := NewAPI()
	api.requireReadCap = true
	srv := httptest.NewServer(api.Routes())
	defer srv.Close()

	api.resultsMu.Lock()
	api.results[9161] = matchResult{MatchID: 9161, Seed: 1512, Language: "en", Over: true, Scores: [2]int64{43, 45}}
	api.resultsMu.Unlock()

	if resp, _ := getResult(t, srv, "9161", "", ""); resp.StatusCode != http.StatusNotFound {
		t.Errorf("legacy row by id while hardened = %d, want 404", resp.StatusCode)
	}
}
