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
// These tests pin the contract: every state in the enum is exercised at
// least once, including the two new ones (linked, untracked-conflict)
// and the modified vs modified-pending split that drives the resolve
// flow.

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
			"deploy-check": {SkillID: "id-1", ContentHash: "h-server"},
		},
	}
	remote := []apiSkill{{ID: "id-1", Name: "deploy-check", ContentHash: "h-server"}}
	local := map[string]string{"deploy-check": "/disk/deploy-check"}
	hash := stubHasher(map[string]string{"/disk/deploy-check": "h-server"})

	got := classifySkills(remote, local, state, hash)
	info := findInfo(t, got, "deploy-check")
	if info.State != StateSynced {
		t.Errorf("expected synced, got %s", info.State)
	}
}

func TestClassifyModifiedOwned(t *testing.T) {
	// Owned (no Source). Local has diverged from marker.
	state := &SyncState{
		Version: 1,
		Skills: map[string]*SyncEntry{
			"my-skill": {SkillID: "id-1", ContentHash: "h-marker"},
		},
	}
	remote := []apiSkill{{ID: "id-1", Name: "my-skill", ContentHash: "h-marker"}}
	local := map[string]string{"my-skill": "/disk/my-skill"}
	hash := stubHasher(map[string]string{"/disk/my-skill": "h-local-edits"})

	got := classifySkills(remote, local, state, hash)
	info := findInfo(t, got, "my-skill")
	if info.State != StateModified {
		t.Errorf("expected modified, got %s", info.State)
	}
}

func TestClassifyModifiedSourcedResolved(t *testing.T) {
	// Sourced. User has customised AND resolved against the current
	// upstream hash. Quiet — modified, not pending.
	state := &SyncState{
		Version: 1,
		Skills: map[string]*SyncEntry{
			"heartbeat": {
				SkillID:      "id-1",
				ContentHash:  "h-marker",
				ResolvedHash: "h-upstream-current",
				Source:       &skillSource{Owner: "chrismdp", Slug: "heartbeat"},
			},
		},
	}
	remote := []apiSkill{{ID: "id-1", Name: "heartbeat", ContentHash: "h-upstream-current"}}
	local := map[string]string{"heartbeat": "/disk/heartbeat"}
	hash := stubHasher(map[string]string{"/disk/heartbeat": "h-customised"})

	got := classifySkills(remote, local, state, hash)
	info := findInfo(t, got, "heartbeat")
	if info.State != StateModified {
		t.Errorf("expected modified (sourced, resolved), got %s", info.State)
	}
}

func TestClassifyModifiedPending(t *testing.T) {
	// Sourced, customised, AND original moved past the resolved hash.
	// Loud — pending review on next sync.
	state := &SyncState{
		Version: 1,
		Skills: map[string]*SyncEntry{
			"heartbeat": {
				SkillID:      "id-1",
				ContentHash:  "h-marker",
				ResolvedHash: "h-upstream-old",
				Source:       &skillSource{Owner: "chrismdp", Slug: "heartbeat"},
			},
		},
	}
	remote := []apiSkill{{ID: "id-1", Name: "heartbeat", ContentHash: "h-upstream-new"}}
	local := map[string]string{"heartbeat": "/disk/heartbeat"}
	hash := stubHasher(map[string]string{"/disk/heartbeat": "h-customised"})

	got := classifySkills(remote, local, state, hash)
	info := findInfo(t, got, "heartbeat")
	if info.State != StateModifiedPending {
		t.Errorf("expected modified-pending, got %s", info.State)
	}
}

func TestClassifyModifiedPendingMissingResolvedHash(t *testing.T) {
	// Backwards-compat: existing markers from before this change have
	// no ResolvedHash field. A sourced skill in that state with a
	// divergence is treated as modified-pending — the safer default.
	state := &SyncState{
		Version: 1,
		Skills: map[string]*SyncEntry{
			"heartbeat": {
				SkillID:     "id-1",
				ContentHash: "h-marker",
				Source:      &skillSource{Owner: "chrismdp", Slug: "heartbeat"},
				// ResolvedHash deliberately empty.
			},
		},
	}
	remote := []apiSkill{{ID: "id-1", Name: "heartbeat", ContentHash: "h-upstream"}}
	local := map[string]string{"heartbeat": "/disk/heartbeat"}
	hash := stubHasher(map[string]string{"/disk/heartbeat": "h-customised"})

	got := classifySkills(remote, local, state, hash)
	info := findInfo(t, got, "heartbeat")
	if info.State != StateModifiedPending {
		t.Errorf("expected modified-pending for missing ResolvedHash, got %s", info.State)
	}
}

func TestClassifyLinked(t *testing.T) {
	// Local dir present, no marker, server has a skill with the same
	// name and matching bytes — sync will silently link.
	state := &SyncState{Version: 1, Skills: map[string]*SyncEntry{}}
	remote := []apiSkill{{ID: "id-1", Name: "drive-by", ContentHash: "h-match"}}
	local := map[string]string{"drive-by": "/disk/drive-by"}
	hash := stubHasher(map[string]string{"/disk/drive-by": "h-match"})

	got := classifySkills(remote, local, state, hash)
	info := findInfo(t, got, "drive-by")
	if info.State != StateLinked {
		t.Errorf("expected linked, got %s", info.State)
	}
}

func TestClassifyUntrackedConflict(t *testing.T) {
	// Local dir present, no marker, server has a skill with the same
	// name but the bytes differ — surfaces via the conflict UX.
	state := &SyncState{Version: 1, Skills: map[string]*SyncEntry{}}
	remote := []apiSkill{{ID: "id-1", Name: "drive-by", ContentHash: "h-server"}}
	local := map[string]string{"drive-by": "/disk/drive-by"}
	hash := stubHasher(map[string]string{"/disk/drive-by": "h-local-different"})

	got := classifySkills(remote, local, state, hash)
	info := findInfo(t, got, "drive-by")
	if info.State != StateUntrackedConflict {
		t.Errorf("expected untracked-conflict, got %s", info.State)
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
	remote := []apiSkill{{ID: "id-1", Name: "available", ContentHash: "h"}}
	local := map[string]string{}
	hash := stubHasher(map[string]string{})

	got := classifySkills(remote, local, state, hash)
	info := findInfo(t, got, "available")
	if info.State != StateNotLocal {
		t.Errorf("expected not-local, got %s", info.State)
	}
}

func TestClassifyMatchesByIDNotName(t *testing.T) {
	// Server-side rename: the marker is keyed under the old dir name
	// but the remote slug has changed. The classifier must follow the
	// skill_id, not the dir name.
	state := &SyncState{
		Version: 1,
		Skills: map[string]*SyncEntry{
			"old-name": {SkillID: "id-1", ContentHash: "h"},
		},
	}
	remote := []apiSkill{{ID: "id-1", Name: "new-name", ContentHash: "h"}}
	local := map[string]string{"old-name": "/disk/old-name"}
	hash := stubHasher(map[string]string{"/disk/old-name": "h"})

	got := classifySkills(remote, local, state, hash)
	// Result keyed under the local dir name (old-name). The Remote
	// pointer reflects the renamed server skill.
	info := findInfo(t, got, "old-name")
	if info.State != StateSynced {
		t.Errorf("expected synced after server-side rename, got %s", info.State)
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
			"deploy-check": {SkillID: "id-1", ContentHash: "h"},
		},
	}
	remote := []apiSkill{{ID: "id-1", Name: "deploy-check", ContentHash: "h"}}
	local := map[string]string{"deploy-check": "/disk/deploy-check"}
	hash := stubHasher(map[string]string{"/disk/deploy-check": "h"})

	got := classifySkills(remote, local, state, hash)
	if len(got) != 1 {
		t.Errorf("expected 1 row, got %d: %v", len(got), names(got))
	}
}
