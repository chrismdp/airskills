package cmd

import (
	"fmt"
	"strings"
	"testing"
)

// TestIncomingCommandIsGone is the spec's "command surface gone" test:
// the airskills incoming command tree was removed in favour of
// airskills add ... --force.
func TestIncomingCommandIsGone(t *testing.T) {
	for _, c := range rootCmd.Commands() {
		if c.Name() == "incoming" {
			t.Fatalf("incoming command still registered on rootCmd; expected it deleted")
		}
	}
}

// TestAddCmdHasForceFlag asserts the --force flag is wired up on
// airskills add. The flag is the only user-facing surface for
// adopting upstream's current bytes after the incoming command tree
// was deleted; if the wiring breaks, users have no path to take
// upstream.
func TestAddCmdHasForceFlag(t *testing.T) {
	flag := addCmd.Flags().Lookup("force")
	if flag == nil {
		t.Fatal("airskills add is missing the --force flag")
	}
	if flag.Value.Type() != "bool" {
		t.Errorf("--force should be bool, got %s", flag.Value.Type())
	}
}

// TestPendingReviewSummaryUsesAddForce verifies the collapsed
// one-line-per-skill output points at `airskills add owner/slug
// --force` rather than the old `airskills incoming` surface or the
// pre-spec multi-paragraph form. Drives renderPendingReviewSummary
// directly with a synthesised pending row so we don't have to mock
// the listing API.
func TestPendingReviewSummaryUsesAddForce(t *testing.T) {
	pending := []SkillStateInfo{{
		Name:          "my-fork",
		State:         StateTracked,
		Local:         true,
		Sourced:       true,
		UpstreamMoved: true,
		Marker: &SyncEntry{
			Source: &skillSource{Owner: "alice", Slug: "my-fork"},
		},
	}}

	out := captureStdout(t, func() {
		renderPendingReviewSummary(pending)
	})

	if strings.Contains(out, "airskills incoming") {
		t.Errorf("output still references airskills incoming:\n%s", out)
	}
	if !strings.Contains(out, "airskills add alice/my-fork --force") {
		t.Errorf("expected new add --force hint pointing at alice/my-fork, got:\n%s", out)
	}
	// One line per skill: the verbose multi-paragraph form is gone.
	// "ASK THE USER" was the load-bearing string on the old verbose
	// branch — its absence is a useful collapse-format canary.
	if strings.Contains(out, "ASK THE USER") {
		t.Errorf("expected collapsed one-liner output, got the old multi-paragraph form:\n%s", out)
	}
}


// add --force used to preserve a stale marker SkillID (it only set the id
// when empty). A marker left tracking the hidden backup fork (the
// pre-overlay 0.7.24 fork-on-push shape, since flagged backup=true
// server-side) therefore kept that identity while owner/source/
// resolved_hash were rewritten around it — the exact broken marker from
// the 2026-06-11 field report. Taking upstream's bytes means tracking the
// upstream: the old id must flip, with a confirmed backup row preserved
// in entry.Backup.
func TestAdoptUpstreamIdentityFlipsBackupRowMarker(t *testing.T) {
	forkID := testUUID("fork-1")
	upstreamID := testUUID("upstream-1")

	entry := &SyncEntry{SkillID: forkID.String()}
	lookup := func(id string) (*apiSkill, error) {
		if id != forkID.String() {
			t.Fatalf("looked up %q, want the old marker id", id)
		}
		return &apiSkill{Id: forkID, ContentHash: strPtr("backup-hash"), ForkedFrom: &upstreamID, Backup: true}, nil
	}

	adoptUpstreamIdentity(entry, upstreamID.String(), lookup)
	if entry.SkillID != upstreamID.String() {
		t.Errorf("SkillID = %q, want upstream %q", entry.SkillID, upstreamID.String())
	}
	if entry.Backup == nil || entry.Backup.SkillID != forkID.String() || entry.Backup.ContentHash != "backup-hash" {
		t.Errorf("Backup should preserve the old backup row, got %+v", entry.Backup)
	}
}

// A visible same-slug fork (the caller's own deliberate skill) must keep
// its identity — add --force only takes upstream's BYTES there.
func TestAdoptUpstreamIdentityKeepsVisibleFork(t *testing.T) {
	forkID := testUUID("fork-1")
	upstreamID := testUUID("upstream-1")

	entry := &SyncEntry{SkillID: forkID.String()}
	lookup := func(id string) (*apiSkill, error) {
		return &apiSkill{Id: forkID, ForkedFrom: &upstreamID, Backup: false}, nil
	}

	adoptUpstreamIdentity(entry, upstreamID.String(), lookup)
	if entry.SkillID != forkID.String() {
		t.Errorf("SkillID = %q, want the visible fork %q kept", entry.SkillID, forkID.String())
	}
	if entry.Backup != nil {
		t.Errorf("no Backup should be invented for a visible fork, got %+v", entry.Backup)
	}
}

// A gone old row (404/410) flips to the upstream — keeping a dead id
// helps nobody; a transient lookup failure changes nothing.
func TestAdoptUpstreamIdentityGoneAndTransient(t *testing.T) {
	upstreamID := testUUID("upstream-1")

	gone := &SyncEntry{SkillID: testUUID("fork-1").String()}
	adoptUpstreamIdentity(gone, upstreamID.String(), func(string) (*apiSkill, error) {
		return nil, fmt.Errorf("API error (404): not found")
	})
	if gone.SkillID != upstreamID.String() {
		t.Errorf("gone row: SkillID = %q, want upstream", gone.SkillID)
	}

	transient := &SyncEntry{SkillID: testUUID("fork-1").String()}
	adoptUpstreamIdentity(transient, upstreamID.String(), func(string) (*apiSkill, error) {
		return nil, fmt.Errorf("API error (500): boom")
	})
	if transient.SkillID != testUUID("fork-1").String() {
		t.Errorf("transient failure must not change identity, got %q", transient.SkillID)
	}

	empty := &SyncEntry{}
	adoptUpstreamIdentity(empty, upstreamID.String(), nil)
	if empty.SkillID != upstreamID.String() {
		t.Errorf("empty SkillID adopts the upstream, got %q", empty.SkillID)
	}

	already := &SyncEntry{SkillID: upstreamID.String()}
	adoptUpstreamIdentity(already, upstreamID.String(), func(string) (*apiSkill, error) {
		t.Fatal("no lookup should happen when the identity already matches")
		return nil, nil
	})
	if already.SkillID != upstreamID.String() {
		t.Errorf("matching identity must be untouched")
	}
}
