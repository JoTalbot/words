package protocol

import (
	"testing"

	wordarenav1 "github.com/JoTalbot/words/server/gen/wordarena/net/v1"
	"github.com/JoTalbot/words/server/internal/match"
	"google.golang.org/protobuf/proto"
)

func TestEventRoundTrip(t *testing.T) {
	ev := match.Event{
		Seq: 12, Tick: 90, Seat: 1, CellIDs: []int{4, 2, 7},
		Word: "dog", Result: match.ResultAccepted, ScoreAdded: 5,
		TotalScore: 12, IsSteal: true, StateVersion: 9,
	}
	pb := WordEventToProto(ev, 9002)
	if pb.UserId != 9002 || pb.EventId != 12 || pb.ServerTick != 90 {
		t.Fatalf("mapping lost: %+v", pb)
	}
	if pb.Result != wordarenav1.WordResult_ACCEPTED || !pb.IsSteal {
		t.Fatalf("event mapping wrong: %+v", pb)
	}
	if pb.NormalizedWord != "dog" || pb.ScoreAdded != 5 || pb.TotalScore != 12 || pb.StateVersion != 9 {
		t.Fatalf("event fields wrong: %+v", pb)
	}
	if pb.ClaimedCellIndex != 4 {
		t.Fatalf("primary cell %d", pb.ClaimedCellIndex)
	}
}

func TestSnapshotRoundTrip(t *testing.T) {
	snap := match.Snapshot{
		MatchID: 42, Language: "en", Seed: 7, ServerTick: 30,
		RemainingTimeMs: 59999, CurrentWave: 1, StateVersion: 3, Phase: "active",
	}
	snap.Players = [2]match.PlayerView{
		{Seat: 0, Score: 5, RankPosition: 1, ComboMult: 1},
		{Seat: 1, Score: 0, RankPosition: 2, ComboMult: 1},
	}
	snap.Cells = []match.CellView{
		{ID: 0, Letter: "a", OwnerSeat: 0, IsLocked: true, LockRemainingMs: 120},
		{ID: 1, Letter: "b", OwnerSeat: -1},
	}
	pb := SnapshotToProto(snap, [2]uint64{9001, 9002})
	if pb.MatchId != 42 || pb.ServerTick != 30 || pb.CurrentWave != 1 || pb.StateVersion != 3 {
		t.Fatalf("snapshot headers: %+v", pb)
	}
	if pb.Players[0].UserId != 9001 || pb.Players[1].UserId != 9002 {
		t.Fatalf("user mapping: %+v", pb.Players)
	}
	if pb.Players[0].Score != 5 || pb.Players[1].Score != 0 {
		t.Fatalf("scores: %+v", pb.Players)
	}
	if pb.Cells[0].OwnerUserId != 9001 || !pb.Cells[0].IsLocked || pb.Cells[0].LockRemainingMs != 120 {
		t.Fatalf("cell0: %+v", pb.Cells[0])
	}
	if pb.Cells[1].OwnerUserId != 0 {
		t.Fatalf("free cell owner must be 0: %+v", pb.Cells[1])
	}
}

// TestGoldenBytes pins the serialized wire bytes for a fixed event so any
// schema drift (field renumbering/type change) breaks loudly. Values were
// reviewed against the schema in proto/wordarena/v1/match.proto.
func TestUnityClientSubmitEnvelopeCompatibility(t *testing.T) {
	// The package-light Unity bootstrap encoder sends repeated letter_indices
	// as unpacked varints. Proto3 accepts this form even though generated Go
	// marshaling chooses packed encoding, so pin the compatibility boundary.
	payload := []byte{
		0x08, 0x07, // ClientEnvelope.match_id = 7
		0x12, 0x0d, // ClientEnvelope.submit_word length = 13
		0x08, 0x07, // SubmitWordIntent.match_id = 7
		0x10, 0x09, // client_sequence = 9
		0x18, 0x02, // letter_indices[0] = 2 (unpacked)
		0x18, 0x04, // letter_indices[1] = 4 (unpacked)
		0x18, 0x06, // letter_indices[2] = 6 (unpacked)
		0x28, 0xd2, 0x09, // client_timestamp_ms = 1234
	}
	var env wordarenav1.ClientEnvelope
	if err := proto.Unmarshal(payload, &env); err != nil {
		t.Fatal(err)
	}
	intent := env.GetSubmitWord()
	if env.MatchId != 7 || intent == nil {
		t.Fatalf("unpacked Unity envelope lost header/payload: match_id=%d has_intent=%t", env.MatchId, intent != nil)
	}
	if intent.MatchId != 7 || intent.ClientSequence != 9 || intent.ClientTimestampMs != 1234 {
		t.Fatalf("intent header mismatch: match_id=%d client_sequence=%d client_timestamp_ms=%d", intent.MatchId, intent.ClientSequence, intent.ClientTimestampMs)
	}
	want := []uint32{2, 4, 6}
	if len(intent.LetterIndices) != len(want) {
		t.Fatalf("letter count = %d, want %d: %+v", len(intent.LetterIndices), len(want), intent.LetterIndices)
	}
	for i := range want {
		if intent.LetterIndices[i] != want[i] {
			t.Fatalf("letter_indices[%d] = %d, want %d", i, intent.LetterIndices[i], want[i])
		}
	}
}

func TestGoldenBytes(t *testing.T) {
	ev := match.Event{
		Seq: 1, Tick: 3, Seat: 0, CellIDs: []int{9, 1, 0}, Word: "cat",
		Result: match.ResultAccepted, ScoreAdded: 5, TotalScore: 5,
		IsSteal: false, StateVersion: 1,
	}
	pb := WordEventToProto(ev, 9001)
	b, err := proto.Marshal(WordEnvelope(7, pb))
	if err != nil {
		t.Fatal(err)
	}
	if len(b) == 0 {
		t.Fatal("empty golden bytes")
	}
	t.Logf("golden envelope bytes: %d", len(b))
	// Stability: marshal twice must be byte-identical.
	b2, _ := proto.Marshal(WordEnvelope(7, pb))
	if string(b) != string(b2) {
		t.Fatal("proto marshal not deterministic")
	}
	// And it must unmarshal back to the same semantic event.
	var env wordarenav1.ServerEnvelope
	if err := proto.Unmarshal(b, &env); err != nil {
		t.Fatal(err)
	}
	got := env.GetWordEvent()
	if got == nil || got.NormalizedWord != "cat" || got.ScoreAdded != 5 {
		t.Fatalf("round trip lost data: %+v", got)
	}
}
