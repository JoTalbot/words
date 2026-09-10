package protocol

// Batch 16D: protocol robustness fuzz targets.
//
// These targets exercise the same client-message decode path the live server
// uses (proto.Unmarshal of ClientEnvelope followed by EnvelopeToIntent, see
// server/cmd/game/api.go). They assert the server never panics and behaves
// deterministically on arbitrary/malformed wire bytes, which is a core
// anti-cheat and availability property for an authoritative match service.
//
// CI runs each target against its seed corpus as a normal test. To search for
// crashes locally, run for example:
//
//	cd server && go test ./internal/protocol -run '^$' -fuzz FuzzClientEnvelopeDecode -fuzztime 30s

import (
	"bytes"
	"testing"

	wordarenav1 "github.com/JoTalbot/words/server/gen/wordarena/net/v1"
	"github.com/JoTalbot/words/server/internal/match"
	"google.golang.org/protobuf/proto"
)

func validSubmitEnvelopeBytes(matchID uint64, seq uint32, indices ...uint32) []byte {
	env := &wordarenav1.ClientEnvelope{
		MatchId: matchID,
		Payload: &wordarenav1.ClientEnvelope_SubmitWord{
			SubmitWord: &wordarenav1.SubmitWordIntent{
				MatchId:        matchID,
				ClientSequence: seq,
				LetterIndices:  indices,
			},
		},
	}
	data, err := proto.Marshal(env)
	if err != nil {
		panic(err)
	}
	return data
}

func seedClientEnvelopeCorpus() [][]byte {
	return [][]byte{
		{},
		{0x00},
		{0xff, 0xff, 0xff, 0xff, 0xff},
		validSubmitEnvelopeBytes(1, 1, 0, 1, 2),
		validSubmitEnvelopeBytes(7, 3, 0, 1, 2, 3, 4, 5),
		validSubmitEnvelopeBytes(1, 0),                       // empty path
		validSubmitEnvelopeBytes(1, 9, 0xFFFFFFFF, 0x80000000), // large indices
		// A resume payload instead of a submit.
		func() []byte {
			env := &wordarenav1.ClientEnvelope{
				MatchId: 2,
				Payload: &wordarenav1.ClientEnvelope_Resume{Resume: &wordarenav1.ResumeRequest{}},
			}
			data, err := proto.Marshal(env)
			if err != nil {
				panic(err)
			}
			return data
		}(),
	}
}

// FuzzClientEnvelopeDecode feeds arbitrary bytes through the server decode
// path: protobuf unmarshal followed by EnvelopeToIntent for both seats. It
// must never panic, and for any input that parses, EnvelopeToIntent must be
// deterministic (same input -> same output).
func FuzzClientEnvelopeDecode(f *testing.F) {
	for _, seed := range seedClientEnvelopeCorpus() {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		var env wordarenav1.ClientEnvelope
		if err := proto.Unmarshal(data, &env); err != nil {
			// Malformed protobuf must be rejected cleanly, not panic.
			return
		}

		for _, seat := range []match.Seat{0, 1} {
			first, firstErr := EnvelopeToIntent(&env, seat)
			second, secondErr := EnvelopeToIntent(&env, seat)

			if (firstErr == nil) != (secondErr == nil) {
				t.Fatalf("EnvelopeToIntent error not deterministic: %v vs %v", firstErr, secondErr)
			}
			if firstErr != nil {
				continue
			}
			if len(first) != len(second) {
				t.Fatalf("EnvelopeToIntent length not deterministic: %d vs %d", len(first), len(second))
			}
			for i := range first {
				if first[i] != second[i] {
					t.Fatalf("EnvelopeToIntent value not deterministic at %d", i)
				}
			}
		}
	})
}

// FuzzClientEnvelopeMutation starts from a known-good submit envelope, applies
// byte-level mutations, and confirms the decode path stays total (no panic)
// and any successful parse round-trips back to identical intent bytes.
func FuzzClientEnvelopeMutation(f *testing.F) {
	base := validSubmitEnvelopeBytes(42, 5, 0, 1, 2, 3)
	f.Add(base, uint(0), byte(0))
	f.Add(base, uint(3), byte(0xFF))
	f.Add(base, uint(10), byte(0x01))

	f.Fuzz(func(t *testing.T, orig []byte, pos uint, val byte) {
		if len(orig) == 0 {
			return
		}
		mut := make([]byte, len(orig))
		copy(mut, orig)
		mut[int(pos)%len(mut)] = val

		var env wordarenav1.ClientEnvelope
		if err := proto.Unmarshal(mut, &env); err != nil {
			return
		}

		ids, err := EnvelopeToIntent(&env, 0)
		if err != nil {
			return
		}

		// Re-encode the parsed intent and decode again; intent bytes must be
		// stable for a given parse.
		reEnv := &wordarenav1.ClientEnvelope{
			MatchId: env.MatchId,
			Payload: &wordarenav1.ClientEnvelope_SubmitWord{
				SubmitWord: &wordarenav1.SubmitWordIntent{
					MatchId:        env.MatchId,
					ClientSequence: env.GetSubmitWord().GetClientSequence(),
					LetterIndices:  uint32sFromInts(ids),
				},
			},
		}
		reBytes, err := proto.Marshal(reEnv)
		if err != nil {
			t.Fatalf("re-marshal failed: %v", err)
		}
		var reDec wordarenav1.ClientEnvelope
		if err := proto.Unmarshal(reBytes, &reDec); err != nil {
			t.Fatalf("re-decode failed: %v", err)
		}
		reIDs, err := EnvelopeToIntent(&reDec, 0)
		if err != nil {
			t.Fatalf("re-decoded intent rejected: %v", err)
		}
		if len(reIDs) != len(ids) {
			t.Fatalf("intent length changed across round-trip")
		}
		for i := range ids {
			if ids[i] != reIDs[i] {
				t.Fatalf("intent value changed across round-trip at %d", i)
			}
		}
	})
}

// FuzzSnapshotEncodeDecode asserts the server->client snapshot envelope path
// is lossless: encoding a snapshot and decoding it must reproduce the same
// match id, version, over flag and cell count.
func FuzzSnapshotEncodeDecode(f *testing.F) {
	f.Add(uint64(1), uint32(0), uint32(0), false, 12)
	f.Add(uint64(99), uint32(7), uint32(300), true, 0)
	f.Add(uint64(5), uint32(255), uint32(1), false, 12)

	f.Fuzz(func(t *testing.T, matchID uint64, version uint32, tick uint32, over bool, cellCount int) {
		if cellCount < 0 || cellCount > 64 {
			return
		}
		phase := "active"
		if over {
			phase = "over"
		}
		snap := match.Snapshot{
			MatchID:      matchID,
			StateVersion: int(version),
			ServerTick:   int(tick),
			Phase:        phase,
		}
		for i := 0; i < cellCount; i++ {
			snap.Cells = append(snap.Cells, match.CellView{
				ID:        i,
				Letter:    string(rune('A' + i%10)),
				OwnerSeat: -1,
			})
		}

		env := SnapshotEnvelope(matchID, SnapshotToProto(snap, [2]uint64{11, 22}))
		out, err := proto.Marshal(env)
		if err != nil {
			t.Fatalf("snapshot marshal failed: %v", err)
		}
		var dec wordarenav1.ServerEnvelope
		if err := proto.Unmarshal(out, &dec); err != nil {
			t.Fatalf("snapshot unmarshal failed: %v", err)
		}
		got := dec.GetSnapshot()
		if got == nil {
			t.Fatalf("decoded envelope lost snapshot payload")
		}
		if got.GetMatchId() != matchID {
			t.Fatalf("match id mismatch: %d vs %d", got.GetMatchId(), matchID)
		}
		if got.GetStateVersion() != version {
			t.Fatalf("state version mismatch")
		}
		if got.GetOver() != over {
			t.Fatalf("over flag mismatch")
		}
		if len(got.GetCells()) != cellCount {
			t.Fatalf("cell count mismatch: %d vs %d", len(got.GetCells()), cellCount)
		}
		if !bytes.Equal(out, mustMarshal(t, env)) {
			t.Fatalf("snapshot encoding not deterministic")
		}
	})
}

func uint32sFromInts(in []int) []uint32 {
	out := make([]uint32, len(in))
	for i, v := range in {
		out[i] = uint32(v)
	}
	return out
}

func mustMarshal(t *testing.T, m proto.Message) []byte {
	t.Helper()
	out, err := proto.Marshal(m)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	return out
}
