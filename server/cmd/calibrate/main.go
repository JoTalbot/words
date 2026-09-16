// Command calibrate is the anti-snowball calibration harness (M2 batch 35A,
// PD-007, docs/M2-ANTI-SNOWBALL.md).
//
// The catch-up rule's constants (25/2/15) are engineering defaults, and the
// roadmap row stays [~] until there is evidence rather than a second guess.
// This tool is that evidence: for a grid of (seed, roster, constant-set) it
// plays a full deterministic match with the rule OFF (the baseline) and with
// it ON under the set's constants, driven by the same fixed policy on every
// seat, and measures how much of the score gap the bonus closes, whether it
// changes who wins, what it costs in points, and how often it fires at all
// (the per-match exposure).
//
// Determinism (PD-003): every match is a pure function of its (seed, roster,
// set, policy) tuple. There is no wall clock, no randomness and no map
// iteration order anywhere in the driver, so a re-run produces identical
// results, and the grid can be split across processes without any result
// depending on scheduling.
//
// Sharding: the grid is ordered by (seed, roster) with the constant sets
// innermost, and shards own whole (seed, roster) pairs, so a baseline match
// is computed exactly once per pair even though six sets reuse it. Run:
//
//	calibrate -shard 0 -shards 4 -out /tmp/sweep-0.json &
//	calibrate -shard 1 -shards 4 -out /tmp/sweep-1.json &
//	...
//	calibrate -merge /tmp/sweep-0.json,/tmp/sweep-1.json,... -out sweep.json
//
// The merge writes the full artifact (pairs + summary); report authors use
// `jq 'del(.pairs)'` on it for the document excerpt.
//
// This command is evidence tooling, not a service: it is not wired into the
// systemd deployment, and it is deleted with the calibration artifact when
// the constants are settled.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/JoTalbot/words/server/internal/dictionary"
	"github.com/JoTalbot/words/server/internal/match"
	"github.com/JoTalbot/words/server/internal/matchroom"
	"github.com/JoTalbot/words/server/internal/pve"
)

// The seed grid is fixed in code so the evidence is reproducible and a later
// re-run (for example when the policy changes) can be compared directly.
var defaultSeeds = []int64{
	101, 103, 107, 109, 113, 127, 131, 137,
	139, 149, 151, 157, 163, 167, 173, 179,
	181, 191, 193, 197, 199, 211, 223, 227,
}

// Constant sets under test. "default" is the shipped 25/2/15; the others
// bracket one constant at a time so the summary can say which of the three
// levers the evidence actually needs.
var constantSets = map[string]match.CatchUpParams{
	"default": {},
	"gap15":   {Gap: 15},
	"gap40":   {Gap: 40},
	"div3":    {Divisor: 3},
	"cap10":   {MaxBonus: 10},
	"cap20":   {MaxBonus: 20},
}

// Policy profiles. "aggressive" is the calibration driver: long enough words
// and a short enough interval that a gap actually opens inside one match -
// the precondition for the rule to matter. "polite" is the shipped practice
// opponent, included because the rule's value in the mode that exists today
// is exactly the question.
var policies = map[string]pve.Policy{
	"aggressive": {MinLen: match.MinWordLength, MaxLen: 6, Interval: 15, AllowSteal: true},
	"polite":     pve.DefaultPolicy(),
}

// matchRecord is one fully played match.
type matchRecord struct {
	Scores      []int64 `json:"scores"`
	Winner      int     `json:"winner"` // seat of the top score, -1 on a tie
	Words       int     `json:"words"`
	BonusEvents int     `json:"bonus_events,omitempty"`
	BonusPoints int64   `json:"bonus_points,omitempty"`
	Ticks       int     `json:"ticks"`
	WallMillis  int64   `json:"wall_ms"`
}

// pairRecord is one (seed, roster, set) configuration: the baseline match,
// the variant match, and the deltas the calibration question is about.
type pairRecord struct {
	Seed        int64       `json:"seed"`
	Roster      int         `json:"roster"`
	Set         string      `json:"set"`
	Params      string      `json:"params"`
	Baseline    matchRecord `json:"baseline"`
	Variant     matchRecord `json:"variant"`
	FinalGapOff int64       `json:"final_gap_off"`
	FinalGapOn  int64       `json:"final_gap_on"`
	Winner      int         `json:"winner_off"`
	WinnerOn    int         `json:"winner_on"`
	WinnerFlip  bool        `json:"winner_flipped"`
	BonusFired  bool        `json:"bonus_fired"`
}

// setSummary is one constant set's answer to the calibration question.
type setSummary struct {
	Matches        int     `json:"matches"`
	Fired          int     `json:"fired"` // matches where at least one bonus was awarded (exposure)
	FireRate       float64 `json:"fire_rate"`
	BonusPoints    int64   `json:"bonus_points_total"`
	AvgBonusPoints float64 `json:"avg_bonus_points_per_match"`
	WinnerFlips    int     `json:"winner_flips"`
	AvgGapOff      float64 `json:"avg_final_gap_off"`
	AvgGapOn       float64 `json:"avg_final_gap_on"`
	GapClosedPct   float64 `json:"avg_gap_closed_pct"` // mean over pairs: how much of the baseline final gap the bonus erased
}

type summary struct {
	Policy    string                        `json:"policy"`
	Language  string                        `json:"language"`
	Seeds     int                           `json:"seeds"`
	Rosters   []int                         `json:"rosters"`
	Generated string                        `json:"generated"`
	PerSet    map[string]*setSummary        `json:"per_set"`
	PerRoster map[string]map[string]float64 `json:"per_roster"` // roster -> {fire_rate, winner_flip_rate, avg_gap_closed_pct}
}

// sweepOut is one shard's file: the pair detail (del(.pairs) for the report
// excerpt) and nothing else - the summary is computed by the merge over all
// shards so it always covers the full grid.
type sweepOut struct {
	Shard  int          `json:"shard"`
	Of     int          `json:"of"`
	Policy string       `json:"policy"`
	Pairs  []pairRecord `json:"pairs"`
}

// driver plays one full match and returns its record. antiSnowball selects
// the rule; params its constants.
func driver(seatCount int, seed int64, policy pve.Policy, antiSnowball bool, params match.CatchUpParams, matchID uint64) matchRecord {
	userIDs := make([]uint64, seatCount)
	for i := range userIDs {
		userIDs[i] = uint64(i + 1)
	}
	room, err := matchroom.New(matchroom.Config{
		MatchID:      matchID,
		Seed:         uint64(seed),
		Language:     "en",
		SeatUserIDs:  userIDs,
		AntiSnowball: antiSnowball,
		CatchUp:      params,
	})
	if err != nil {
		panic(fmt.Sprintf("calibrate: room: %v", err))
	}
	ops := make([]*pve.Opponent, seatCount)
	for s := range ops {
		opp, err := pve.New("en", policy)
		if err != nil {
			panic(fmt.Sprintf("calibrate: opponent: %v", err))
		}
		ops[s] = opp
	}
	rec := matchRecord{}
	start := time.Now()
	// One seat per tick, round-robin with a ROTATING start: the rotating
	// order removes the systematic first-mover advantage a fixed seat order
	// would give seats 0..1. Without it, and with the policy's
	// shortest-lexicographic-first word choice, the two earliest seats enter
	// a degenerate steal ping-pong over the lexicographic minimum word of
	// the whole dictionary and the rest of the roster never scores - a
	// dynamics no real population (independent preferences, independent
	// timing) produces. One intent per tick also mirrors how the
	// deployment's per-seat intent limit paces real traffic.
	for i := 0; i < 4*60*60 && !room.IsOver(); i++ {
		m := room.Match()
		seat := match.Seat(i % seatCount)
		if !m.IsEliminated(seat) {
			if path := ops[seat].Intent(m.Snapshot(), m.Tick(), seat); path != nil {
				if _, err := room.Submit(seat, path); err != nil {
					panic(fmt.Sprintf("calibrate: submit: %v", err))
				}
			}
		}
		room.Tick()
	}
	if !room.IsOver() {
		panic("calibrate: the match never finished")
	}
	rec.Ticks = room.Match().Tick()
	rec.WallMillis = time.Since(start).Milliseconds()
	rec.Scores = make([]int64, seatCount)
	for s := range rec.Scores {
		rec.Scores[s] = room.Match().Score(match.Seat(s))
	}
	best := int64(-1)
	tied := false
	winner := 0
	for s, sc := range rec.Scores {
		if sc > best {
			best, winner, tied = sc, s, false
		} else if sc == best {
			tied = true
		}
	}
	if tied {
		winner = -1
	}
	rec.Winner = winner
	for _, ev := range room.Match().Events() {
		if ev.Result == match.ResultAccepted {
			rec.Words++
		}
		if ev.CatchUpBonus > 0 {
			rec.BonusEvents++
			rec.BonusPoints += ev.CatchUpBonus
		}
	}
	return rec
}

// finalGap is the leader's margin over the lowest-scoring seat - the gap the
// catch-up rule exists to close.
func finalGap(scores []int64) int64 {
	var lo, hi int64
	for i, sc := range scores {
		if i == 0 || sc < lo {
			lo = sc
		}
		if i == 0 || sc > hi {
			hi = sc
		}
	}
	return hi - lo
}

func parseSets(val string) ([]string, error) {
	var out []string
	for _, part := range strings.Split(val, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if _, ok := constantSets[part]; !ok {
			return nil, fmt.Errorf("unknown set %q", part)
		}
		out = append(out, part)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("-sets produced no values")
	}
	return out, nil
}

func parseInts(kind, val string, min, max int) ([]int, error) {
	var out []int
	for _, part := range strings.Split(val, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		n, err := strconv.Atoi(part)
		if err != nil || n < min || n > max {
			return nil, fmt.Errorf("bad %s %q (want %d..%d)", kind, part, min, max)
		}
		out = append(out, n)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("-%s produced no values", kind)
	}
	return out, nil
}

type gridPair struct {
	seed   int64
	roster int
}

func main() {
	var (
		outPath   = flag.String("out", "sweep.json", "output JSON path")
		seedsCSV  = flag.String("seeds", "", "comma list of seeds (default: the built-in 24)")
		rosterCSV = flag.String("rosters", "2,8,30,60", "comma list of roster sizes")
		setCSV    = flag.String("sets", "default,gap15,gap40,div3,cap10,cap20", "comma list of constant sets")
		policy    = flag.String("policy", "aggressive", "driver policy: aggressive|polite")
		lang      = flag.String("lang", "en", "dictionary language")
		shard     = flag.Int("shard", 0, "shard index (0-based)")
		shards    = flag.Int("shards", 1, "total shards")
		limit     = flag.Int("limit", 0, "run at most N pairs (0 = all; for smoke runs)")
		mergeCSV  = flag.String("merge", "", "merge the given shard JSON files into one output (empty = run)")
	)
	flag.Parse()

	if *mergeCSV != "" {
		if err := mergeShards(*mergeCSV, *outPath, *policy); err != nil {
			fmt.Fprintln(os.Stderr, "calibrate:", err)
			os.Exit(1)
		}
		fmt.Printf("merged %s -> %s\n", *mergeCSV, *outPath)
		return
	}

	if *shards < 1 || *shard < 0 || *shard >= *shards {
		fmt.Fprintln(os.Stderr, "calibrate: -shard must be in [0, -shards)")
		os.Exit(1)
	}
	if _, err := dictionary.ParseLanguage(*lang); err != nil {
		fmt.Fprintln(os.Stderr, "calibrate:", err)
		os.Exit(1)
	}

	var seeds []int64
	if *seedsCSV != "" {
		for _, part := range strings.Split(*seedsCSV, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			n, err := strconv.ParseInt(part, 10, 64)
			if err != nil || n < 1 {
				fmt.Fprintf(os.Stderr, "calibrate: bad seed %q\n", part)
				os.Exit(1)
			}
			seeds = append(seeds, n)
		}
	} else {
		seeds = defaultSeeds
	}
	if len(seeds) == 0 {
		fmt.Fprintln(os.Stderr, "calibrate: no seeds")
		os.Exit(1)
	}
	rosters, err := parseInts("rosters", *rosterCSV, match.MinSeats, match.MaxSeats)
	if err != nil {
		fmt.Fprintln(os.Stderr, "calibrate:", err)
		os.Exit(1)
	}
	sets, err := parseSets(*setCSV)
	if err != nil {
		fmt.Fprintln(os.Stderr, "calibrate:", err)
		os.Exit(1)
	}
	if _, ok := policies[*policy]; !ok {
		fmt.Fprintln(os.Stderr, "calibrate: unknown policy", *policy)
		os.Exit(1)
	}
	// Load the dictionary once so every match in this process shares the
	// exact same snapshot.
	if _, err := dictionary.LoadSnapshot(dictionary.Language(*lang)); err != nil {
		fmt.Fprintln(os.Stderr, "calibrate:", err)
		os.Exit(1)
	}

	// Grid order: (seed, roster) outer, sets inner. Shards own whole pairs,
	// so each baseline is computed exactly once per pair.
	var pairs []gridPair
	for _, s := range seeds {
		for _, r := range rosters {
			pairs = append(pairs, gridPair{s, r})
		}
	}
	forShard := 0
	for i := range pairs {
		if i%*shards == *shard {
			forShard++
		}
	}
	out := sweepOut{Shard: *shard, Of: *shards, Policy: *policy}
	start := time.Now()
	done := 0
	for i, p := range pairs {
		if i%*shards != *shard {
			continue
		}
		if *limit > 0 && done >= *limit {
			break
		}
		done++
		base := driver(p.roster, p.seed, policies[*policy], false, match.CatchUpParams{}, uint64(i*1000+1))
		for _, setName := range sets {
			var pr = pairRecord{
				Seed:        p.seed,
				Roster:      p.roster,
				Set:         setName,
				Params:      paramsString(constantSets[setName]),
				Baseline:    base,
				FinalGapOff: finalGap(base.Scores),
				Winner:      base.Winner,
			}
			vr := driver(p.roster, p.seed, policies[*policy], true, constantSets[setName], uint64(i*1000+2))
			pr.Variant = vr
			pr.FinalGapOn = finalGap(vr.Scores)
			pr.WinnerOn = vr.Winner
			pr.WinnerFlip = vr.Winner != base.Winner
			pr.BonusFired = vr.BonusEvents > 0
			out.Pairs = append(out.Pairs, pr)
		}
		fmt.Printf("shard %d/%d: %d/%d pairs done (%.0fs elapsed)\n",
			*shard, *shards, done, forShard, time.Since(start).Seconds())
	}

	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		fmt.Fprintln(os.Stderr, "calibrate:", err)
		os.Exit(1)
	}
	if err := os.WriteFile(*outPath, b, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "calibrate:", err)
		os.Exit(1)
	}
	fmt.Printf("shard %d: wrote %d pairs to %s in %.0fs\n", *shard, len(out.Pairs), *outPath, time.Since(start).Seconds())
}

func paramsString(p match.CatchUpParams) string {
	d := match.DefaultCatchUpParams()
	gap, div, cap := p.Gap, p.Divisor, p.MaxBonus
	if gap == 0 {
		gap = d.Gap
	}
	if div == 0 {
		div = d.Divisor
	}
	if cap == 0 {
		cap = d.MaxBonus
	}
	return fmt.Sprintf("%d/%d/%d", gap, div, cap)
}

// mergeShards combines shard files into one output (pairs in grid order is
// not required for the summary, only completeness) and prints the summary.
// The merged file is the calibration artifact.
func mergeShards(csv, outPath, policy string) error {
	var files []string
	for _, f := range strings.Split(csv, ",") {
		if f = strings.TrimSpace(f); f != "" {
			files = append(files, f)
		}
	}
	var all []pairRecord
	seen := map[string]bool{}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			return err
		}
		var shardOut sweepOut
		if err := json.Unmarshal(b, &shardOut); err != nil {
			return fmt.Errorf("%s: %w", f, err)
		}
		for _, p := range shardOut.Pairs {
			key := fmt.Sprintf("%d/%d/%s", p.Seed, p.Roster, p.Set)
			if seen[key] {
				return fmt.Errorf("duplicate pair %s (in %s)", key, f)
			}
			seen[key] = true
			all = append(all, p)
		}
	}
	if len(all) == 0 {
		return fmt.Errorf("no pairs in %s", csv)
	}
	merged := sweepOut{Policy: policy, Pairs: all}
	b, err := json.MarshalIndent(merged, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(outPath, b, 0o644); err != nil {
		return err
	}
	printSummary(all, policy)
	return nil
}

func printSummary(pairs []pairRecord, policy string) {
	perSet := map[string]*setSummary{}
	perRoster := map[string]map[string]float64{}
	rosterCount := map[string]int{}
	seeds := map[int64]bool{}
	rosters := map[int]bool{}
	for _, p := range pairs {
		seeds[p.Seed] = true
		rosters[p.Roster] = true
		s := perSet[p.Set]
		if s == nil {
			s = &setSummary{}
			perSet[p.Set] = s
		}
		s.Matches++
		if p.BonusFired {
			s.Fired++
		}
		s.BonusPoints += p.Variant.BonusPoints
		s.AvgBonusPoints += float64(p.Variant.BonusPoints)
		if p.WinnerFlip {
			s.WinnerFlips++
		}
		s.AvgGapOff += float64(p.FinalGapOff)
		s.AvgGapOn += float64(p.FinalGapOn)
		if p.FinalGapOff > 0 {
			closed := float64(p.FinalGapOff-p.FinalGapOn) / float64(p.FinalGapOff)
			if closed < 0 {
				closed = 0
			}
			s.GapClosedPct += closed
		}
		rk := strconv.Itoa(p.Roster)
		if perRoster[rk] == nil {
			perRoster[rk] = map[string]float64{}
		}
		if p.BonusFired {
			perRoster[rk]["fired"]++
		}
		if p.WinnerFlip {
			perRoster[rk]["winner_flips"]++
		}
		if p.FinalGapOff > 0 {
			closed := float64(p.FinalGapOff-p.FinalGapOn) / float64(p.FinalGapOff)
			if closed < 0 {
				closed = 0
			}
			perRoster[rk]["gap_closed"] += closed
		}
		rosterCount[rk]++
	}
	rosterList := make([]int, 0, len(rosters))
	for r := range rosters {
		rosterList = append(rosterList, r)
	}
	sort.Ints(rosterList)
	sum := summary{
		Policy:    policy,
		Language:  "en",
		Seeds:     len(seeds),
		Rosters:   rosterList,
		Generated: time.Now().UTC().Format(time.RFC3339),
		PerSet:    perSet,
		PerRoster: perRoster,
	}
	for _, s := range perSet {
		if s.Matches > 0 {
			s.FireRate = float64(s.Fired) / float64(s.Matches)
			s.AvgBonusPoints = s.AvgBonusPoints / float64(s.Matches)
			s.AvgGapOff = s.AvgGapOff / float64(s.Matches)
			s.AvgGapOn = s.AvgGapOn / float64(s.Matches)
			s.GapClosedPct = s.GapClosedPct / float64(s.Matches)
		}
	}
	for rk, m := range perRoster {
		n := float64(rosterCount[rk])
		m["fire_rate"] = m["fired"] / n
		m["winner_flip_rate"] = m["winner_flips"] / n
		m["avg_gap_closed_pct"] = m["gap_closed"] / n
		delete(m, "fired")
		delete(m, "winner_flips")
		delete(m, "gap_closed")
	}
	b, _ := json.MarshalIndent(sum, "", "  ")
	fmt.Println(string(b))
}
