package main

import (
	"net/http/httptest"
	"testing"
	"time"

	wordarenav1 "github.com/JoTalbot/words/server/gen/wordarena/net/v1"
	"github.com/JoTalbot/words/server/internal/protocol"
	"github.com/coder/websocket"
	"google.golang.org/protobuf/proto"
)

// TestProtocolVersionDeltaOptOut is the heart of the debt fix (M2 batch 33):
// a client that negotiates supports_delta=false must NEVER be served a
// MatchStateDelta. The server falls back to full snapshots for it, even on a
// roster size that would otherwise stream deltas.
func TestProtocolVersionDeltaOptOut(t *testing.T) {
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
	c.sendHello(t, false)

	_, payload := c.readEnvelope(t)
	if _, ok := payload.(*wordarenav1.MatchStateSnapshot); !ok {
		t.Fatalf("first frame was %T, want full snapshot", payload)
	}
	sh := c.readHello(t)
	if sh.UseDelta {
		t.Fatalf("server negotiated use_delta=true for a supports_delta=false client")
	}

	actor := dial(t, srv, info.ID, info.Tokens[5])
	defer actor.close()
	actor.submit(t, info.ID, []uint32{9, 1, 0}, 1)
	if ev := actor.readWordEvent(t); ev.Result != wordarenav1.WordResult_ACCEPTED {
		t.Fatalf("fixture word not accepted: %v", ev.Result)
	}

	deadline := time.Now().Add(12 * time.Second)
	var deltas int
	for time.Now().Before(deadline) {
		_, payload := c.readEnvelope(t)
		switch payload.(type) {
		case *wordarenav1.MatchStateDelta:
			deltas++
		case *wordarenav1.MatchStateSnapshot:
		case *wordarenav1.WordValidatedEvent:
		default:
			t.Fatalf("unexpected payload %T", payload)
		}
	}
	if deltas != 0 {
		t.Fatalf("a supports_delta=false client received %d delta frames; it must receive only full snapshots", deltas)
	}
}

// TestProtocolVersionDeltaOptIn proves a modern client that negotiates
// supports_delta=true still gets deltas and can reconstruct the board.
func TestProtocolVersionDeltaOptIn(t *testing.T) {
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

	_, payload := c.readEnvelope(t)
	if _, ok := payload.(*wordarenav1.MatchStateSnapshot); !ok {
		t.Fatalf("first frame was %T, want full snapshot", payload)
	}
	sh := c.readHello(t)
	if !sh.UseDelta {
		t.Fatalf("server negotiated use_delta=false for a supports_delta=true client")
	}

	actor := dial(t, srv, info.ID, info.Tokens[5])
	defer actor.close()
	actor.submit(t, info.ID, []uint32{9, 1, 0}, 1)
	if ev := actor.readWordEvent(t); ev.Result != wordarenav1.WordResult_ACCEPTED {
		t.Fatalf("fixture word not accepted: %v", ev.Result)
	}

	held := c.readSnapshot(t)
	var deltas int
	deadline := time.Now().Add(12 * time.Second)
	for time.Now().Before(deadline) && deltas < 2 {
		_, payload := c.readEnvelope(t)
		switch p := payload.(type) {
		case *wordarenav1.MatchStateSnapshot:
			held = p
		case *wordarenav1.MatchStateDelta:
			deltas++
			if p.BaseVersion != held.StateVersion {
				t.Fatalf("delta based on %d but client holds %d", p.BaseVersion, held.StateVersion)
			}
			held = protocol.ApplyDelta(held, p)
			if held.StateVersion != p.StateVersion {
				t.Fatalf("reconstruction ended at %d, delta said %d", held.StateVersion, p.StateVersion)
			}
			if len(held.Players) != seats {
				t.Fatalf("reconstruction has %d players, want %d", len(held.Players), seats)
			}
		case *wordarenav1.WordValidatedEvent:
		default:
			t.Fatalf("unexpected payload %T", payload)
		}
	}
	if deltas == 0 {
		t.Fatal("no delta arrived for a supports_delta=true client")
	}
}

// TestProtocolVersionLegacyUnaffected proves the legacy path is unchanged: a
// client that sends no ClientHello receives no ServerHello and is still fully
// playable (the pre-negotiation behaviour is preserved exactly).
func TestProtocolVersionLegacyUnaffected(t *testing.T) {
	api := NewAPI()
	defer api.Stop()
	srv := httptest.NewServer(api.Routes())
	defer srv.Close()

	seed := uint64(e2eSeed)
	id, tokens, _ := createMatch(t, srv, "en", &seed)
	c := dial(t, srv, id, tokens[0])
	defer c.close()

	_, payload := c.readEnvelope(t)
	if _, ok := payload.(*wordarenav1.MatchStateSnapshot); !ok {
		t.Fatalf("legacy client's first frame was %T, want full snapshot (no ServerHello)", payload)
	}
	c.submit(t, id, []uint32{9, 1, 0}, 1)
	ev := c.readWordEvent(t)
	if ev.Result != wordarenav1.WordResult_ACCEPTED {
		t.Fatalf("legacy claim not accepted: %v", ev.Result)
	}
}

var _ = websocket.MessageBinary
var _ = proto.Marshal
