package main

import (
	"net/http/httptest"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	wordarenav1 "github.com/JoTalbot/words/server/gen/wordarena/net/v1"
	"github.com/JoTalbot/words/server/internal/dictionary"
	"github.com/JoTalbot/words/server/internal/match"
	"github.com/JoTalbot/words/server/internal/matchroom"
	"github.com/JoTalbot/words/server/internal/protocol"
)

// Batch 32A transport tests. The protocol package proves a delta reconstructs
// its frame; these prove the TRANSPORT chooses correctly, which is where a
// mistake would be most expensive: a client handed a delta it cannot apply
// diverges silently, and nothing in the simulation would notice.

// frameFor pulls one real snapshot frame out of a room, so the encoder is
// exercised against frames the room actually produced rather than a fixture.
func frameFor(t *testing.T, room *matchroom.Room, skip int) matchroom.SnapshotFrame {
	t.Helper()
	sub := room.Subscribe(0)
	var frames []matchroom.SnapshotFrame
	for tick := 0; tick < match.TicksPerSecond*(skip+3) && len(frames) <= skip; tick++ {
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
	if len(frames) <= skip {
		t.Fatalf("only %d frames arrived, wanted at least %d", len(frames), skip+1)
	}
	return frames[skip]
}

func rosterRoom(t *testing.T, seats int) *matchroom.Room {
	t.Helper()
	ids := make([]uint64, seats)
	for i := range ids {
		ids[i] = uint64(i + 1)
	}
	room, err := matchroom.New(matchroom.Config{MatchID: 5, Seed: e2eSeed, Language: "en", SeatUserIDs: ids})
	if err != nil {
		t.Fatalf("new room: %v", err)
	}
	// Drive one accepted claim so the state version advances: without a
	// mutation, every frame version stays 0 and "the connection fell behind"
	// cannot be expressed at all.
	if ev, err := room.Submit(0, []int{9, 1, 0}); err != nil { // c, a, t on this seed
		t.Fatalf("seed claim: %v", err)
	} else if ev.Result != match.ResultAccepted {
		t.Fatalf("seed claim was %v, want ACCEPTED; the fixture depends on it", ev.Result)
	}
	return room
}

// TestEncodeSnapshotFrameChoosesBySyncState is the heart of the transport
// contract: in sync means delta, out of sync means full, and a connection that
// fell behind re-syncs by itself on the very next frame instead of staying
// broken for the rest of the match.
func TestEncodeSnapshotFrameChoosesBySyncState(t *testing.T) {
	room := rosterRoom(t, 8)
	first := frameFor(t, room, 0)
	second := frameFor(t, room, 1)

	// A room's first frame has no base, so it can only ever be sent in full.
	if b, err := encodeSnapshotFrame(5, room, first, new(int), true); err != nil {
		t.Fatalf("first frame: %v", err)
	} else if env := decodeEnvelope(t, b); env.GetSnapshot() == nil {
		t.Fatal("a frame with no base was not sent as a full snapshot")
	}

	if second.Base == nil {
		t.Fatal("the second frame of an 8-seat room has no base; nothing to test")
	}

	// In sync: the connection holds exactly the base version.
	inSync := second.Base.StateVersion
	if b, err := encodeSnapshotFrame(5, room, second, &inSync, true); err != nil {
		t.Fatalf("in sync: %v", err)
	} else {
		env := decodeEnvelope(t, b)
		if env.GetSnapshotDelta() == nil {
			t.Fatalf("an in-sync connection was sent a full snapshot instead of a delta: %v", env)
		}
		if env.GetSnapshot() != nil {
			t.Fatal("an in-sync connection was sent both arms")
		}
		if got, want := env.GetSnapshotDelta().BaseVersion, uint32(second.Base.StateVersion); got != want {
			t.Fatalf("delta base_version %d, want %d", got, want)
		}
		if got, want := inSync, second.Snapshot.StateVersion; got != want {
			t.Fatalf("the connection's tracked version is %d after the delta, want %d", got, want)
		}
	}

	// Out of sync: the connection missed frames, so it must be re-anchored
	// with a full snapshot. Both directions are covered - a connection that
	// fell BEHIND the base (dropped frames) and one that is somehow ahead of
	// it (which the server must never interpret as "apply anyway").
	stale := []int{second.Base.StateVersion + 1, second.Base.StateVersion + 3}
	if v := second.Base.StateVersion; v > 0 {
		stale = append(stale, v-1, 0)
	}
	for _, stale := range stale {
		v := stale
		b, err := encodeSnapshotFrame(5, room, second, &v, true)
		if err != nil {
			t.Fatalf("out of sync (%d): %v", stale, err)
		}
		if env := decodeEnvelope(t, b); env.GetSnapshotDelta() != nil {
			t.Fatalf("a connection holding version %d was sent a delta based on %d",
				stale, second.Base.StateVersion)
		}
		if v != second.Snapshot.StateVersion {
			t.Fatalf("after a full snapshot the tracked version is %d, want %d", v, second.Snapshot.StateVersion)
		}
	}

	// Self-healing: having just been re-anchored, the same connection is back
	// to receiving deltas.
	if b, err := encodeSnapshotFrame(5, room, second, &inSync, true); err != nil {
		t.Fatalf("after resync: %v", err)
	} else if decodeEnvelope(t, b).GetSnapshotDelta() == nil {
		t.Fatal("a re-synced connection did not return to deltas")
	}
}

func decodeEnvelope(t *testing.T, b []byte) *wordarenav1.ServerEnvelope {
	t.Helper()
	var env wordarenav1.ServerEnvelope
	if err := proto.Unmarshal(b, &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return &env
}

// TestOneVsOneWireStillCarriesOnlyFullSnapshots guards the compatibility rule
// this batch was built around: the M0/M1 two-seat contract does not change.
// Whatever a client could observe before, it observes now, byte for byte.
func TestOneVsOneWireStillCarriesOnlyFullSnapshots(t *testing.T) {
	api := NewAPI()
	defer api.Stop()
	srv := httptest.NewServer(api.Routes())
	defer srv.Close()

	seed := uint64(e2eSeed)
	id, tokens, _ := createMatch(t, srv, "en", &seed)
	c := dial(t, srv, id, tokens[0])
	defer c.close()

	// The anchor plus two further frames; snapshots are 1 Hz, so this costs
	// about two seconds.
	for i := 0; i < 3; i++ {
		env, payload := c.readEnvelope(t)
		if env.GetSnapshotDelta() != nil {
			t.Fatalf("frame %d of a 1v1 match arrived as a delta", i)
		}
		if _, ok := payload.(*wordarenav1.MatchStateSnapshot); !ok {
			t.Fatalf("frame %d of a 1v1 match was not a full snapshot: %T", i, payload)
		}
	}
}

// TestRosterWireCarriesDeltasThatReconstructTheBoard drives the real
// WebSocket surface at a roster size that enables deltas and checks the client
// can actually follow the board from them: every delta has to fit the state
// the client is holding, and the ownership it reconstructs must agree with the
// independently delivered word events.
func TestRosterWireCarriesDeltasThatReconstructTheBoard(t *testing.T) {
	const seats = 8
	api := NewAPI()
	defer api.Stop()
	srv := httptest.NewServer(api.Routes())
	defer srv.Close()

	seed := uint64(e2eSeed)
	info, err := api.createRoomN("en", &seed, nil, seats, false)
	if err != nil {
		t.Fatalf("createRoomN: %v", err)
	}
	c := dial(t, srv, info.ID, info.Tokens[0])
	defer c.close()
	c.sendHello(t, true)

	// Anchor: the first frame a connection receives is always a full snapshot.
	env, payload := c.readEnvelope(t)
	_ = c.readHello(t) // negotiate deltas for this modern client
	anchor, ok := payload.(*wordarenav1.MatchStateSnapshot)
	if !ok {
		t.Fatalf("first frame was %T, want a full snapshot", payload)
	}
	if env.GetSnapshotDelta() != nil {
		t.Fatal("the anchor frame was a delta")
	}
	held := anchor

	// Play a word from seat 5 so the board really changes - a delta over an
	// idle board would prove very little.
	actor := dial(t, srv, info.ID, info.Tokens[5])
	defer actor.close()
	actor.submit(t, info.ID, []uint32{9, 1, 0}, 1) // c, a, t: ACCEPTED on this seed
	ev := actor.readWordEvent(t)
	if ev.Result != wordarenav1.WordResult_ACCEPTED {
		t.Fatalf("fixture word was not accepted: %v", ev.Result)
	}

	var deltas int
	deadline := time.Now().Add(12 * time.Second)
	for time.Now().Before(deadline) && deltas < 2 {
		env, payload := c.readEnvelope(t)
		switch p := payload.(type) {
		case *wordarenav1.MatchStateSnapshot:
			held = p
		case *wordarenav1.MatchStateDelta:
			deltas++
			// The client can only apply a delta whose base it holds. This is
			// the invariant the transport's per-connection tracking exists to
			// guarantee; if it ever slipped, a real client would diverge here.
			if p.BaseVersion != held.StateVersion {
				t.Fatalf("delta is based on version %d but the client holds %d",
					p.BaseVersion, held.StateVersion)
			}
			if p.MatchId != info.ID {
				t.Fatalf("delta for match %d on match %d", p.MatchId, info.ID)
			}
			held = protocol.ApplyDelta(held, p)
			if held.StateVersion != p.StateVersion {
				t.Fatalf("reconstruction ended at version %d, delta said %d",
					held.StateVersion, p.StateVersion)
			}
			// A reconstructed state must stay internally consistent: the
			// roster is fixed, and no cell may be owned by a user the client
			// has never been told about.
			if len(held.Players) != seats {
				t.Fatalf("reconstruction has %d players, want %d", len(held.Players), seats)
			}
			if want := match.BoardCells(seats); len(held.Cells) != want {
				t.Fatalf("reconstruction has %d cells, want %d", len(held.Cells), want)
			}
			known := map[uint64]bool{}
			for _, pl := range held.Players {
				known[pl.UserId] = true
			}
			for _, cell := range held.Cells {
				if o := cell.OwnerUserId; o != 0 && !known[o] {
					t.Fatalf("cell %d is owned by user %d, who is not in the reconstructed roster",
						cell.CellId, o)
				}
			}
			// Cross-check against the independent event stream: once the
			// reconstruction reaches a version at or past the accepted word,
			// the claimed cell must belong to the user who played it.
			if uint32(held.StateVersion) >= ev.StateVersion && deltas == 1 {
				claimed := ev.ClaimedCellIndex
				for _, cell := range held.Cells {
					if cell.CellId != claimed {
						continue
					}
					if cell.OwnerUserId == 0 {
						t.Fatalf("after reconstructing to version %d, cell %d is still unowned, "+
							"but the server accepted a word claiming it", held.StateVersion, claimed)
					}
					if cell.OwnerUserId != ev.UserId {
						t.Fatalf("cell %d belongs to user %d, but user %d claimed it",
							claimed, cell.OwnerUserId, ev.UserId)
					}
				}
			}
		case *wordarenav1.WordValidatedEvent:
			// Every seat observes every word; not this test's subject.
		default:
			t.Fatalf("unexpected payload %T", payload)
		}
		_ = env
	}
	if deltas == 0 {
		t.Fatal("no delta arrived over the wire for an 8-seat match")
	}
}

// TestDeltaLanguageDictionaryIsAvailable keeps the fixture honest: the
// reconstructed-board assertion above assumes the English snapshot loads,
// which is also what the rooms use.
func TestDeltaLanguageDictionaryIsAvailable(t *testing.T) {
	snap, err := dictionary.LoadSnapshot(dictionary.Language("en"))
	if err != nil {
		t.Fatalf("load en dictionary: %v", err)
	}
	if !snap.Contains("cat") {
		t.Fatal(`the seed ${e2eSeed} fixture plays "cat"; the dictionary no longer accepts it`)
	}
}
