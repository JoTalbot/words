package match

import "testing"

// Q5 (M2 batch 32D): the simulated-player declaration is canonical state, not a
// presentation detail. These tests pin the two properties that make it safe to
// build ratings and rewards on top of: it is part of the fingerprint, and it is
// never inferred from anything else.

func TestBotDeclarationIsPartOfTheFingerprint(t *testing.T) {
	human, err := New(Config{MatchID: 1, Seed: 7, Lang: "en", Seats: 4})
	if err != nil {
		t.Fatal(err)
	}
	bot, err := New(Config{MatchID: 1, Seed: 7, Lang: "en", Seats: 4, BotSeats: []bool{false, true}})
	if err != nil {
		t.Fatal(err)
	}
	if human.Fingerprint() == bot.Fingerprint() {
		t.Fatal("declaring a seat a bot produced the same fingerprint: a substituted opponent " +
			"could replay as the same match")
	}
	if got := bot.BotSeats(); len(got) != 1 || got[0] != 1 {
		t.Fatalf("BotSeats = %v, want [1]", got)
	}
	if !bot.HasBot() || human.HasBot() {
		t.Fatalf("HasBot: declared=%v, undeclared=%v, want true/false", bot.HasBot(), human.HasBot())
	}
}

func TestBotDeclarationIsNeverInferred(t *testing.T) {
	// A seat with no profile, no user id and no token is still a HUMAN seat as
	// far as disclosure goes. Q5 forbids guessing, so the only input is the
	// declaration itself.
	m, err := New(Config{MatchID: 1, Seed: 7, Lang: "en", Seats: 4})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range m.Players() {
		if p.IsBot {
			t.Fatalf("seat %d was inferred to be a bot from an undeclared config", p.Seat)
		}
	}
	for _, p := range m.Snapshot().Players {
		if p.IsBot {
			t.Fatalf("seat %d reached the snapshot as a bot", p.Seat)
		}
	}
}

func TestBotDeclarationLongerThanRosterIsIgnored(t *testing.T) {
	// A caller that hands in a longer declaration than it has seats must not be
	// able to take the process down, and must not silently seat a bot past the
	// roster either.
	m, err := New(Config{MatchID: 1, Seed: 7, Lang: "en", Seats: 2, BotSeats: []bool{true, true, true}})
	if err != nil {
		t.Fatalf("an over-long declaration was fatal: %v", err)
	}
	if got := m.BotSeats(); len(got) != 2 {
		t.Fatalf("BotSeats = %v, want exactly the two seats in the roster", got)
	}
}
