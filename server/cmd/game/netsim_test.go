package main

import (
	"context"
	"fmt"
	"math/rand"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	wordarenav1 "github.com/JoTalbot/words/server/gen/wordarena/net/v1"
	"github.com/coder/websocket"
	"google.golang.org/protobuf/proto"
)

// netemClient wraps a WebSocket with application-level latency/loss:
// writes are delayed by rtt/2 (one-way), reads drop frames with p(loss).
// This emulates the network conditions of M0 acceptance 7 without touching
// loopback TCP itself.
type netemClient struct {
	conn *websocket.Conn
	rtt  time.Duration
	loss float64
	rng  *rand.Rand
}

func dialNetem(t *testing.T, srv *httptest.Server, id uint64, token string, rtt time.Duration, loss float64, rng *rand.Rand) *netemClient {
	t.Helper()
	url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/v1/match/ws?match_id=%d&token=%s"
	url = strings.Replace(url, "%d", fmtInt(id), 1)
	url = strings.Replace(url, "%s", token, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	return &netemClient{conn: conn, rtt: rtt, loss: loss, rng: rng}
}

func fmtInt(v uint64) string {
	if v == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	return string(b[i:])
}

func (c *netemClient) submitIntent(t *testing.T, id uint64, path []uint32) {
	t.Helper()
	env := &wordarenav1.ClientEnvelope{
		MatchId: id,
		Payload: &wordarenav1.ClientEnvelope_SubmitWord{
			SubmitWord: &wordarenav1.SubmitWordIntent{
				MatchId: id, LetterIndices: path,
			},
		},
	}
	b, err := proto.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	// one-way latency before the intent reaches the server
	if c.rtt > 0 {
		time.Sleep(c.rtt / 2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.conn.Write(ctx, websocket.MessageBinary, b); err != nil {
		t.Fatalf("write: %v", err)
	}
}

// readAny reads the next frame that survives the simulated loss; events and
// snapshots are both returned so callers can tolerate drops.
func (c *netemClient) readAny(t *testing.T, timeout time.Duration) (env *wordarenav1.ServerEnvelope, ok bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		remaining := time.Until(deadline)
		ctx, cancel := context.WithTimeout(context.Background(), remaining)
		typ, data, err := c.conn.Read(ctx)
		cancel()
		if err != nil {
			return nil, false
		}
		if c.rtt > 0 {
			time.Sleep(c.rtt / 2)
		}
		if typ != websocket.MessageBinary {
			continue
		}
		// Simulated packet loss: frame never arrives.
		if c.loss > 0 && c.rng.Float64() < c.loss {
			continue
		}
		var e wordarenav1.ServerEnvelope
		if err := proto.Unmarshal(data, &e); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		return &e, true
	}
	return nil, false
}

func (c *netemClient) close() { _ = c.conn.Close(websocket.StatusNormalClosure, "bye") }

// collectSnapshot reads frames until a fresh snapshot at/after minVersion.
func (c *netemClient) waitSnapshot(t *testing.T, minVersion uint32, timeout time.Duration) *wordarenav1.MatchStateSnapshot {
	t.Helper()
	for {
		env, ok := c.readAny(t, timeout)
		if !ok {
			t.Fatalf("no snapshot within %v", timeout)
		}
		if s := env.GetSnapshot(); s != nil && s.StateVersion >= minVersion {
			return s
		}
	}
}

// TestNetworkConditionsMatrix runs the M0 acceptance-7 matrix:
// RTT ∈ {50, 100, 150} ms, loss ∈ {0%, 1%, 3%}. Invariant: both clients
// converge to the identical canonical snapshot after the same intent stream,
// even when some frames are lost.
func TestNetworkConditionsMatrix(t *testing.T) {
	rtts := []time.Duration{50 * time.Millisecond, 100 * time.Millisecond, 150 * time.Millisecond}
	losses := []float64{0.0, 0.01, 0.03}
	seed := uint64(e2eSeed)

	for _, rtt := range rtts {
		for _, loss := range losses {
			t.Run(fmt.Sprintf("%s/loss%g", rtt.String(), loss), func(t *testing.T) {
				api := NewAPI()
				srv := httptest.NewServer(api.Routes())
				defer srv.Close()

				id, tokens, userIDs := createMatch(t, srv, "en", &seed)
				rng := rand.New(rand.NewSource(int64(id) * 7919))
				c0 := dialNetem(t, srv, id, tokens[0], rtt, loss, rng)
				defer c0.close()
				c1 := dialNetem(t, srv, id, tokens[1], rtt, loss, rng)
				defer c1.close()
				_ = userIDs

				// Both must eventually see a board.
				_ = c0.waitSnapshot(t, 0, 3*time.Second)
				_ = c1.waitSnapshot(t, 0, 3*time.Second)

				// seat0 claims cat (c9 a1 t0), seat1 claims dog (d11 o10 g8).
				c0.submitIntent(t, id, []uint32{9, 1, 0})
				c1.submitIntent(t, id, []uint32{11, 10, 8})
				// Nonsense word from seat0 that must be rejected server-side.
				c0.submitIntent(t, id, []uint32{0, 1, 2})

				// Give the network time to deliver everything, then read
				// canonical snapshots on both sides until they report the
				// same score profile.
				deadline := time.Now().Add(8 * time.Second)
				var s0, s1 *wordarenav1.MatchStateSnapshot
				for time.Now().Before(deadline) {
					s0 = c0.waitSnapshot(t, 1, 2*time.Second)
					s1 = c1.waitSnapshot(t, 1, 2*time.Second)
					if s0.Players[0].Score == 5 && s1.Players[1].Score == 5 &&
						s0.StateVersion == s1.StateVersion && s0.ServerTick == s1.ServerTick {
						break
					}
					time.Sleep(200 * time.Millisecond)
				}

				if s0 == nil || s1 == nil {
					t.Fatal("no converging snapshots")
				}
				if s0.Players[0].Score != 5 {
					t.Fatalf("seat0 score %d, want 5", s0.Players[0].Score)
				}
				if s1.Players[1].Score != 5 {
					t.Fatalf("seat1 score %d, want 5", s1.Players[1].Score)
				}
				if s0.StateVersion != s1.StateVersion || s0.ServerTick != s1.ServerTick {
					t.Fatalf("clients diverged: ver %d/%d tick %d/%d",
						s0.StateVersion, s1.StateVersion, s0.ServerTick, s1.ServerTick)
				}
				for i := range s0.Cells {
					if s0.Cells[i].OwnerUserId != s1.Cells[i].OwnerUserId {
						t.Fatalf("cell %d ownership diverged under loss %v rtt %v", i, loss, rtt)
					}
				}
			})
		}
	}
}

