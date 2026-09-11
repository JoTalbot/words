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
	// GetByCode returns a stored result by its unguessable match code. Match
	// ids are a monotonic counter and therefore enumerable; the code is 128
	// bits of crypto/rand and is what clients present instead. Storing it
	// with the result is what keeps the batch 8 restart criterion true once
	// the read is gated: seat tokens are process-lifetime state, so a
	// credential that must outlive a restart has to live with the result.
	GetByCode(code string) (matchResult, bool, error)
	// MaxMatchID returns the highest match id already stored (0 when empty).
	// The service resumes its id counter from this value after a restart:
	// match ids are generated per process, so without it a new process hands
	// out ids that alias finished matches already in the durable store, the
	// fresh result is dropped by ON CONFLICT DO NOTHING, and reads of the new
	// match return the old match's data. See TestMatchIDsDoNotAliasAcrossRestarts.
	MaxMatchID() (uint64, error)
}
