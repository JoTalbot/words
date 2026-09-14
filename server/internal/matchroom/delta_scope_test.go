package matchroom

import (
	"reflect"
	"testing"

	"github.com/JoTalbot/words/server/internal/match"
)

// Batch 32A room-level contract. The protocol package proves that a delta
// reconstructs its frame exactly; these tests pin WHICH rooms are allowed to
// hand out deltas at all, because that is the part that decides whether the
// existing two-seat wire contract can change.
//
// The rule: a roster larger than 1v1 keeps a previous-snapshot base per frame
// so a transport can offer a delta; a two-seat room never does, so a 1v1
// connection can only ever be sent a full snapshot - byte-identical to the
// behaviour it had before this batch.

func roomWithSeats(t *testing.T, seats int, seed uint64) *Room {
	t.Helper()
	ids := make([]uint64, seats)
	for i := range ids {
		ids[i] = uint64(i + 10)
	}
	room, err := New(Config{MatchID: 7, Seed: seed, Language: "en", SeatUserIDs: ids})
	if err != nil {
		t.Fatalf("new %d-seat room: %v", seats, err)
	}
	return room
}

func collectFrames(t *testing.T, room *Room, want int) []SnapshotFrame {
	t.Helper()
	sub := room.Subscribe(0)
	frames := make([]SnapshotFrame, 0, want)
	for tick := 0; tick < match.TicksPerSecond*(want+2) && len(frames) < want; tick++ {
		room.Tick()
		for {
			select {
			case f := <-sub.Snapshots:
				frames = append(frames, f)
				continue
			default:
			}
			break
		}
	}
	if len(frames) < want {
		t.Fatalf("collected %d frames, want %d", len(frames), want)
	}
	return frames[:want]
}

func TestRoomScopesDeltaBasesToLargerRosters(t *testing.T) {
	for _, tc := range []struct {
		seats   int
		wantDel bool
	}{
		{2, false},
		{4, true},
		{8, true},
		{60, true},
	} {
		t.Run(seatsName(tc.seats), func(t *testing.T) {
			room := roomWithSeats(t, tc.seats, 99)
			if got := room.DeltaSnapshots(); got != tc.wantDel {
				t.Fatalf("DeltaSnapshots() = %v for %d seats, want %v", got, tc.seats, tc.wantDel)
			}

			frames := collectFrames(t, room, 5)
			if !tc.wantDel {
				for i, f := range frames {
					if f.Base != nil || f.EncodedDelta != nil {
						t.Fatalf("frame %d of a %d-seat room carries a delta base; "+
							"the two-seat wire contract must not change", i, tc.seats)
					}
				}
				return
			}

			// The first frame of a room has no predecessor to diff against.
			if frames[0].Base != nil {
				t.Fatalf("the first frame carries a base (version %d)", frames[0].Base.StateVersion)
			}
			if frames[0].EncodedDelta != nil {
				t.Fatal("the first frame carries a delta box with no base")
			}
			for i := 1; i < len(frames); i++ {
				base := frames[i].Base
				if base == nil {
					t.Fatalf("frame %d has no base; consecutive frames must chain", i)
				}
				if frames[i].EncodedDelta == nil {
					t.Fatalf("frame %d has a base but no delta box", i)
				}
				if got, want := base.StateVersion, frames[i-1].Snapshot.StateVersion; got != want {
					t.Fatalf("frame %d is based on version %d, but the previous frame was %d: "+
						"a delta applied by a client would silently mis-reconstruct", i, got, want)
				}
			}
		})
	}
}

func TestRoomDeltaBaseIsNotMutatedByLaterFrames(t *testing.T) {
	room := roomWithSeats(t, 8, 1234)
	frames := collectFrames(t, room, 4)

	first := *frames[1].Base
	_ = collectFrames(t, room, 2)
	if frames[1].Base == nil {
		t.Fatal("base pointer vanished")
	}
	if !reflect.DeepEqual(*frames[1].Base, first) {
		t.Fatal("a room-owned base snapshot changed after later frames were broadcast; " +
			"deltas are computed against it lazily, so this would corrupt them")
	}
}

func seatsName(n int) string {
	return "seats_" + itoa(n)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [8]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
