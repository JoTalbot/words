package protocol

import (
	"testing"

	"github.com/JoTalbot/words/server/internal/match"
	"github.com/JoTalbot/words/server/internal/matchroom"
	"google.golang.org/protobuf/proto"
)

// Batch 31B: these benchmarks are the evidence behind the shared-payload
// change. A canonical snapshot is identical for every seat, so encoding it per
// subscriber multiplies CPU and garbage by the roster size for no benefit.

func snapshotFixture(seats int) (match.Snapshot, []uint64) {
	m, err := match.New(match.Config{MatchID: 1, Seed: 1, Lang: "en", Seats: seats})
	if err != nil {
		panic(err)
	}
	ids := make([]uint64, seats)
	for i := range ids {
		ids[i] = uint64(i + 1)
	}
	return m.Snapshot(), ids
}

// benchPerSubscriber is the OLD behaviour, kept as the comparison baseline.
func benchPerSubscriber(b *testing.B, seats int) {
	snap, ids := snapshotFixture(seats)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for s := 0; s < seats; s++ {
			if _, err := proto.Marshal(SnapshotEnvelope(1, SnapshotToProto(snap, ids))); err != nil {
				b.Fatal(err)
			}
		}
	}
}

// benchShared is the CURRENT behaviour: one encoding reused by the roster.
func benchShared(b *testing.B, seats int) {
	snap, ids := snapshotFixture(seats)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		box := matchroom.NewSharedPayload()
		for s := 0; s < seats; s++ {
			if _, err := box.Bytes(func() ([]byte, error) {
				return proto.Marshal(SnapshotEnvelope(1, SnapshotToProto(snap, ids)))
			}); err != nil {
				b.Fatal(err)
			}
		}
	}
}

func BenchmarkSnapshotFanoutPerSubscriber2(b *testing.B)  { benchPerSubscriber(b, 2) }
func BenchmarkSnapshotFanoutShared2(b *testing.B)         { benchShared(b, 2) }
func BenchmarkSnapshotFanoutPerSubscriber60(b *testing.B) { benchPerSubscriber(b, 60) }
func BenchmarkSnapshotFanoutShared60(b *testing.B)        { benchShared(b, 60) }

// TestSharedFanoutIsCheaperThanPerSubscriber guards the optimisation against
// silent regression: if someone reverts the transport to encoding per
// connection, a full-roster fan-out gets dramatically more expensive and this
// fails. It is a ratio test, so it does not depend on host speed.
func TestSharedFanoutIsCheaperThanPerSubscriber(t *testing.T) {
	if testing.Short() {
		t.Skip("fan-out cost comparison skipped in -short")
	}
	const seats = 60
	per := testing.Benchmark(func(b *testing.B) { benchPerSubscriber(b, seats) })
	shared := testing.Benchmark(func(b *testing.B) { benchShared(b, seats) })

	perNs := float64(per.NsPerOp())
	sharedNs := float64(shared.NsPerOp())
	perAllocs := per.AllocsPerOp()
	sharedAllocs := shared.AllocsPerOp()
	t.Logf("%d-seat fan-out: per-subscriber %.0f ns / %d allocs, shared %.0f ns / %d allocs (%.1fx cheaper)",
		seats, perNs, perAllocs, sharedNs, sharedAllocs, perNs/sharedNs)

	if sharedNs*10 > perNs {
		t.Fatalf("shared fan-out (%.0f ns) is not at least 10x cheaper than per-subscriber (%.0f ns); "+
			"the transport is probably encoding once per connection again", sharedNs, perNs)
	}
	if sharedAllocs*10 > perAllocs {
		t.Fatalf("shared fan-out allocates %d per op vs %d per-subscriber; the encoding is not being reused",
			sharedAllocs, perAllocs)
	}
}
