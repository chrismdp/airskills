package cmd

import (
	"sync"
)

// ownerResolver maps an apiSkill's owner_id / org_id UUIDs to the
// (kind, slug) pair the local marker stores. The skills list endpoint
// returns UUIDs only, but the marker stores slugs so other commands
// (doctor, list, future tooling) can identify a skill's namespace
// without re-fetching the world.
//
// Lookups are lazy: the resolver only hits /api/v1/me and
// /api/v1/organizations the first time it's asked, so tests with
// no-skill responses pay nothing. Errors during init are swallowed —
// a partial marker (no owner_kind/owner_slug) is no worse than the
// pre-fix state.
type ownerResolver struct {
	c            *apiClient
	initOnce     sync.Once
	userID       string
	username     string
	orgsByID     map[string]string // org_id → org slug
	orgRolesByID map[string]string // org_id → caller's role ("owner"/"admin"/"member")
	orgsOK       bool              // the /organizations fetch succeeded — roles are trustworthy
}

func newOwnerResolver(c *apiClient) *ownerResolver {
	return &ownerResolver{c: c}
}

func (r *ownerResolver) init() {
	if r.c == nil {
		return
	}
	profile, err := r.c.getMe()
	if err == nil && profile != nil {
		r.userID = profile.Id.String()
		r.username = profile.Username
	}
	orgs, err := listCallerOrgs(r.c)
	if err == nil {
		r.orgsOK = true
		r.orgsByID = make(map[string]string, len(orgs))
		r.orgRolesByID = make(map[string]string, len(orgs))
		for _, o := range orgs {
			r.orgsByID[o.ID] = o.Slug
			r.orgRolesByID[o.ID] = o.Role
		}
	}
}

// callerUsername returns the caller's username, or "" when /me failed.
func (r *ownerResolver) callerUsername() string {
	r.initOnce.Do(r.init)
	return r.username
}

// orgRole returns the caller's role in the given org and whether the role
// data is trustworthy (the /organizations fetch succeeded). Push routes
// org-skill writes by this — owner/admin write in place, everyone else
// goes via the backup+suggest path. When ok is false, callers must fall
// back to the server's verdict rather than assume either way.
func (r *ownerResolver) orgRole(orgID string) (role string, ok bool) {
	r.initOnce.Do(r.init)
	if !r.orgsOK {
		return "", false
	}
	return r.orgRolesByID[orgID], true
}

// resolve returns the (kind, slug) for a skill. Empty strings mean
// "unknown" — caller should leave the marker fields blank rather than
// guess. Org skills the caller is not a member of (e.g. just transferred
// in) return ("org", "") which is still better than nothing: the kind
// alone tells push not to misclassify the skill as a personal orphan.
func (r *ownerResolver) resolve(skill *apiSkill) (kind, slug string) {
	if skill == nil {
		return "", ""
	}
	r.initOnce.Do(r.init)
	if skill.OrgId != nil {
		return "org", r.orgsByID[skill.OrgId.String()]
	}
	if skill.OwnerId != nil && r.userID != "" && skill.OwnerId.String() == r.userID {
		return "user", r.username
	}
	return "", ""
}

// sourceFor returns the marker `Source` block for a non-owned skill, or
// nil when the caller owns the skill. Source tells future syncs that
// this skill came from elsewhere, so push knows to fork-then-suggest
// rather than push-to-upstream. See
// platform/doc/changes/cli-org-member-suggest-via-shadow-fork.md.
//
// Owner slug we record is the upstream owner: org slug for org skills,
// empty for another user's personal skill (we don't have their username
// without a separate lookup — slug + ID still pin the upstream and the
// fork-suggest path doesn't need Owner). Returns nil for:
//   - caller-owned skills (no Source needed)
//   - skills whose owner we cannot identify and there's no useful slug
//     to record — leave Source nil rather than write a half-record.
// sourceForNonOwned returns sourceFor(skill) only when the skill is
// provably not the caller's: org-owned, or personal-owned by a different
// user. Returns nil when ownership can't be established (e.g. the /me
// lookup failed) — callers use this where guessing wrong would fork the
// caller's own skill.
func (r *ownerResolver) sourceForNonOwned(skill *apiSkill) *skillSource {
	if skill == nil {
		return nil
	}
	r.initOnce.Do(r.init)
	if skill.OrgId != nil {
		return r.sourceFor(skill)
	}
	if skill.OwnerId != nil && r.userID != "" && skill.OwnerId.String() != r.userID {
		return r.sourceFor(skill)
	}
	return nil
}

func (r *ownerResolver) sourceFor(skill *apiSkill) *skillSource {
	if skill == nil {
		return nil
	}
	r.initOnce.Do(r.init)
	// Org-owned skills take the org branch unconditionally: post lineage
	// split, forked_from on a listed skill always means a genuine fork
	// (transfer successors carry no lineage), so the org skill itself IS
	// the upstream the marker should point at.
	if skill.OrgId != nil {
		owner := r.orgsByID[skill.OrgId.String()]
		return &skillSource{
			Owner:               owner,
			Slug:                skill.Slug,
			ID:                  skill.Id.String(),
			ContentHash:         strDeref(skill.ContentHash),
			UpstreamSkillID:     skill.Id.String(),
			UpstreamContentHash: strDeref(skill.ContentHash),
			UpstreamVersion:     skill.Version,
		}
	}
	if skill.OwnerId != nil && r.userID != "" && skill.OwnerId.String() == r.userID && skill.ForkedFrom == nil {
		return nil // caller owns the skill — no Source
	}
	if skill.ForkedFrom != nil {
		upstreamHash := strDeref(skill.UpstreamContentHash)
		return &skillSource{
			Slug:                skill.Slug,
			ID:                  skill.ForkedFrom.String(),
			ContentHash:         upstreamHash,
			UpstreamSkillID:     skill.ForkedFrom.String(),
			UpstreamContentHash: upstreamHash,
			UpstreamVersion:     skill.Version,
		}
	}
	// Another user's personal skill — a subscription. The effective listing
	// (GET /api/v1/skills) now carries the upstream owner's username, so the
	// marker can record it for "added from <owner>" and anon-pull resolution
	// without a probe. Empty when absent (pre-b0 server, or getSkill which
	// doesn't project it) — same as the old behaviour.
	return &skillSource{
		Owner:               strDeref(skill.OwnerUsername),
		Slug:                skill.Slug,
		ID:                  skill.Id.String(),
		ContentHash:         strDeref(skill.ContentHash),
		UpstreamSkillID:     skill.Id.String(),
		UpstreamContentHash: strDeref(skill.ContentHash),
		UpstreamVersion:     skill.Version,
	}
}
