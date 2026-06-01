package cmd

import (
	"sort"
	"testing"
)

// classifySkills is the shared classifier described in
// doc/changes/cli-untracked-collision-and-resolve.md. It produces the
// cross-state of every skill on the machine — both server-known skills
// and untracked local directories — without any network calls.
//
// These tests pin the contract: every presence state in the enum is
// exercised at least once, and the tracked rows assert the divergence
// booleans (LocalDirty / RemoteMoved / UpstreamMoved) that replaced the old
// modified / modified-pending flat values.

func findInfo(t *testing.T, results []SkillStateInfo, name string) SkillStateInfo {
	t.Helper()
	for _, r := range results {
		if r.Name == name {
			return r
		}
	}
	t.Fatalf("no classification for %q in %v", name, names(results))
	return SkillStateInfo{}
}

func names(results []SkillStateInfo) []string {
	out := make([]string, 0, len(results))
	for _, r := range results {
		out = append(out, r.Name)
	}
	sort.Strings(out)
	return out
}

// stubHasher returns a fixed hash for any path. Tests use this to
// simulate "what's on disk right now" without writing real files.
func stubHasher(byPath map[string]string) func(string) string {
	return func(path string) string { return byPath[path] }
}

func TestClassifySynced(t *testing.T) {
	state := &SyncState{
		Version: 1,
		Skills: map[string]*SyncEntry{
			"deploy-check": {SkillID: testUUID("id-1").String(), ContentHash: "h-server"},
		},
	}
	remote := []apiSkill{{Id: testUUID("id-1"), Name: "deploy-check", ContentHash: strPtr("h-server")}}
	local := map[string]string{"deploy-check": "/disk/deploy-check"}
	hash := stubHasher(map[string]string{"/disk/deploy-check": "h-server"})

	got := classifySkills(remote, local, state, hash)
	info := findInfo(t, got, "deploy-check")
	if info.State != StateTracked {
		t.Errorf("expected tracked, got %s", info.State)
	}
	if info.LocalDirty || info.RemoteMoved || info.UpstreamMoved {
		t.Errorf("expected a clean tracked skill, got dirty=%v remoteMoved=%v upstreamMoved=%v",
			info.LocalDirty, info.RemoteMoved, info.UpstreamMoved)
	}
}

func TestClassifyModifiedOwned(t *testing.T) {
	// Owned (no Source). Local has diverged from marker → LocalDirty on the
	// own axis (renders "modified").
	state := &SyncState{
		Version: 1,
		Skills: map[string]*SyncEntry{
			"my-skill": {SkillID: testUUID("id-1").String(), ContentHash: "h-marker"},
		},
	}
	remote := []apiSkill{{Id: testUUID("id-1"), Name: "my-skill", ContentHash: strPtr("h-marker")}}
	local := map[string]string{"my-skill": "/disk/my-skill"}
	hash := stubHasher(map[string]string{"/disk/my-skill": "h-local-edits"})

	got := classifySkills(remote, local, state, hash)
	info := findInfo(t, got, "my-skill")
	if info.State != StateTracked || !info.LocalDirty || info.Sourced || info.UpstreamMoved {
		t.Errorf("expected tracked+localDirty owned, got state=%s dirty=%v sourced=%v upstreamMoved=%v",
			info.State, info.LocalDirty, info.Sourced, info.UpstreamMoved)
	}
}

func TestClassifySourcedCustomisedOwnAxis(t *testing.T) {
	// A sourced skill the user has customised, whose matched remote row is
	// the upstream itself (no separate fork head). The customisation shows
	// on the own axis (LocalDirty); the fork axis stays quiet because there
	// is no separate parent head to have moved. Renders "modified".
	state := &SyncState{
		Version: 1,
		Skills: map[string]*SyncEntry{
			"heartbeat": {
				SkillID:      testUUID("id-1").String(),
				ContentHash:  "h-marker",
				ResolvedHash: "h-upstream-current",
				Source:       &skillSource{Owner: "chrismdp", Slug: "heartbeat"},
			},
		},
	}
	remote := []apiSkill{{Id: testUUID("id-1"), Name: "heartbeat", ContentHash: strPtr("h-upstream-current")}}
	local := map[string]string{"heartbeat": "/disk/heartbeat"}
	hash := stubHasher(map[string]string{"/disk/heartbeat": "h-customised"})

	got := classifySkills(remote, local, state, hash)
	info := findInfo(t, got, "heartbeat")
	if info.State != StateTracked || !info.Sourced || !info.LocalDirty || info.UpstreamMoved {
		t.Errorf("expected tracked sourced localDirty, upstream quiet; got state=%s sourced=%v dirty=%v upstreamMoved=%v",
			info.State, info.Sourced, info.LocalDirty, info.UpstreamMoved)
	}
	if listStateLabel(info) != "modified" {
		t.Errorf("expected list label 'modified', got %q", listStateLabel(info))
	}
}

func TestClassifyForkUpstreamMoved(t *testing.T) {
	// A server-side fork (UpstreamContentHash populated = parent's live head)
	// whose parent has moved past the version the user acknowledged
	// (upstream_base). This is the unified replacement for modified-pending:
	// UpstreamMoved is true and the list label is "modified*".
	state := &SyncState{
		Version: 1,
		Skills: map[string]*SyncEntry{
			"heartbeat": {
				SkillID:      testUUID("fork-1").String(),
				ContentHash:  "h-fork-local",
				ResolvedHash: "h-upstream-old",
				Source:       &skillSource{Owner: "chrismdp", Slug: "heartbeat", ID: testUUID("parent-1").String()},
			},
		},
	}
	forkParent := testUUID("parent-1")
	remote := []apiSkill{{
		Id:                  testUUID("fork-1"),
		Name:                "heartbeat",
		ContentHash:         strPtr("h-fork-local"), // own head == base: own axis clean
		ForkedFrom:          &forkParent,
		UpstreamContentHash: strPtr("h-upstream-new"), // parent moved
	}}
	local := map[string]string{"heartbeat": "/disk/heartbeat"}
	hash := stubHasher(map[string]string{"/disk/heartbeat": "h-fork-local"})

	got := classifySkills(remote, local, state, hash)
	info := findInfo(t, got, "heartbeat")
	if info.State != StateTracked || !info.UpstreamMoved {
		t.Errorf("expected tracked + upstreamMoved, got state=%s upstreamMoved=%v", info.State, info.UpstreamMoved)
	}
	// Bug #1: clean own axis + upstream moved must NOT read as synced.
	if info.LocalDirty {
		t.Errorf("expected clean own axis (no local edits), got LocalDirty=true")
	}
	if listStateLabel(info) != "modified*" {
		t.Errorf("expected list label 'modified*' for a clean fork whose upstream moved, got %q", listStateLabel(info))
	}
}

func TestUpstreamBaseFallsBackToSourceHash(t *testing.T) {
	// Back-compat: a marker written before ResolvedHash existed records the
	// acknowledged upstream only in Source.UpstreamContentHash. The unified
	// accessor must read it, so a fork whose parent matches that base reads
	// as quiet (no false "upstream moved").
	m := &SyncEntry{
		ContentHash: "h-marker",
		Source:      &skillSource{Owner: "chrismdp", Slug: "heartbeat", UpstreamContentHash: "h-parent"},
	}
	r := &apiSkill{UpstreamContentHash: strPtr("h-parent")}
	if got := markerUpstreamBase(m); got != "h-parent" {
		t.Errorf("expected upstream_base to fall back to Source.UpstreamContentHash, got %q", got)
	}
	if skillUpstreamMoved(m, r) {
		t.Errorf("expected upstream quiet when parent head equals the fallback base")
	}
	r.UpstreamContentHash = strPtr("h-parent-moved")
	if !skillUpstreamMoved(m, r) {
		t.Errorf("expected upstream moved when parent head differs from the fallback base")
	}
}

func TestAddForceAdvancesUpstreamBase(t *testing.T) {
	// The spec's named "add --force ResolvedHash gap": "take theirs" must
	// advance BOTH base and upstream_base to the upstream version, so both
	// axes are provably clean. The old --force path advanced only
	// Source.UpstreamContentHash and left a stale ResolvedHash, so a fork
	// kept reading "upstream available" until the next push.
	parentHead := "h-upstream-current"
	fork := &apiSkill{UpstreamContentHash: strPtr(parentHead)}

	// Marker as the OLD --force path left it: customised acknowledgement
	// (ResolvedHash) stale at an earlier upstream, Source updated only.
	buggy := &SyncEntry{
		ContentHash:  parentHead, // own copy is the taken bytes — own axis clean
		ResolvedHash: "h-upstream-old",
		Source:       &skillSource{Owner: "alice", Slug: "thing", UpstreamContentHash: parentHead},
	}
	if !skillUpstreamMoved(buggy, fork) {
		t.Fatal("test premise broken: the stale-ResolvedHash marker should read upstream-moved")
	}

	// Marker as the FIXED --force path leaves it: ResolvedHash advanced to
	// the taken upstream too. Both axes clean → no false "upstream available".
	fixed := &SyncEntry{
		ContentHash:  parentHead,
		ResolvedHash: parentHead,
		Source:       &skillSource{Owner: "alice", Slug: "thing", UpstreamContentHash: parentHead},
	}
	if skillUpstreamMoved(fixed, fork) {
		t.Error("after add --force advances ResolvedHash, the fork must read clean, not upstream-moved")
	}
}

func TestClassifyAdoptable(t *testing.T) {
	// Local dir present, no marker, server has a skill with the same name
	// and matching bytes — sync will silently link (adoptable).
	state := &SyncState{Version: 1, Skills: map[string]*SyncEntry{}}
	remote := []apiSkill{{Id: testUUID("id-1"), Name: "drive-by", ContentHash: strPtr("h-match")}}
	local := map[string]string{"drive-by": "/disk/drive-by"}
	hash := stubHasher(map[string]string{"/disk/drive-by": "h-match"})

	got := classifySkills(remote, local, state, hash)
	info := findInfo(t, got, "drive-by")
	if info.State != StateAdoptable {
		t.Errorf("expected adoptable, got %s", info.State)
	}
}

func TestClassifyConflict(t *testing.T) {
	// Local dir present, no marker, server has a skill with the same name
	// but the bytes differ — surfaces via the conflict UX.
	state := &SyncState{Version: 1, Skills: map[string]*SyncEntry{}}
	remote := []apiSkill{{Id: testUUID("id-1"), Name: "drive-by", ContentHash: strPtr("h-server")}}
	local := map[string]string{"drive-by": "/disk/drive-by"}
	hash := stubHasher(map[string]string{"/disk/drive-by": "h-local-different"})

	got := classifySkills(remote, local, state, hash)
	info := findInfo(t, got, "drive-by")
	if info.State != StateConflict {
		t.Errorf("expected conflict, got %s", info.State)
	}
}

func TestClassifyUntrackedNoRemote(t *testing.T) {
	// Local-only directory with no marker and no matching server skill.
	state := &SyncState{Version: 1, Skills: map[string]*SyncEntry{}}
	remote := []apiSkill{}
	local := map[string]string{"local-only": "/disk/local-only"}
	hash := stubHasher(map[string]string{"/disk/local-only": "h-anything"})

	got := classifySkills(remote, local, state, hash)
	info := findInfo(t, got, "local-only")
	if info.State != StateUntracked {
		t.Errorf("expected untracked, got %s", info.State)
	}
}

func TestClassifyNotLocal(t *testing.T) {
	// Server has a skill the user hasn't installed locally.
	state := &SyncState{Version: 1, Skills: map[string]*SyncEntry{}}
	remote := []apiSkill{{Id: testUUID("id-1"), Name: "available", ContentHash: strPtr("h")}}
	local := map[string]string{}
	hash := stubHasher(map[string]string{})

	got := classifySkills(remote, local, state, hash)
	info := findInfo(t, got, "available")
	if info.State != StateAvailable {
		t.Errorf("expected available, got %s", info.State)
	}
}

func TestClassifyMatchesByIDNotName(t *testing.T) {
	// Server-side rename: the marker is keyed under the old dir name
	// but the remote slug has changed. The classifier must follow the
	// skill_id, not the dir name.
	state := &SyncState{
		Version: 1,
		Skills: map[string]*SyncEntry{
			"old-name": {SkillID: testUUID("id-1").String(), ContentHash: "h"},
		},
	}
	remote := []apiSkill{{Id: testUUID("id-1"), Name: "new-name", ContentHash: strPtr("h")}}
	local := map[string]string{"old-name": "/disk/old-name"}
	hash := stubHasher(map[string]string{"/disk/old-name": "h"})

	got := classifySkills(remote, local, state, hash)
	// Result keyed under the local dir name (old-name). The Remote
	// pointer reflects the renamed server skill.
	info := findInfo(t, got, "old-name")
	if info.State != StateTracked || info.LocalDirty {
		t.Errorf("expected clean tracked after server-side rename, got state=%s dirty=%v", info.State, info.LocalDirty)
	}
	if info.Remote == nil || info.Remote.Name != "new-name" {
		t.Errorf("expected Remote.Name='new-name', got %+v", info.Remote)
	}
}

func TestClassifyEmits1RowPerSkill(t *testing.T) {
	// Sanity check: a remote that is also tracked locally must not be
	// double-counted (once via skill_id match, once via dir-name match).
	state := &SyncState{
		Version: 1,
		Skills: map[string]*SyncEntry{
			"deploy-check": {SkillID: testUUID("id-1").String(), ContentHash: "h"},
		},
	}
	remote := []apiSkill{{Id: testUUID("id-1"), Name: "deploy-check", ContentHash: strPtr("h")}}
	local := map[string]string{"deploy-check": "/disk/deploy-check"}
	hash := stubHasher(map[string]string{"/disk/deploy-check": "h"})

	got := classifySkills(remote, local, state, hash)
	if len(got) != 1 {
		t.Errorf("expected 1 row, got %d: %v", len(got), names(got))
	}
}
