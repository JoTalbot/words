package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// M2 batch 32G: the guild foundation's guarantees, each one a test.
//
// The interesting cases are the refusals, not the happy path: the design's whole
// point is that a caller cannot act as another player, cannot hold two guilds,
// cannot take a name someone else has, and cannot leave a guild headless. Those
// are the properties the durable row depends on, so they are asserted through
// the real HTTP surface rather than by calling the store directly.

// guildAPI builds an API with in-memory storage and its test server.
func guildAPI(t *testing.T) (*API, *httptest.Server) {
	t.Helper()
	api := NewAPI()
	api.maxSeats = 2
	t.Cleanup(api.Stop)
	srv := httptest.NewServer(api.Routes())
	t.Cleanup(srv.Close)
	return api, srv
}

// newPlayer creates a profile and returns its id and owner token.
func newPlayer(t *testing.T, srv *httptest.Server, nickname string) (uint64, string) {
	t.Helper()
	code, body := postRaw(t, srv, "/v1/players",
		fmt.Sprintf(`{"nickname":%q,"language":"en"}`, nickname), nil)
	if code != http.StatusCreated {
		t.Fatalf("create player %s: status %d body %s", nickname, code, body)
	}
	var out struct {
		ID         uint64 `json:"id"`
		OwnerToken string `json:"owner_token"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("decode profile: %v (%s)", err, body)
	}
	if out.ID == 0 || out.OwnerToken == "" {
		t.Fatalf("profile response is missing id or owner token: %s", body)
	}
	if len(out.OwnerToken) != 32 {
		t.Fatalf("owner token %q is not a 128-bit hex string", out.OwnerToken)
	}
	return out.ID, out.OwnerToken
}

// guildReq performs a guild call with an optional owner token.
func guildReq(t *testing.T, srv *httptest.Server, method, path, body, token string) (int, string) {
	t.Helper()
	hdr := map[string]string{}
	if token != "" {
		hdr["X-Player-Token"] = token
	}
	switch method {
	case http.MethodPost:
		return postRaw(t, srv, path, body, hdr)
	case http.MethodDelete:
		req, err := http.NewRequest(http.MethodDelete, srv.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return resp.StatusCode, string(msg)
	case http.MethodGet:
		code, respBody, _ := getRaw(t, srv, path, hdr)
		return code, respBody
	default:
		t.Fatalf("unsupported method %s", method)
		return 0, ""
	}
}

func createGuild(t *testing.T, srv *httptest.Server, token, name, tag string) uint64 {
	t.Helper()
	code, body := guildReq(t, srv, http.MethodPost, "/v1/guilds",
		fmt.Sprintf(`{"name":%q,"tag":%q,"language":"en"}`, name, tag), token)
	if code != http.StatusCreated {
		t.Fatalf("create guild %s: status %d body %s", name, code, body)
	}
	var g Guild
	if err := json.Unmarshal([]byte(body), &g); err != nil {
		t.Fatalf("decode guild: %v (%s)", err, body)
	}
	if g.ID == 0 || g.MemberCount != 1 {
		t.Fatalf("a fresh guild should exist with its founder in the roster: %s", body)
	}
	return g.ID
}

func TestGuildLifecycleCreateJoinLeave(t *testing.T) {
	_, srv := guildAPI(t)
	founderID, founderTok := newPlayer(t, srv, "founder")
	memberID, memberTok := newPlayer(t, srv, "member")

	guildID := createGuild(t, srv, founderTok, "night-owls", "OWL")

	// Join: open in this slice, and the identity is the token.
	code, body := guildReq(t, srv, http.MethodPost, fmt.Sprintf("/v1/guilds/%d/members", guildID), "", memberTok)
	if code != http.StatusOK {
		t.Fatalf("join: status %d body %s", code, body)
	}
	var d GuildDetail
	if err := json.Unmarshal([]byte(body), &d); err != nil {
		t.Fatalf("decode join response: %v (%s)", err, body)
	}
	if len(d.Members) != 2 || d.Guild.MemberCount != 2 {
		t.Fatalf("roster should hold both players: %s", body)
	}
	if d.Members[0].PlayerID != founderID || d.Members[0].Role != guildRoleOwner {
		t.Fatalf("the founder should be first and own the guild: %s", body)
	}
	if d.Members[1].PlayerID != memberID || d.Members[1].Role != guildRoleMember {
		t.Fatalf("the joiner should be a plain member: %s", body)
	}
	if d.Members[0].Nickname != "founder" || d.Members[1].Nickname != "member" {
		t.Fatalf("roster should carry display names: %s", body)
	}

	// The membership is durable through the store, and readable without a token.
	code, body = guildReq(t, srv, http.MethodGet, fmt.Sprintf("/v1/players/%d/guild", memberID), "", "")
	if code != http.StatusOK || !strings.Contains(body, `"role":"member"`) {
		t.Fatalf("player guild lookup: status %d body %s", code, body)
	}
	code, body = guildReq(t, srv, http.MethodGet, fmt.Sprintf("/v1/guilds/%d", guildID), "", "")
	if code != http.StatusOK || !strings.Contains(body, "night-owls") {
		t.Fatalf("guild read: status %d body %s", code, body)
	}

	// Leave: the roster shrinks and the player is guildless again.
	code, body = guildReq(t, srv, http.MethodDelete, fmt.Sprintf("/v1/guilds/%d/members/%d", guildID, memberID), "", memberTok)
	if code != http.StatusOK {
		t.Fatalf("leave: status %d body %s", code, body)
	}
	code, _ = guildReq(t, srv, http.MethodGet, fmt.Sprintf("/v1/players/%d/guild", memberID), "", "")
	if code != http.StatusNotFound {
		t.Fatalf("a player who left should have no guild, got status %d", code)
	}
}

func TestGuildMutationsRequireAPlayerToken(t *testing.T) {
	_, srv := guildAPI(t)
	_, founderTok := newPlayer(t, srv, "founder")
	_, otherTok := newPlayer(t, srv, "other")
	guildID := createGuild(t, srv, founderTok, "owls-nest", "NEST")

	cases := []struct {
		name   string
		method string
		path   string
		body   string
		token  string
	}{
		{"create without a token", http.MethodPost, "/v1/guilds", `{"name":"x-team","tag":"XX"}`, ""},
		{"create with a bogus token", http.MethodPost, "/v1/guilds", `{"name":"x-team","tag":"XX"}`, strings.Repeat("a", 32)},
		{"join without a token", http.MethodPost, fmt.Sprintf("/v1/guilds/%d/members", guildID), "", ""},
		{"leave without a token", http.MethodDelete, fmt.Sprintf("/v1/guilds/%d/members/1", guildID), "", ""},
		{"create with a malformed token", http.MethodPost, "/v1/guilds", `{"name":"x-team","tag":"XX"}`, "short"},
	}
	for _, c := range cases {
		code, body := guildReq(t, srv, c.method, c.path, c.body, c.token)
		if code != http.StatusUnauthorized {
			t.Errorf("%s: status %d, want 401 (body %s)", c.name, code, body)
			continue
		}
		if !strings.Contains(strings.ToLower(body), "token") {
			t.Errorf("%s: the refusal should name the token requirement: %s", c.name, body)
		}
	}

	// A valid token that belongs to someone else is still just "a player": it
	// cannot remove a member it does not own.
	code, body := guildReq(t, srv, http.MethodDelete,
		fmt.Sprintf("/v1/guilds/%d/members/%d", guildID, 1), "", otherTok)
	if code != http.StatusForbidden {
		t.Fatalf("a non-owner removed a member: status %d body %s", code, body)
	}
}

// TestGuildOneGuildPerPlayer is invariant 1, asserted through the surface a
// client actually uses.
func TestGuildOneGuildPerPlayer(t *testing.T) {
	_, srv := guildAPI(t)
	_, aTok := newPlayer(t, srv, "alpha")
	_, bTok := newPlayer(t, srv, "beta")
	first := createGuild(t, srv, aTok, "first-guild", "FST")
	second := createGuild(t, srv, bTok, "second-guild", "SND")

	code, body := guildReq(t, srv, http.MethodPost, fmt.Sprintf("/v1/guilds/%d/members", second), "", aTok)
	if code != http.StatusConflict {
		t.Fatalf("a player joined a second guild: status %d body %s", code, body)
	}
	if !strings.Contains(body, "one guild") {
		t.Fatalf("the refusal should explain the one-guild rule: %s", body)
	}

	// Joining twice is the same refusal, not a second row.
	code, body = guildReq(t, srv, http.MethodPost, fmt.Sprintf("/v1/guilds/%d/members", first), "", aTok)
	if code != http.StatusConflict {
		t.Fatalf("a player joined their own guild twice: status %d body %s", code, body)
	}
}

// TestGuildOwnershipIsNeverOrphaned is invariant 4: the case that would
// otherwise produce a guild nobody can administer, or one that outlives its
// last member and squats on a name.
func TestGuildOwnershipIsNeverOrphaned(t *testing.T) {
	_, srv := guildAPI(t)
	founderID, founderTok := newPlayer(t, srv, "owner")
	firstID, firstTok := newPlayer(t, srv, "first")
	secondID, secondTok := newPlayer(t, srv, "second")

	guildID := createGuild(t, srv, founderTok, "handover-club", "HND")
	for _, tok := range []string{firstTok, secondTok} {
		if code, body := guildReq(t, srv, http.MethodPost, fmt.Sprintf("/v1/guilds/%d/members", guildID), "", tok); code != http.StatusOK {
			t.Fatalf("join: status %d body %s", code, body)
		}
	}

	// The founder leaves: ownership moves to the earliest-joined member.
	code, body := guildReq(t, srv, http.MethodDelete, fmt.Sprintf("/v1/guilds/%d/members/%d", guildID, founderID), "", founderTok)
	if code != http.StatusOK {
		t.Fatalf("founder leave: status %d body %s", code, body)
	}
	var d GuildDetail
	if err := json.Unmarshal([]byte(body), &d); err != nil {
		t.Fatalf("decode: %v (%s)", err, body)
	}
	if len(d.Members) != 2 {
		t.Fatalf("two members should remain: %s", body)
	}
	if d.Members[0].PlayerID != firstID || d.Members[0].Role != guildRoleOwner {
		t.Fatalf("ownership should pass to the earliest-joined member: %s", body)
	}

	// The new owner can remove the other member, and may not remove themselves.
	if code, body := guildReq(t, srv, http.MethodDelete, fmt.Sprintf("/v1/guilds/%d/members/%d", guildID, secondID), "", firstTok); code != http.StatusOK {
		t.Fatalf("owner remove: status %d body %s", code, body)
	}
	if code, _ := guildReq(t, srv, http.MethodDelete, fmt.Sprintf("/v1/guilds/%d/members/%d", guildID, firstID), "", firstTok); code != http.StatusOK {
		t.Fatalf("self-removal through the member path should be the leave operation, got %d", code)
	}

	// The last member has left, so the guild is gone and its name is free.
	if code, _ := guildReq(t, srv, http.MethodGet, fmt.Sprintf("/v1/guilds/%d", guildID), "", ""); code != http.StatusNotFound {
		t.Fatalf("an emptied guild should cease to exist, got status %d", code)
	}
	if got := createGuild(t, srv, secondTok, "handover-club", "HND"); got == 0 {
		t.Fatal("the freed name and tag should be reusable")
	}
}

func TestGuildNameAndTagRules(t *testing.T) {
	_, srv := guildAPI(t)
	_, tok := newPlayer(t, srv, "namer")
	_, tok2 := newPlayer(t, srv, "namer2")

	bad := []struct{ name, tag, why string }{
		{"ab", "AB", "too short"},
		{strings.Repeat("n", 25), "AB", "too long"},
		{"-leading", "AB", "leading separator"},
		{"trailing-", "AB", "trailing separator"},
		{"double--dash", "AB", "separator run"},
		{"has space", "AB", "space"},
		{"control\x01char", "AB", "control character"},
		{"valid-name", "A", "tag too short"},
		{"valid-name", "TOOLONG", "tag too long"},
		{"valid-name", "a-1", "tag with a separator"},
	}
	for _, c := range bad {
		code, body := guildReq(t, srv, http.MethodPost, "/v1/guilds",
			fmt.Sprintf(`{"name":%q,"tag":%q}`, c.name, c.tag), tok)
		if code != http.StatusBadRequest {
			t.Errorf("name %q tag %q (%s): status %d, want 400 (body %s)", c.name, c.tag, c.why, code, body)
		}
	}

	createGuild(t, srv, tok, "valid-name", "VAL")

	// A lowercase tag is normalised rather than refused: the client is not
	// wrong to type it, and the uniqueness key is the uppercase form.
	{
		_, lcTok := newPlayer(t, srv, "lowercase-tagger")
		code, body := guildReq(t, srv, http.MethodPost, "/v1/guilds", `{"name":"lower-tag-club","tag":"lo"}`, lcTok)
		if code != http.StatusCreated {
			t.Fatalf("a lowercase tag should be accepted and normalised: status %d body %s", code, body)
		}
		if !strings.Contains(body, `"tag":"LO"`) {
			t.Fatalf("the tag was not normalised to uppercase: %s", body)
		}
	}

	// Case-insensitive uniqueness for the name, and exact uniqueness for the tag.
	code, body := guildReq(t, srv, http.MethodPost, "/v1/guilds", `{"name":"VALID-NAME","tag":"OTHER"}`, tok2)
	if code != http.StatusConflict {
		t.Fatalf("a name differing only in case was accepted: status %d body %s", code, body)
	}
	code, body = guildReq(t, srv, http.MethodPost, "/v1/guilds", `{"name":"another-name","tag":"VAL"}`, tok2)
	if code != http.StatusConflict {
		t.Fatalf("a duplicate tag was accepted: status %d body %s", code, body)
	}
}

// TestGuildIsFullRefusesJoin keeps the roster bounded: without it a guild is an
// unbounded fan-out target for anything built on top of it later.
func TestGuildIsFullRefusesJoin(t *testing.T) {
	oldCap := guildMaxMembers
	guildMaxMembers = 3
	t.Cleanup(func() { guildMaxMembers = oldCap })

	_, srv := guildAPI(t)
	_, founderTok := newPlayer(t, srv, "capowner")
	guildID := createGuild(t, srv, founderTok, "small-club", "SML")

	for i := 0; i < 2; i++ {
		_, tok := newPlayer(t, srv, fmt.Sprintf("cap%d", i))
		if code, body := guildReq(t, srv, http.MethodPost, fmt.Sprintf("/v1/guilds/%d/members", guildID), "", tok); code != http.StatusOK {
			t.Fatalf("join %d: status %d body %s", i, code, body)
		}
	}
	_, extraTok := newPlayer(t, srv, "cap-extra")
	code, body := guildReq(t, srv, http.MethodPost, fmt.Sprintf("/v1/guilds/%d/members", guildID), "", extraTok)
	if code != http.StatusConflict || !strings.Contains(body, "full") {
		t.Fatalf("a full guild accepted another member: status %d body %s", code, body)
	}
}

// TestGuildJoinAndReadRefusalsAreSpecific: a client must be able to tell "that
// guild does not exist" from "you are already in one" and "nobody is in that
// guild".
func TestGuildJoinAndReadRefusalsAreSpecific(t *testing.T) {
	_, srv := guildAPI(t)
	loneID, loneTok := newPlayer(t, srv, "lonely")
	_, otherTok := newPlayer(t, srv, "other")

	code, body := guildReq(t, srv, http.MethodPost, "/v1/guilds/9999/members", "", otherTok)
	if code != http.StatusNotFound || !strings.Contains(body, "guild not found") {
		t.Fatalf("joining a missing guild: status %d body %s", code, body)
	}
	code, body = guildReq(t, srv, http.MethodGet, "/v1/guilds/9999", "", "")
	if code != http.StatusNotFound {
		t.Fatalf("reading a missing guild: status %d body %s", code, body)
	}
	code, body = guildReq(t, srv, http.MethodGet, fmt.Sprintf("/v1/players/%d/guild", loneID), "", "")
	if code != http.StatusNotFound || !strings.Contains(body, "not in a guild") {
		t.Fatalf("a guildless player: status %d body %s", code, body)
	}
	code, body = guildReq(t, srv, http.MethodDelete, fmt.Sprintf("/v1/guilds/9999/members/%d", loneID), "", loneTok)
	if code != http.StatusNotFound {
		t.Fatalf("leaving a missing guild: status %d body %s", code, body)
	}
}

// TestLegacyProfileCannotClaimAGuild pins the decision in docs/M2-GUILDS.md:
// profiles created before owner tokens existed have no token, and there is no
// endpoint that would mint one on demand, because that would let any caller
// claim an unclaimed id.
func TestLegacyProfileCannotClaimAGuild(t *testing.T) {
	api, srv := guildAPI(t)

	// A profile created directly through the store, the way a pre-batch
	// deployment would have made one: no owner token.
	legacy, err := api.profiles.Create("legacy", "en")
	if err != nil {
		t.Fatal(err)
	}
	for _, tok := range []string{"", strings.Repeat("b", 32), strings.Repeat("c", 30)} {
		code, body := guildReq(t, srv, http.MethodPost, "/v1/guilds", `{"name":"legacy-guild","tag":"LGC"}`, tok)
		if code != http.StatusUnauthorized {
			t.Fatalf("legacy claim with token %q: status %d body %s", tok, code, body)
		}
	}
	// And the legacy profile is not in any guild.
	code, _ := guildReq(t, srv, http.MethodGet, fmt.Sprintf("/v1/players/%d/guild", legacy.ID), "", "")
	if code != http.StatusNotFound {
		t.Fatalf("a legacy profile appears to be in a guild: status %d", code)
	}
}

// TestOwnerTokenIsNotReadableFromTheProfile is the "credential is not data"
// check: the token is returned once at creation and never again.
func TestOwnerTokenIsNotReadableFromTheProfile(t *testing.T) {
	_, srv := guildAPI(t)
	id, tok := newPlayer(t, srv, "secretive")

	code, body := guildReq(t, srv, http.MethodGet, fmt.Sprintf("/v1/players/%d", id), "", "")
	if code != http.StatusOK {
		t.Fatalf("profile read: status %d body %s", code, body)
	}
	if strings.Contains(body, tok) || strings.Contains(strings.ToLower(body), "owner_token") {
		t.Fatalf("the profile response leaks the owner token: %s", body)
	}
}
