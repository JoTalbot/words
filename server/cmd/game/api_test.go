package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	wordarenav1 "github.com/JoTalbot/words/server/gen/wordarena/net/v1"
	"github.com/coder/websocket"
	"google.golang.org/protobuf/proto"
)

// e2eSeed produces a deterministic board with claimable "cat" and "dog"
// (found by scan: board "taandsvtgcod").
const e2eSeed = 1512

type testClient struct {
	conn *websocket.Conn
	seat uint64
	user uint64
}

func createMatch(t *testing.T, srv *httptest.Server, lang string, seed *uint64) (id uint64, tokens [2]string, userIDs [2]uint64) {
	t.Helper()
	body := fmt.Sprintf(`{"language":%q`, lang)
	if seed != nil {
		body += fmt.Sprintf(`,"seed":%d`, *seed)
	}
	body += `}`
	resp, err := http.Post(srv.URL+"/v1/matches", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("create match: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status %d", resp.StatusCode)
	}
	var out createMatchResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.MatchID == 0 || out.Tokens[0] == "" || out.Tokens[1] == "" {
		t.Fatalf("bad create response %+v", out)
	}
	if out.Language != lang {
		t.Fatalf("language %s", out.Language)
	}
	return out.MatchID, out.Tokens, out.UserIDs
}

func dial(t *testing.T, srv *httptest.Server, id uint64, token string) *testClient {
	t.Helper()
	url := fmt.Sprintf("%s/v1/match/ws?match_id=%d&token=%s", strings.TrimPrefix(srv.URL, "http"), id, token)
	// httptest server URL is http; coder/websocket needs ws scheme
	url = "ws" + strings.TrimPrefix(srv.URL, "http") + fmt.Sprintf("/v1/match/ws?match_id=%d&token=%s", id, token)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	return &testClient{conn: conn}
}

func (c *testClient) close() { _ = c.conn.Close(websocket.StatusNormalClosure, "test") }

// readEnvelope reads one server envelope; the payload is returned.
func (c *testClient) readEnvelope(t *testing.T) (*wordarenav1.ServerEnvelope, proto.Message) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	typ, data, err := c.conn.Read(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if typ != websocket.MessageBinary {
		t.Fatalf("frame type %v", typ)
	}
	var env wordarenav1.ServerEnvelope
	if err := proto.Unmarshal(data, &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if env.GetWordEvent() != nil {
		return &env, env.GetWordEvent()
	}
	if env.GetSnapshot() != nil {
		return &env, env.GetSnapshot()
	}
	t.Fatalf("unknown envelope %s", env.String())
	return nil, nil
}

func (c *testClient) readSnapshot(t *testing.T) *wordarenav1.MatchStateSnapshot {
	t.Helper()
	for {
		_, payload := c.readEnvelope(t)
		if snap, ok := payload.(*wordarenav1.MatchStateSnapshot); ok {
			return snap
		}
	}
}

func (c *testClient) readWordEvent(t *testing.T) *wordarenav1.WordValidatedEvent {
	t.Helper()
	for {
		_, payload := c.readEnvelope(t)
		if ev, ok := payload.(*wordarenav1.WordValidatedEvent); ok {
			return ev
		}
	}
}

// submit sends a word path as a client intent.
func (c *testClient) submit(t *testing.T, id uint64, path []uint32, seq uint32) {
	t.Helper()
	env := &wordarenav1.ClientEnvelope{
		MatchId: id,
		Payload: &wordarenav1.ClientEnvelope_SubmitWord{
			SubmitWord: &wordarenav1.SubmitWordIntent{
				MatchId: id, ClientSequence: seq, LetterIndices: path,
				ClientTimestampMs: 0, // telemetry-only field; 0 is fine in tests
			},
		},
	}
	b, err := proto.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.conn.Write(ctx, websocket.MessageBinary, b); err != nil {
		t.Fatalf("write: %v", err)
	}
}

// boardLetters maps cell letter string for the deterministic test seed.
func TestEndToEndTwoClients(t *testing.T) {
	api := NewAPI()
	srv := httptest.NewServer(api.Routes())
	defer srv.Close()

	seed := uint64(e2eSeed)
	id, tokens, userIDs := createMatch(t, srv, "en", &seed)

	c0 := dial(t, srv, id, tokens[0])
	defer c0.close()
	c1 := dial(t, srv, id, tokens[1])
	defer c1.close()
	c0.seat, c0.user = 0, userIDs[0]
	c1.seat, c1.user = 1, userIDs[1]

	// 1. Both clients receive the same deterministic board.
	s0 := c0.readSnapshot(t)
	s1 := c1.readSnapshot(t)
	if s0.MatchId != id || s1.MatchId != id {
		t.Fatalf("match ids %d/%d", s0.MatchId, s1.MatchId)
	}
	if len(s0.Cells) != 12 || len(s1.Cells) != 12 {
		t.Fatalf("cells %d/%d", len(s0.Cells), len(s1.Cells))
	}
	for i := range s0.Cells {
		if s0.Cells[i].Letter != s1.Cells[i].Letter {
			t.Fatalf("clients disagree on cell %d: %s vs %s", i, s0.Cells[i].Letter, s1.Cells[i].Letter)
		}
	}
	// seed 1512 board: t a a n d s v t g c o d
	want := "taandsvtgcod"
	var got []string
	for _, c := range s0.Cells {
		got = append(got, c.Letter)
	}
	if strings.Join(got, "") != want {
		t.Fatalf("board %v, want %s", got, want)
	}

	// 2. seat0 claims "cat": cells c(10) a(1) t(0) -> [10,1,0]; both clients
	//    observe the identical accepted event.
	c0.submit(t, id, []uint32{9, 1, 0}, 1)
	ev0 := c0.readWordEvent(t)
	ev1 := c1.readWordEvent(t)
	if ev0.Result != wordarenav1.WordResult_ACCEPTED || ev1.Result != wordarenav1.WordResult_ACCEPTED {
		t.Fatalf("results %v/%v", ev0.Result, ev1.Result)
	}
	if ev0.NormalizedWord != "cat" || ev1.NormalizedWord != "cat" {
		t.Fatalf("words %q/%q", ev0.NormalizedWord, ev1.NormalizedWord)
	}
	if ev0.ScoreAdded != 5 || ev1.ScoreAdded != 5 {
		t.Fatalf("score_added %d/%d", ev0.ScoreAdded, ev1.ScoreAdded)
	}
	if ev0.StateVersion != ev1.StateVersion || ev0.StateVersion == 0 {
		t.Fatalf("state versions %d/%d", ev0.StateVersion, ev1.StateVersion)
	}
	if ev0.UserId != userIDs[0] {
		t.Fatalf("event user_id = %d, want %d", ev0.UserId, userIDs[0])
	}
	if ev0.IsSteal {
		t.Fatal("cat must not be a steal")
	}

	// 3. seat1 claims "dog": d(11) o(10) g(8). Both see it.
	c1.submit(t, id, []uint32{11, 10, 8}, 1)
	e0 := c0.readWordEvent(t)
	e1 := c1.readWordEvent(t)
	for _, e := range []*wordarenav1.WordValidatedEvent{e0, e1} {
		if e.Result != wordarenav1.WordResult_ACCEPTED || e.NormalizedWord != "dog" || e.ScoreAdded != 5 {
			t.Fatalf("dog event %+v", e)
		}
		if e.UserId != userIDs[1] {
			t.Fatalf("dog user %d", e.UserId)
		}
	}

	// 4. A nonsense submission is rejected identically for both clients.
	c0.submit(t, id, []uint32{0, 1, 2}, 2) // t,a,a -> not a word
	r0 := c0.readWordEvent(t)
	r1 := c1.readWordEvent(t)
	if r0.Result != wordarenav1.WordResult_REJECTED_NOT_IN_DICT || r1.Result != r0.Result {
		t.Fatalf("reject results %v/%v", r0.Result, r1.Result)
	}

	// 5. Snapshot convergence after ~1.5 s of live ticking: both clients see
	//    the same canonical cell ownership and scores.
	time.Sleep(1600 * time.Millisecond)
	f0 := c0.readSnapshot(t)
	f1 := c1.readSnapshot(t)
	if f0.StateVersion != f1.StateVersion || f0.ServerTick != f1.ServerTick {
		t.Fatalf("convergence mismatch ver=%d/%d tick=%d/%d", f0.StateVersion, f1.StateVersion, f0.ServerTick, f1.ServerTick)
	}
	for i := range f0.Cells {
		if f0.Cells[i].OwnerUserId != f1.Cells[i].OwnerUserId || f0.Cells[i].IsLocked != f1.Cells[i].IsLocked {
			t.Fatalf("cell %d ownership diverged", i)
		}
	}
	if f0.Players[0].Score != 5 || f0.Players[1].Score != 5 {
		t.Fatalf("scores %d/%d", f0.Players[0].Score, f0.Players[1].Score)
	}
}

func TestReconnectWithinGrace(t *testing.T) {
	api := NewAPI()
	srv := httptest.NewServer(api.Routes())
	defer srv.Close()

	seed := uint64(e2eSeed)
	id, tokens, _ := createMatch(t, srv, "en", &seed)

	// seat0 plays a claim, then disconnects.
	c0 := dial(t, srv, id, tokens[0])
	defer c0.close()
	snap := c0.readSnapshot(t)
	c0.submit(t, id, []uint32{9, 1, 0}, 1)
	ev := c0.readWordEvent(t)
	if ev.Result != wordarenav1.WordResult_ACCEPTED {
		t.Fatalf("claim failed %v", ev.Result)
	}
	verBefore := ev.StateVersion
	_ = snap
	c0.close()

	// Let the match advance while disconnected (ticker keeps running).
	time.Sleep(1500 * time.Millisecond)

	// Reconnect with the same token: canonical snapshot must reflect the
	// claim made before disconnection (resume semantics).
	c1 := dial(t, srv, id, tokens[0])
	defer c1.close()
	resumed := c1.readSnapshot(t)
	if resumed.StateVersion < verBefore {
		t.Fatalf("resumed state version %d < pre-disconnect %d", resumed.StateVersion, verBefore)
	}
	if resumed.ServerTick == 0 {
		t.Fatal("tick did not advance")
	}
	// The claim persists in canonical state.
	if resumed.Players[0].Score != 5 {
		t.Fatalf("score after resume %d, want 5", resumed.Players[0].Score)
	}
	owned := 0
	for _, c := range resumed.Cells {
		if c.OwnerUserId != 0 {
			owned++
		}
	}
	if owned != 3 {
		t.Fatalf("owned cells after resume = %d, want 3", owned)
	}
}

func TestWSRejectsBadToken(t *testing.T) {
	api := NewAPI()
	srv := httptest.NewServer(api.Routes())
	defer srv.Close()

	seed := uint64(1)
	id, _, _ := createMatch(t, srv, "en", &seed)
	url := "ws" + strings.TrimPrefix(srv.URL, "http") + fmt.Sprintf("/v1/match/ws?match_id=%d&token=wrong", id)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _, err := websocket.Dial(ctx, url, nil)
	if err == nil {
		t.Fatal("expected handshake rejection for bad token")
	}
}

func TestCreateMatchBadLanguage(t *testing.T) {
	api := NewAPI()
	srv := httptest.NewServer(api.Routes())
	defer srv.Close()
	bad := uint64(99)
	resp, err := http.Post(srv.URL+"/v1/matches", "application/json", strings.NewReader(`{"language":"fr"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", resp.StatusCode)
	}
	_ = bad
}

func TestHealthzOnAPI(t *testing.T) {
	api := NewAPI()
	srv := httptest.NewServer(api.Routes())
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("healthz %d", resp.StatusCode)
	}
}
