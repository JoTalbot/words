package main

// HTTP surface for guilds (M2 batch 32G, docs/M2-GUILDS.md).
//
// Two rules shape this file:
//
//   - Identity comes from a token, never from a body field. A caller cannot say
//     who they are, so impersonation is impossible by construction rather than
//     prevented by a check someone could forget to write.
//   - Every refusal says which rule it hit. "409" alone is not a contract; the
//     messages below are what a client renders and what a support transcript
//     needs to be readable.

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
)

// profileCreateResponse is the profile response plus the owner token, which is
// returned exactly once at creation and never again (there is no read endpoint
// for it, on purpose).
type profileCreateResponse struct {
	Profile
	OwnerToken string `json:"owner_token"`
}

// hashOwnerToken hashes a presented owner token the way it is stored. The token
// itself is never persisted, so a database read cannot be replayed as a player.
func hashOwnerToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// authenticatePlayer resolves the caller's profile from X-Player-Token. The
// token is a bearer credential: possession is the claim, which is why the
// hash is all that is stored and why every guild mutation funnels through here.
func (a *API) authenticatePlayer(w http.ResponseWriter, r *http.Request) (uint64, bool) {
	token := strings.TrimSpace(r.Header.Get("X-Player-Token"))
	if token == "" {
		httpError(w, http.StatusUnauthorized, "missing X-Player-Token: guild changes are made as a player, not anonymously")
		return 0, false
	}
	if len(token) != 32 {
		httpError(w, http.StatusUnauthorized, "invalid player token")
		return 0, false
	}
	id, ok, err := a.profiles.PlayerIDByTokenHash(hashOwnerToken(token))
	if err != nil {
		httpError(w, http.StatusInternalServerError, "profile store error")
		return 0, false
	}
	if !ok {
		// The same message for a malformed and an unknown token: the difference
		// is only useful to someone guessing.
		httpError(w, http.StatusUnauthorized, "invalid player token")
		return 0, false
	}
	return id, true
}

// guildError maps a repo error to the status and message a client sees.
func guildError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrGuildNotFound):
		httpError(w, http.StatusNotFound, "guild not found")
	case errors.Is(err, ErrGuildNameTaken):
		httpError(w, http.StatusConflict, "that guild name or tag is already taken")
	case errors.Is(err, ErrGuildFull):
		httpError(w, http.StatusConflict, "this guild is full")
	case errors.Is(err, ErrAlreadyInGuild):
		httpError(w, http.StatusConflict, "a player may belong to one guild at a time")
	case errors.Is(err, ErrNotMember):
		httpError(w, http.StatusNotFound, "that player is not a member of this guild")
	case errors.Is(err, ErrNotOwner):
		httpError(w, http.StatusForbidden, "only the guild owner may remove another member")
	default:
		httpError(w, http.StatusInternalServerError, "guild store error")
	}
}

// handleGuildCreate creates a guild owned by the authenticated caller.
func (a *API) handleGuildCreate(w http.ResponseWriter, r *http.Request) {
	if a.rejectIfDraining(w) {
		return
	}
	if !a.allowMutation(w, r) {
		return
	}
	player, ok := a.authenticatePlayer(w, r)
	if !ok {
		return
	}
	var req struct {
		Name     string `json:"name"`
		Tag      string `json:"tag"`
		Language string `json:"language"`
	}
	if !a.decodeJSON(w, r, &req) {
		return
	}
	name := strings.TrimSpace(req.Name)
	if !ValidateGuildName(name) {
		httpError(w, http.StatusBadRequest, "guild name must be 3..24 characters using letters, digits, '-', '_' or '.', starting with a letter or digit")
		return
	}
	tag := strings.ToUpper(strings.TrimSpace(req.Tag))
	if !ValidateGuildTag(tag) {
		httpError(w, http.StatusBadRequest, "guild tag must be 2..5 characters using A-Z or 0-9")
		return
	}
	lang := req.Language
	if lang == "" {
		lang = "en"
	}
	switch lang {
	case "en", "ru", "uk":
	default:
		httpError(w, http.StatusBadRequest, "language must be en, ru or uk")
		return
	}
	g, err := a.guilds.CreateGuild(name, tag, lang, player)
	if err != nil {
		guildError(w, err)
		return
	}
	a.publishTelemetry(telemetryEvent{Type: "guild_created", GuildID: g.ID, UserID: player, Language: lang})
	writeJSON(w, http.StatusCreated, g)
}

// handleGuildGet serves a guild and its roster. No token: rosters are public in
// a social game, and making them secret would make moderation harder.
func (a *API) handleGuildGet(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r.PathValue("id"))
	if err != nil {
		httpError(w, http.StatusBadRequest, "invalid guild id")
		return
	}
	d, ok, err := a.guilds.GetGuild(id)
	if err != nil {
		guildError(w, err)
		return
	}
	if !ok {
		httpError(w, http.StatusNotFound, "guild not found")
		return
	}
	a.fillNicknames(&d)
	writeJSON(w, http.StatusOK, d)
}

// handleGuildJoin adds the authenticated caller to a guild's roster. Open join
// in this slice; the caller cannot ask to join as somebody else because the
// roster row is built from the token.
func (a *API) handleGuildJoin(w http.ResponseWriter, r *http.Request) {
	if a.rejectIfDraining(w) {
		return
	}
	if !a.allowMutation(w, r) {
		return
	}
	player, ok := a.authenticatePlayer(w, r)
	if !ok {
		return
	}
	id, err := parseID(r.PathValue("id"))
	if err != nil {
		httpError(w, http.StatusBadRequest, "invalid guild id")
		return
	}
	d, err := a.guilds.JoinGuild(id, player)
	if err != nil {
		guildError(w, err)
		return
	}
	a.publishTelemetry(telemetryEvent{Type: "guild_member_joined", GuildID: id, UserID: player})
	a.fillNicknames(&d)
	writeJSON(w, http.StatusOK, d)
}

// handleGuildMemberDelete removes a member. Removing yourself is leaving;
// removing someone else requires ownership.
func (a *API) handleGuildMemberDelete(w http.ResponseWriter, r *http.Request) {
	if a.rejectIfDraining(w) {
		return
	}
	if !a.allowMutation(w, r) {
		return
	}
	player, ok := a.authenticatePlayer(w, r)
	if !ok {
		return
	}
	guildID, err := parseID(r.PathValue("id"))
	if err != nil {
		httpError(w, http.StatusBadRequest, "invalid guild id")
		return
	}
	target, err := parseID(r.PathValue("player_id"))
	if err != nil {
		httpError(w, http.StatusBadRequest, "invalid player id")
		return
	}

	if target == player {
		_, dissolved, err := a.guilds.LeaveGuild(guildID, player)
		if err != nil {
			guildError(w, err)
			return
		}
		a.publishTelemetry(telemetryEvent{Type: "guild_member_left", GuildID: guildID, UserID: player})
		if dissolved {
			// The last member leaving dissolves the guild; say so rather than
			// returning an empty roster that looks like a still-existing guild.
			writeJSON(w, http.StatusOK, map[string]any{
				"guild_id": guildID, "dissolved": true,
				"note": "the last member left, so the guild no longer exists",
			})
			return
		}
	}
	// A caller may only remove another player as the owner; RemoveMember is
	// also the path for a self-removal that the repo already handled above, so
	// only run it when the target differs.
	if target != player {
		d, err := a.guilds.RemoveMember(guildID, player, target)
		if err != nil {
			guildError(w, err)
			return
		}
		a.publishTelemetry(telemetryEvent{Type: "guild_member_removed", GuildID: guildID, UserID: target})
		a.fillNicknames(&d)
		writeJSON(w, http.StatusOK, d)
		return
	}

	d, ok, err := a.guilds.GetGuild(guildID)
	if err != nil {
		guildError(w, err)
		return
	}
	if !ok {
		httpError(w, http.StatusNotFound, "guild not found")
		return
	}
	a.fillNicknames(&d)
	writeJSON(w, http.StatusOK, d)
}

// handlePlayerGuild answers which guild a player is in, with their role.
func (a *API) handlePlayerGuild(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r.PathValue("id"))
	if err != nil {
		httpError(w, http.StatusBadRequest, "invalid player id")
		return
	}
	if !profileIDInRange(id) {
		httpError(w, http.StatusBadRequest, "invalid player id")
		return
	}
	m, ok, err := a.guilds.GuildOf(id)
	if err != nil {
		guildError(w, err)
		return
	}
	if !ok {
		httpError(w, http.StatusNotFound, "this player is not in a guild")
		return
	}
	writeJSON(w, http.StatusOK, m)
}

// fillNicknames decorates a roster with display names. The guild store does not
// know about identity storage, so the join happens here; a guild member whose
// profile vanished still appears, with an empty nickname rather than dropping
// out of the roster silently.
func (a *API) fillNicknames(d *GuildDetail) {
	for i := range d.Members {
		if p, ok, err := a.profiles.Get(d.Members[i].PlayerID); err == nil && ok {
			d.Members[i].Nickname = p.Nickname
		}
	}
}
