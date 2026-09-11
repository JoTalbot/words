package main

// Regression tests for durable match-id aliasing (batch 22A).
//
// The failure: match ids come from a per-process counter that starts at 1,
// while match_results is keyed by match_id and survives restarts. After a
// restart the service re-issued ids that already had durable rows, so
//   - recordResult's INSERT ... ON CONFLICT DO NOTHING silently dropped the
//     real outcome of every aliased match, and
//   - GET /v1/matches/{id}/result and /replay returned the *previous* match's
//     data for a brand-new match (observed live on the OCI deployment, where
//     infra/smoke.sh's "replay is 404 while the match is active" answered 200).
//
// The fix is to resume the counter from the durable high-water mark. Both
// directions are pinned below: the bug reproduces without priming, and the
// contract holds with it.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// aliasingResultRepo is a tiny durable store seeded with finished matches,
// mimicking a Postgres table that outlived the process that wrote it.
type aliasingResultRepo struct {
	mu      sync.Mutex
	byID    map[uint64]matchResult
	maxID   uint64
	storedN int
}

func newSeededResultRepo(ids ...uint64) *aliasingResultRepo {
	r := &aliasingResultRepo{byID: map[uint64]matchResult{}}
	for _, id := range ids {
		r.byID[id] = matchResult{MatchID: id, Seed: 999, Language: "en", Over: true,
			Scores: [2]int64{1, 2}, StateVer: 7, ServerTick: 30,
			Events: []replayEvent{{Seq: 1, Word: "alias"}}}
		if id > r.maxID {
			r.maxID = id
		}
	}
	return r
}

// Put mirrors the durable store's ON CONFLICT DO NOTHING: the second writer
// loses, which is precisely why ids must never repeat.
func (r *aliasingResultRepo) Put(res matchResult) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.byID[res.MatchID]; exists {
		return nil
	}
	r.byID[res.MatchID] = res
	r.storedN++
	if res.MatchID > r.maxID {
		r.maxID = res.MatchID
	}
	return nil
}

func (r *aliasingResultRepo) Get(id uint64) (matchResult, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	res, ok := r.byID[id]
	return res, ok, nil
}

// GetByCode mirrors the real store's code lookup so the aliasing tests exercise
// the same interface the service uses.
func (r *aliasingResultRepo) GetByCode(code string) (matchResult, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, res := range r.byID {
		if res.Code != "" && res.Code == code {
			return res, true, nil
		}
	}
	return matchResult{}, false, nil
}

func (r *aliasingResultRepo) MaxMatchID() (uint64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.maxID, nil
}

func (r *aliasingResultRepo) stored() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.storedN
}

func createMatchID(t *testing.T, srv *httptest.Server) uint64 {
	t.Helper()
	resp, err := http.Post(srv.URL+"/v1/matches", "application/json",
		strings.NewReader(`{"language":"en"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d", resp.StatusCode)
	}
	var cr createMatchResponse
	if err := json.NewDecoder(resp.Body).Decode(&cr); err != nil {
		t.Fatal(err)
	}
	return cr.MatchID
}

// replayStatus reads the durable-backed replay endpoint for a live match. A
// correct service answers 404: the match has no finished result of its own.
func replayStatus(t *testing.T, srv *httptest.Server, id uint64) int {
	t.Helper()
	resp, err := http.Get(fmt.Sprintf("%s/v1/matches/%d/replay", srv.URL, id))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

// TestAliasingReproducesWithoutPring documents the old behaviour so the fix
// cannot be "verified" by a test that would also pass without it.
func TestAliasingReproducesWithoutPriming(t *testing.T) {
	repo := newSeededResultRepo(1, 2, 3)
	api := newAPI(newMemProfileStore(), repo, nil)
	defer api.Stop()
	// Deliberately NOT primed: this is the pre-fix state.
	srv := httptest.NewServer(api.Routes())
	defer srv.Close()

	id := createMatchID(t, srv)
	if id != 1 {
		t.Fatalf("unprimed first id = %d, want 1 (the aliasing condition)", id)
	}
	// The bug: a match that has not finished reports the previous match's log.
	if got := replayStatus(t, srv, id); got != http.StatusOK {
		t.Fatalf("unprimed replay = %d, want the buggy 200 to reproduce", got)
	}
}

// TestMatchIDsDoNotAliasAcrossRestarts is the regression itself.
func TestMatchIDsDoNotAliasAcrossRestarts(t *testing.T) {
	repo := newSeededResultRepo(1, 2, 3)
	api := newAPI(newMemProfileStore(), repo, nil)
	defer api.Stop()
	if err := api.primeMatchIDs(); err != nil {
		t.Fatalf("prime: %v", err)
	}
	srv := httptest.NewServer(api.Routes())
	defer srv.Close()

	id := createMatchID(t, srv)
	if id != 4 {
		t.Errorf("primed first id = %d, want 4 (continues the durable sequence)", id)
	}
	if got := replayStatus(t, srv, id); got != http.StatusNotFound {
		t.Errorf("replay of a live match = %d, want 404 (no inherited durable row)", got)
	}
	// And the new match's own result must actually be stored, not dropped by a
	// conflict on an aliased id.
	res := matchResult{MatchID: id, Seed: 1234, Language: "en", Over: true,
		Scores: [2]int64{10, 9}, StateVer: 5, ServerTick: 10}
	if err := repo.Put(res); err != nil {
		t.Fatal(err)
	}
	if repo.stored() != 1 {
		t.Errorf("durable writes = %d, want 1 (the fresh match must not collide)", repo.stored())
	}
	got, ok := api.lookupResult(id)
	if !ok || got.Scores != [2]int64{10, 9} {
		t.Errorf("lookup after store = %+v ok=%v", got, ok)
	}
}

// TestPrimeIsHarmlessOnAnEmptyStore keeps the first-ever boot working.
func TestPrimeIsHarmlessOnAnEmptyStore(t *testing.T) {
	repo := newSeededResultRepo()
	api := newAPI(newMemProfileStore(), repo, nil)
	defer api.Stop()
	if err := api.primeMatchIDs(); err != nil {
		t.Fatalf("prime on empty store: %v", err)
	}
	srv := httptest.NewServer(api.Routes())
	defer srv.Close()
	if id := createMatchID(t, srv); id != 1 {
		t.Errorf("first id on an empty durable store = %d, want 1", id)
	}
	// No repo at all (in-memory deployments) must not error.
	api2 := NewAPI()
	defer api2.Stop()
	if err := api2.primeMatchIDs(); err != nil {
		t.Errorf("prime without a durable store: %v", err)
	}
}
