package main

// Guild foundation (M2 batch 32G, docs/M2-GUILDS.md).
//
// A guild is a durable, privileged relationship between players, which is why
// this file and the owner-token primitive in profile.go arrived together: match
// seats are protected by per-match capability tokens and profiles are
// anonymous, so a roster built on a caller-supplied player_id would let anyone
// manage any guild as anyone. Every mutation therefore derives its identity
// from a token (X-Player-Token), never from a body field.
//
// Storage is behind GuildRepo: memory by default, Postgres when a DSN is set.

import (
	"errors"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
)

// guildMaxMembers caps a roster. It is a variable rather than a const so the
// tests can exercise the boundary without creating 51 profiles, and a variable
// rather than a schema constraint because the right number is a product
// question (docs/M2-GUILDS.md, follow-up 2).
var guildMaxMembers = 50

// Roles. owner is the founder or the member ownership was transferred to; a
// guild never exists without exactly one owner (invariant 4).
const (
	guildRoleOwner  = "owner"
	guildRoleMember = "member"
)

var (
	// ErrGuildNotFound, ErrGuildExists and ErrGuildFull are the create/join
	// refusals a caller can act on.
	ErrGuildNotFound = errors.New("guild not found")
	ErrGuildExists   = errors.New("guild name or tag is already taken")
	ErrGuildFull     = errors.New("guild is full")
	// ErrAlreadyInGuild is invariant 1: one guild per player.
	ErrAlreadyInGuild = errors.New("player is already in a guild")
	ErrNotMember      = errors.New("player is not a member of this guild")
	// ErrNotOwner is the capability failure: only the owner may remove another
	// member.
	ErrNotOwner = errors.New("only the guild owner may remove another member")
	// ErrGuildNameTaken is returned for a duplicate name/tag only.
	ErrGuildNameTaken = errors.New("guild name is already taken")
)

// Guild is a guild's public identity.
type Guild struct {
	ID              uint64    `json:"id"`
	Name            string    `json:"name"`
	Tag             string    `json:"tag"`
	Language        string    `json:"language"`
	FounderPlayerID uint64    `json:"founder_player_id"`
	CreatedAt       time.Time `json:"created_at"`
	MemberCount     int       `json:"member_count"`
}

// GuildMember is one roster row. Nickname is filled by the API layer from the
// profile store so the guild store stays independent of identity storage.
type GuildMember struct {
	PlayerID uint64    `json:"player_id"`
	Nickname string    `json:"nickname,omitempty"`
	Role     string    `json:"role"`
	JoinedAt time.Time `json:"joined_at"`
}

// GuildDetail is a guild plus its roster, ordered by join time then player id.
// The order is deterministic so a roster comparison in a test (or a support
// transcript) is stable.
type GuildDetail struct {
	Guild   Guild         `json:"guild"`
	Members []GuildMember `json:"members"`
}

// Membership answers "which guild is this player in".
type Membership struct {
	GuildID uint64 `json:"guild_id"`
	Name    string `json:"name"`
	Tag     string `json:"tag"`
	Role    string `json:"role"`
}

// GuildRepo persists guilds and rosters. Every mutating method is written so
// that a concurrent caller cannot leave the invariants broken: the in-memory
// implementation holds one mutex, the Postgres one runs each mutation in a
// transaction with the unique constraints as the backstop.
type GuildRepo interface {
	// CreateGuild registers a guild owned by founder and adds the founder to
	// its roster. It fails with ErrGuildNameTaken when name or tag is taken.
	CreateGuild(name, tag, lang string, founder uint64) (Guild, error)
	// GetGuild returns a guild and its roster.
	GetGuild(id uint64) (GuildDetail, bool, error)
	// JoinGuild adds player to the roster. It fails with ErrAlreadyInGuild when
	// the player is in another guild and ErrGuildFull when the cap is reached.
	JoinGuild(guildID, player uint64) (GuildDetail, error)
	// LeaveGuild removes player from the roster, transferring ownership to the
	// earliest-joined member when the owner leaves, and dissolving the guild
	// when the last member leaves. It reports whether the guild was dissolved.
	LeaveGuild(guildID, player uint64) (GuildDetail, bool, error)
	// RemoveMember removes target on behalf of actor. actor must be the owner
	// and may not remove itself (that is LeaveGuild).
	RemoveMember(guildID, actor, target uint64) (GuildDetail, error)
	// GuildOf returns the guild a player belongs to.
	GuildOf(player uint64) (Membership, bool, error)
	// Close releases backend resources.
	Close() error
}

// ---- validation ----

// normalizeGuildName lowercases and trims a name for its uniqueness key.
func normalizeGuildName(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

// ValidateGuildName enforces invariant 2: 3..24 characters, letters, digits and
// -_. only, starting with a letter or a digit. The same reasoning as nickname
// validation applies (security.ValidateNickname): no control characters, no
// spaces, nothing that can forge a log line or break an inline rendering.
func ValidateGuildName(name string) bool {
	name = strings.TrimSpace(name)
	r := []rune(name)
	if len(r) < 3 || len(r) > 24 {
		return false
	}
	for i, c := range r {
		switch {
		case unicode.IsLetter(c) || unicode.IsDigit(c):
		case c == '-' || c == '_' || c == '.':
			if i == 0 || i == len(r)-1 {
				return false // no leading or trailing separator
			}
			if r[i-1] == '-' || r[i-1] == '_' || r[i-1] == '.' {
				return false // no runs of separators
			}
		default:
			return false
		}
	}
	return true
}

// ValidateGuildTag enforces invariant 2 for tags: 2..5 characters, A-Z or 0-9.
func ValidateGuildTag(tag string) bool {
	tag = strings.TrimSpace(tag)
	if len(tag) < 2 || len(tag) > 5 {
		return false
	}
	for _, c := range tag {
		if (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
			continue
		}
		return false
	}
	return true
}

// ---- in-memory backend ----

type memGuildStore struct {
	mu        sync.Mutex
	byID      map[uint64]*GuildDetail
	byNameKey map[string]uint64
	byTag     map[string]uint64
	byPlayer  map[uint64]uint64
	counter   uint64
}

func newMemGuildStore() *memGuildStore {
	return &memGuildStore{
		byID:      map[uint64]*GuildDetail{},
		byNameKey: map[string]uint64{},
		byTag:     map[string]uint64{},
		byPlayer:  map[uint64]uint64{},
	}
}

func (gs *memGuildStore) CreateGuild(name, tag, lang string, founder uint64) (Guild, error) {
	name = strings.TrimSpace(name)
	tag = strings.ToUpper(strings.TrimSpace(tag))
	key := normalizeGuildName(name)

	gs.mu.Lock()
	defer gs.mu.Unlock()
	if _, ok := gs.byNameKey[key]; ok {
		return Guild{}, ErrGuildNameTaken
	}
	if _, ok := gs.byTag[tag]; ok {
		return Guild{}, ErrGuildNameTaken
	}
	if _, ok := gs.byPlayer[founder]; ok {
		return Guild{}, ErrAlreadyInGuild
	}
	gs.counter++
	g := Guild{
		ID: gs.counter, Name: name, Tag: tag, Language: lang,
		FounderPlayerID: founder, CreatedAt: time.Now(), MemberCount: 1,
	}
	d := &GuildDetail{Guild: g, Members: []GuildMember{{
		PlayerID: founder, Role: guildRoleOwner, JoinedAt: g.CreatedAt,
	}}}
	gs.byID[g.ID] = d
	gs.byNameKey[key] = g.ID
	gs.byTag[tag] = g.ID
	gs.byPlayer[founder] = g.ID
	return g, nil
}

func (gs *memGuildStore) GetGuild(id uint64) (GuildDetail, bool, error) {
	gs.mu.Lock()
	defer gs.mu.Unlock()
	d, ok := gs.byID[id]
	if !ok {
		return GuildDetail{}, false, nil
	}
	return cloneDetail(*d), true, nil
}

func (gs *memGuildStore) JoinGuild(guildID, player uint64) (GuildDetail, error) {
	gs.mu.Lock()
	defer gs.mu.Unlock()
	d, ok := gs.byID[guildID]
	if !ok {
		return GuildDetail{}, ErrGuildNotFound
	}
	if other, ok := gs.byPlayer[player]; ok {
		if other == guildID {
			return GuildDetail{}, ErrAlreadyInGuild
		}
		return GuildDetail{}, ErrAlreadyInGuild
	}
	if len(d.Members) >= guildMaxMembers {
		return GuildDetail{}, ErrGuildFull
	}
	d.Members = append(d.Members, GuildMember{PlayerID: player, Role: guildRoleMember, JoinedAt: time.Now()})
	d.Guild.MemberCount = len(d.Members)
	gs.byPlayer[player] = guildID
	return cloneDetail(*d), nil
}

func (gs *memGuildStore) LeaveGuild(guildID, player uint64) (GuildDetail, bool, error) {
	gs.mu.Lock()
	defer gs.mu.Unlock()
	d, ok := gs.byID[guildID]
	if !ok {
		return GuildDetail{}, false, ErrGuildNotFound
	}
	idx := -1
	for i, m := range d.Members {
		if m.PlayerID == player {
			idx = i
			break
		}
	}
	if idx < 0 {
		return GuildDetail{}, false, ErrNotMember
	}
	wasOwner := d.Members[idx].Role == guildRoleOwner
	d.Members = append(d.Members[:idx], d.Members[idx+1:]...)
	delete(gs.byPlayer, player)

	if len(d.Members) == 0 {
		// Invariant 4: the last member leaving dissolves the guild. Leaving a
		// name reserved by an empty guild would let a dead guild squat on it.
		delete(gs.byID, guildID)
		delete(gs.byNameKey, normalizeGuildName(d.Guild.Name))
		delete(gs.byTag, d.Guild.Tag)
		return GuildDetail{}, true, nil
	}
	if wasOwner {
		// Invariant 4: ownership transfers to the earliest-joined remaining
		// member. Deterministic, and visible in the roster rather than implied.
		next := earliestMember(d.Members)
		for i := range d.Members {
			if d.Members[i].PlayerID == next {
				d.Members[i].Role = guildRoleOwner
				break
			}
		}
	}
	d.Guild.MemberCount = len(d.Members)
	return cloneDetail(*d), false, nil
}

func (gs *memGuildStore) RemoveMember(guildID, actor, target uint64) (GuildDetail, error) {
	gs.mu.Lock()
	defer gs.mu.Unlock()
	d, ok := gs.byID[guildID]
	if !ok {
		return GuildDetail{}, ErrGuildNotFound
	}
	actorRole, actorIn := "", false
	targetIdx := -1
	for i, m := range d.Members {
		if m.PlayerID == actor {
			actorRole, actorIn = m.Role, true
		}
		if m.PlayerID == target {
			targetIdx = i
		}
	}
	if !actorIn || actorRole != guildRoleOwner {
		return GuildDetail{}, ErrNotOwner
	}
	if targetIdx < 0 {
		return GuildDetail{}, ErrNotMember
	}
	if actor == target {
		// There is no self-kick: leaving is the operation for that, and
		// conflating them is how "I accidentally kicked myself" happens.
		return GuildDetail{}, ErrNotOwner
	}
	d.Members = append(d.Members[:targetIdx], d.Members[targetIdx+1:]...)
	delete(gs.byPlayer, target)
	d.Guild.MemberCount = len(d.Members)
	return cloneDetail(*d), nil
}

func (gs *memGuildStore) GuildOf(player uint64) (Membership, bool, error) {
	gs.mu.Lock()
	defer gs.mu.Unlock()
	id, ok := gs.byPlayer[player]
	if !ok {
		return Membership{}, false, nil
	}
	d, ok := gs.byID[id]
	if !ok {
		return Membership{}, false, nil
	}
	for _, m := range d.Members {
		if m.PlayerID == player {
			return Membership{GuildID: id, Name: d.Guild.Name, Tag: d.Guild.Tag, Role: m.Role}, true, nil
		}
	}
	return Membership{}, false, nil
}

func (gs *memGuildStore) Close() error { return nil }

// earliestMember returns the player id of the earliest-joined member, breaking
// ties on the lower player id so the transfer is deterministic.
func earliestMember(members []GuildMember) uint64 {
	best := members[0]
	for _, m := range members[1:] {
		if m.JoinedAt.Before(best.JoinedAt) ||
			(m.JoinedAt.Equal(best.JoinedAt) && m.PlayerID < best.PlayerID) {
			best = m
		}
	}
	return best.PlayerID
}

// cloneDetail copies a roster so callers cannot mutate stored state.
func cloneDetail(d GuildDetail) GuildDetail {
	out := d
	out.Members = append([]GuildMember(nil), d.Members...)
	sort.SliceStable(out.Members, func(i, j int) bool {
		if !out.Members[i].JoinedAt.Equal(out.Members[j].JoinedAt) {
			return out.Members[i].JoinedAt.Before(out.Members[j].JoinedAt)
		}
		return out.Members[i].PlayerID < out.Members[j].PlayerID
	})
	out.Guild.MemberCount = len(out.Members)
	return out
}
