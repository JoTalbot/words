package matchroom

import (
	"testing"
)

// TestSubmitWithSeques echoes the client intent sequence in the event
// frame so clients can correlate intents to outcomes (protocol doc
// WIRE-PROTOCOL.md §2; M0 acceptance 3 / telemetry aid).
func TestSubmitWithSeqEchosClientSequence(t *testing.T) {
	// Use a controlled board so indices are always valid.
	r, err := New(Config{
		MatchID:  99,
		Seed:     42,
		Language: "en",
		UserIDs:  [2]uint64{101, 102},
		Token0:   "tok0",
		Token1:   "tok1",
	})
	if err != nil {
		t.Fatalf("new room: %v", err)
	}

	// Index 0 is always a valid cell; submission may be rejected but
	// the sequence must still echo.
	frame, err := r.SubmitWithSeq(0, 777, []int{0})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if frame.ClientSeq != 777 {
		t.Errorf("ClientSeq = %d, want 777", frame.ClientSeq)
	}
	if frame.Seat != 0 {
		t.Errorf("Seat = %d, want 0", frame.Seat)
	}

	// Default Submit (without sequence) should preserve zero.
	frame0, err := r.Submit(1, []int{0})
	if err != nil {
		t.Fatalf("submit default: %v", err)
	}
	if frame0.ClientSeq != 0 {
		t.Errorf("default ClientSeq = %d, want 0", frame0.ClientSeq)
	}
}
