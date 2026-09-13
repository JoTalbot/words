package main

// Batch 28b: partner mode. The device smoke needs a REAL opponent for the
// Unity client running on the emulator: the match must be formed through the
// matchmaking queue (the client presses "Find match"), the opponent must play
// legal words so the board actually drains, and the match must reach its
// terminal state within a bounded budget so a stuck run fails loudly instead
// of hanging a CI runner.
//
// This is deliberately NOT the exit-gate harness: the exact-cover script used
// by playOne assumes the bot controls BOTH seats and can therefore predict the
// whole match offline. Here a human-driven (well, adb-driven) client owns the
// other seat, so the partner plays reactively: every snapshot it receives, it
// looks for one legal word over the currently FREE cells and submits it,
// leaving the rest of the board to the device client.
//
// Fairness note: the partner is intentionally polite. It waits
// -partner-grace before its first word of each wave so the device client has
// a chance to claim cells; without that the bot solves a 12-cell board faster
// than a synthetic swipe can be scripted and the client would never see an
// accepted word of its own.

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	wordarenav1 "github.com/JoTalbot/words/server/gen/wordarena/net/v1"
	"github.com/JoTalbot/words/server/internal/dictionary"
)

// partnerConfig is the bounded operating envelope of one partner session.
type partnerConfig struct {
	addr     string
	lang     string
	waitFor  time.Duration // how long to wait for the device client to pair
	budget   time.Duration // hard cap on the whole match
	grace    time.Duration // delay before the partner's first word in a wave
	interval time.Duration // minimum delay between partner words
	verbose  bool
}

// freeCellWord finds one dictionary word spelled by currently free cells of
// the snapshot, preferring SHORT words: a 3-letter claim takes three cells,
// which keeps enough of the board free for the device client to play too.
// Returns the cell ids in path order.
func freeCellWord(snap *dictionary.Snapshot, cells []*wordarenav1.BoardCell, maxLen int) ([]uint32, string) {
	free := make([]*wordarenav1.BoardCell, 0, len(cells))
	for _, c := range cells {
		if c.GetOwnerUserId() == 0 {
			free = append(free, c)
		}
	}
	if len(free) < 3 {
		return nil, ""
	}
	counts := map[rune]int{}
	for _, c := range free {
		for _, r := range strings.ToLower(c.GetLetter()) {
			counts[r]++
			break
		}
	}
	if maxLen > len(free) {
		maxLen = len(free)
	}
	byLen := map[int][]string{}
	for _, w := range snap.Words() {
		l := len([]rune(w))
		if l >= 3 && l <= maxLen {
			byLen[l] = append(byLen[l], w)
		}
	}
	for l := 3; l <= maxLen; l++ {
		words := byLen[l]
		sort.Strings(words)
		for _, w := range words {
			tmp := map[rune]int{}
			for k, v := range counts {
				tmp[k] = v
			}
			ok := true
			for _, r := range w {
				tmp[r]--
				if tmp[r] < 0 {
					ok = false
					break
				}
			}
			if !ok {
				continue
			}
			used := map[uint32]bool{}
			ids := make([]uint32, 0, l)
			for _, r := range w {
				found := false
				for _, c := range free {
					if used[c.GetCellId()] {
						continue
					}
					letter := []rune(strings.ToLower(c.GetLetter()))
					if len(letter) > 0 && letter[0] == r {
						used[c.GetCellId()] = true
						ids = append(ids, c.GetCellId())
						found = true
						break
					}
				}
				if !found {
					ok = false
					break
				}
			}
			if ok && len(ids) == l {
				return ids, w
			}
		}
	}
	return nil, ""
}

// waitForPartnerMatch enqueues one anonymous seat and waits until the
// matchmaker pairs it with whoever else is in the queue - on the device run
// that is the Unity client on the emulator. Unlike createMatchViaQueue it
// never enqueues a second seat of its own.
func waitForPartnerMatch(cfg partnerConfig) (queueEntryView, error) {
	e, err := queueEnqueue(cfg.addr, cfg.lang)
	if err != nil {
		return queueEntryView{}, err
	}
	fmt.Printf("[PARTNER] enqueued %s (waiting up to %s for the device client)\n", e.QueueID, cfg.waitFor)
	deadline := time.Now().Add(cfg.waitFor)
	for time.Now().Before(deadline) {
		if e.Status == "matched" && e.MatchID != 0 {
			return e, nil
		}
		if e.Status == "expired" {
			// The queue entry has a server-side TTL; re-enqueue so a slow
			// emulator boot does not end the session.
			fmt.Println("[PARTNER] queue entry expired - re-enqueuing")
			if e, err = queueEnqueue(cfg.addr, cfg.lang); err != nil {
				return queueEntryView{}, err
			}
			continue
		}
		time.Sleep(500 * time.Millisecond)
		if e, err = queuePoll(cfg.addr, e.QueueID); err != nil {
			return queueEntryView{}, err
		}
	}
	return queueEntryView{}, fmt.Errorf("no opponent joined the queue within %s", cfg.waitFor)
}

// runPartner plays one match as the opponent of an external client. It
// returns the terminal snapshot, or an error when the budget expires.
func runPartner(cfg partnerConfig) error {
	snap, err := dictionary.LoadSnapshot(dictionary.Language(cfg.lang))
	if err != nil {
		return err
	}
	entry, err := waitForPartnerMatch(cfg)
	if err != nil {
		return err
	}
	fmt.Printf("[PARTNER] matched match_id=%d seed=%d user_id=%d\n", entry.MatchID, entry.Seed, entry.UserID)

	c, err := dialBot(cfg.addr, entry.MatchID, entry.Token)
	if err != nil {
		return fmt.Errorf("dial partner seat: %w", err)
	}
	defer c.close()

	ctx, cancel := context.WithTimeout(context.Background(), cfg.budget)
	defer cancel()

	var (
		seq        uint32
		lastPlayed time.Time
		waveStart  = time.Now()
		lastWave   = uint32(0)
		words      int
	)
	for {
		p, err := c.readPayload(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return fmt.Errorf("partner budget %s expired after %d words (match did not finish)", cfg.budget, words)
			}
			return err
		}
		s, ok := p.(*wordarenav1.MatchStateSnapshot)
		if !ok {
			continue
		}
		if s.GetCurrentWave() != lastWave {
			lastWave = s.GetCurrentWave()
			waveStart = time.Now()
			if cfg.verbose {
				fmt.Printf("[PARTNER] wave %d\n", lastWave)
			}
		}
		if s.GetOver() {
			fmt.Printf("[PARTNER] MATCH OVER match_id=%d scores=%d:%d partner_words=%d\n",
				s.GetMatchId(), s.GetPlayers()[0].GetScore(), s.GetPlayers()[1].GetScore(), words)
			fmt.Println("PARTNER-GATE: PASS (match reached its terminal state)")
			return nil
		}
		if time.Since(waveStart) < cfg.grace {
			continue
		}
		if time.Since(lastPlayed) < cfg.interval {
			continue
		}
		ids, word := freeCellWord(snap, s.GetCells(), 5)
		if ids == nil {
			continue
		}
		seq++
		if err := c.submit(ctx, entry.MatchID, ids, seq); err != nil {
			return fmt.Errorf("partner submit: %w", err)
		}
		lastPlayed = time.Now()
		words++
		if cfg.verbose {
			fmt.Printf("[PARTNER] submitted %q seq=%d cells=%v\n", strings.ToUpper(word), seq, ids)
		}
	}
}

// partnerMain is the -partner entry point, split out of main so the flag
// wiring stays readable.
func partnerMain(cfg partnerConfig) {
	if err := runPartner(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "PARTNER-GATE: FAIL: %v\n", err)
		os.Exit(1)
	}
}
