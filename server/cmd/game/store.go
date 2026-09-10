package main

// Durable storage contracts (M1 vertical slice). The live working set stays
// in memory for the fast path; these repositories mirror and read through to
// a durable backend so state survives restarts.
//
//   - Profiles go through ProfileRepo (see profile.go): in-memory by
//     default, Postgres when WORDARENA_POSTGRES_DSN is set.
//   - Match results are kept in the in-memory cache (a.results, TTL-reaped)
//     and, when configured, mirrored to a ResultRepo; reads that miss the
//     cache fall back to the durable store.

// ResultRepo durably stores finished match results (matchResult includes the
// deterministic event log, so replays survive restarts too).
type ResultRepo interface {
	// Put stores a finished match result. Duplicate ids are no-ops.
	Put(res matchResult) error
	// Get returns a stored result.
	Get(id uint64) (matchResult, bool, error)
}
