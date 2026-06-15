package cmd

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	openapi_types "github.com/oapi-codegen/runtime/types"
)

// TestOwnerResolverResolvesPersonalAndOrgSkills verifies the resolver
// correctly maps an apiSkill (owner_id / org_id UUIDs) back to the
// (kind, slug) pair the marker stores. The skills list endpoint returns
// UUIDs only; the resolver looks up slugs via /api/v1/me +
// /api/v1/organizations.
//
// Covers the secondary fix from
// cli-push-owned-listing-excludes-org-membership-skills.md: pull's
// linked path needs to write full owner context onto new markers so
// doctor/list/future tooling can identify a skill's namespace without
// re-fetching the world.
func TestOwnerResolverResolvesPersonalAndOrgSkills(t *testing.T) {
	const (
		userID    = "11111111-1111-1111-1111-111111111111"
		username  = "alice"
		orgAID    = "22222222-2222-2222-2222-222222222222"
		orgASlug  = "cherrypick"
		strangeID = "99999999-9999-9999-9999-999999999999"
	)

	var meHits, orgsHits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/me":
			meHits++
			fmt.Fprintf(w, `{"id":%q,"username":%q,"role":"user","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}`, userID, username)
		case "/api/v1/organizations":
			orgsHits++
			fmt.Fprintf(w, `{"organizations":[{"id":%q,"slug":%q,"name":"Cherrypick","role":"member","member_count":1}]}`, orgAID, orgASlug)
		default:
			http.Error(w, "not found", 404)
		}
	}))
	defer srv.Close()

	c := &apiClient{baseURL: srv.URL, token: "test-token", http: srv.Client()}
	r := newOwnerResolver(c)

	mustUUID := func(s string) openapi_types.UUID {
		var u openapi_types.UUID
		if err := u.UnmarshalText([]byte(s)); err != nil {
			t.Fatalf("parse uuid %s: %v", s, err)
		}
		return u
	}

	personalOwner := mustUUID(userID)
	orgOwner := mustUUID(orgAID)
	strangerOwner := mustUUID(strangeID)

	cases := []struct {
		name     string
		skill    *apiSkill
		wantKind string
		wantSlug string
	}{
		{
			name:     "personal skill owned by caller",
			skill:    &apiSkill{OwnerId: &personalOwner},
			wantKind: "user",
			wantSlug: username,
		},
		{
			name:     "org skill in caller's org",
			skill:    &apiSkill{OrgId: &orgOwner},
			wantKind: "org",
			wantSlug: orgASlug,
		},
		{
			name:     "org skill in an org caller is NOT a member of",
			skill:    &apiSkill{OrgId: &strangerOwner},
			wantKind: "org",
			wantSlug: "", // unknown slug — better than misclassifying as user
		},
		{
			name:     "personal skill owned by someone else (fork case)",
			skill:    &apiSkill{OwnerId: &strangerOwner},
			wantKind: "", // unknown — leave marker fields blank
			wantSlug: "",
		},
		{
			name:     "nil skill",
			skill:    nil,
			wantKind: "",
			wantSlug: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			kind, slug := r.resolve(tc.skill)
			if kind != tc.wantKind {
				t.Errorf("kind: want %q, got %q", tc.wantKind, kind)
			}
			if slug != tc.wantSlug {
				t.Errorf("slug: want %q, got %q", tc.wantSlug, slug)
			}
		})
	}

	if meHits > 1 || orgsHits > 1 {
		t.Errorf("expected lazy init to hit each endpoint at most once, got me=%d orgs=%d", meHits, orgsHits)
	}
}

// TestSourceForUsesOwnerUsernameForSubscription verifies that the marker
// Source for a subscription (another user's personal skill) records the
// upstream owner's username from the effective listing's owner_username field
// (b0). Before that field existed, Source.Owner was left empty for another
// user's skill, breaking "added from <owner>" and anon-pull resolution on a
// fresh device.
func TestSourceForUsesOwnerUsernameForSubscription(t *testing.T) {
	const (
		userID    = "11111111-1111-1111-1111-111111111111"
		username  = "me"
		strangeID = "99999999-9999-9999-9999-999999999999"
		skillID   = "33333333-3333-3333-3333-333333333333"
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/me":
			fmt.Fprintf(w, `{"id":%q,"username":%q,"role":"user","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}`, userID, username)
		case "/api/v1/organizations":
			fmt.Fprint(w, `{"organizations":[]}`)
		default:
			http.Error(w, "not found", 404)
		}
	}))
	defer srv.Close()

	c := &apiClient{baseURL: srv.URL, token: "t", http: srv.Client()}
	r := newOwnerResolver(c)

	mustUUID := func(s string) openapi_types.UUID {
		var u openapi_types.UUID
		if err := u.UnmarshalText([]byte(s)); err != nil {
			t.Fatalf("parse uuid %s: %v", s, err)
		}
		return u
	}
	strangerOwner := mustUUID(strangeID)
	sid := mustUUID(skillID)
	upstreamOwner := "alice"

	src := r.sourceFor(&apiSkill{Id: sid, OwnerId: &strangerOwner, Slug: "retro", OwnerUsername: &upstreamOwner})
	if src == nil {
		t.Fatalf("sourceFor returned nil for a non-owned skill (a subscription)")
	}
	if src.Owner != "alice" {
		t.Errorf("Source.Owner: want %q (from owner_username), got %q", "alice", src.Owner)
	}
	if src.Slug != "retro" || src.ID != skillID {
		t.Errorf("Source slug/id wrong: %+v", src)
	}

	// The caller's own skill → no Source, regardless of owner_username.
	mineOwner := mustUUID(userID)
	mineName := username
	if r.sourceFor(&apiSkill{Id: sid, OwnerId: &mineOwner, Slug: "retro", OwnerUsername: &mineName}) != nil {
		t.Errorf("sourceFor should be nil for a caller-owned skill")
	}
}
