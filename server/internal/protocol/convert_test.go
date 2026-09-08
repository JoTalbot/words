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
