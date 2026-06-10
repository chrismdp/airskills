package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidatePullArgsRejectsPlainPositional(t *testing.T) {
	pullForceFlag = false
	pullVersionFlag = ""
	t.Cleanup(func() {
		pullForceFlag = false
		pullVersionFlag = ""
	})

	err := validatePullArgs(pullCmd, []string{"home"})
	if err == nil || (!strings.Contains(err.Error(), "accepts 0 arg") && !strings.Contains(err.Error(), "unknown command")) {
		t.Fatalf("expected plain pull positional to be rejected, got %v", err)
	}
}

func TestValidatePullArgsAllowsForceAndRequiresVersionSkill(t *testing.T) {
	t.Cleanup(func() {
		pullForceFlag = false
		pullVersionFlag = ""
	})

	pullForceFlag = true
	pullVersionFlag = ""
	if err := validatePullArgs(pullCmd, []string{"home"}); err != nil {
		t.Fatalf("pull --force should allow skill targets: %v", err)
	}

	pullForceFlag = false
	pullVersionFlag = "abc123"
	if err := validatePullArgs(pullCmd, []string{"home"}); err != nil {
		t.Fatalf("pull --version should allow exactly one skill target: %v", err)
	}
	if err := validatePullArgs(pullCmd, nil); err == nil {
		t.Fatal("pull --version should require one skill target")
	}
}

// TestPullSkipsMissingLocal verifies that when a skill is tracked in sync state
// by skill_id but its local directory has been removed, pull does NOT silently
// re-download it. Instead it warns the user and skips.
//
// This is the bug behind the rename issue: a renamed-then-edited skill leaves
// an orphan tracked entry, and the next pull resurrects the old skill.
func TestPullSkipsMissingLocal(t *testing.T) {
	state := &SyncState{
		Version: 1,
		Skills: map[string]*SyncEntry{
			"today-in-claude-code": {
				SkillID:     testUUID("skill-abc").String(),
				Version:     "1.0.0",
				ContentHash: "deadbeef",
				Tool:        "claude-code",
			},
		},
	}
	remote := []apiSkill{
		{Id: testUUID("skill-abc"), Name: "today-in-claude-code", Version: "1.0.0", ContentHash: strPtr("deadbeef")},
	}
	local := map[string]string{} // dir was deleted

	actions, warnings, _ := decidePullActions(remote, local, state, nil)

	if len(actions) != 0 {
		t.Errorf("expected 0 pull actions for missing-local skill, got %d: %+v", len(actions), actions)
	}
	if len(warnings) != 1 {
		t.Fatalf("expected 1 warning, got %d", len(warnings))
	}
	if !strings.Contains(warnings[0], "today-in-claude-code") {
		t.Errorf("warning should mention skill name, got: %q", warnings[0])
	}
	if !strings.Contains(warnings[0], "rm") {
		t.Errorf("warning should suggest 'airskills rm', got: %q", warnings[0])
	}
}

// TestPullForkConflictNotReportedMissing reproduces the 2026-06-10 feedback:
// a tracked skill whose agent copies diverged (a local fork) is removed from
// the localSkills map by runPull before classification, but it is NOT missing
// — every copy exists on disk. decidePullActions must skip the slug silently
// (printMirrorConflicts has already surfaced the fork), never emit the
// "tracked but missing locally" warning whose rm/pull hints would delete the
// skill server-side or clobber newer local edits.
// See platform/doc/changes/cli-sync-misreports-local-fork-as-missing.md.
func TestPullForkConflictNotReportedMissing(t *testing.T) {
	state := &SyncState{
		Version: 1,
		Skills: map[string]*SyncEntry{
			"telegram": {
				SkillID:     testUUID("skill-tg").String(),
				Version:     "1.0.0",
				ContentHash: "deadbeef",
				Tool:        "claude-code",
			},
		},
	}
	remote := []apiSkill{
		{Id: testUUID("skill-tg"), Name: "telegram", Version: "1.0.1", ContentHash: strPtr("cafef00d")},
	}
	local := map[string]string{} // runPull dropped the slug: forked, not missing
	forks := map[string]bool{"telegram": true}

	actions, warnings, diverged := decidePullActions(remote, local, state, forks)

	if len(actions) != 0 {
		t.Errorf("expected 0 pull actions for fork-conflicted skill, got %d: %+v", len(actions), actions)
	}
	if len(warnings) != 0 {
		t.Errorf("fork-conflicted skill must not be warned as missing, got: %v", warnings)
	}
	if len(diverged) != 0 {
		t.Errorf("fork-conflicted skill must not be registered as diverged, got: %v", diverged)
	}
}

// TestPullForkConflictUntrackedNotInstalledAsNew covers the untracked variant:
// a fork-conflicted slug with no marker must not be classified "new" (which
// would download over the in-progress local copies).
func TestPullForkConflictUntrackedNotInstalledAsNew(t *testing.T) {
	state := &SyncState{Version: 1, Skills: map[string]*SyncEntry{}}
	remote := []apiSkill{
		{Id: testUUID("skill-bs"), Name: "bluesky", Version: "1.0.0", ContentHash: strPtr("abc123")},
	}
	local := map[string]string{} // runPull dropped the slug: forked, not missing
	forks := map[string]bool{"bluesky": true}

	actions, warnings, _ := decidePullActions(remote, local, state, forks)

	if len(actions) != 0 {
		t.Errorf("expected 0 pull actions for fork-conflicted untracked skill, got %d: %+v", len(actions), actions)
	}
	if len(warnings) != 0 {
		t.Errorf("expected no warnings, got: %v", warnings)
	}
}

// TestPullDownloadsNewRemote verifies that an untracked remote skill (no local
// dir, not in sync state) is still pulled as new.
func TestPullDownloadsNewRemote(t *testing.T) {
	state := &SyncState{Version: 1, Skills: map[string]*SyncEntry{}}
	remote := []apiSkill{
		{Id: testUUID("skill-xyz"), Name: "brand-new-skill", Version: "1.0.0", ContentHash: strPtr("abc123")},
	}
	local := map[string]string{}

	actions, warnings, _ := decidePullActions(remote, local, state, nil)

	if len(actions) != 1 {
		t.Fatalf("expected 1 pull action for new skill, got %d", len(actions))
	}
	if actions[0].reason != "new" {
		t.Errorf("expected reason 'new', got %q", actions[0].reason)
	}
	if len(warnings) != 0 {
		t.Errorf("expected no warnings, got %d", len(warnings))
	}
}

// TestPullDecidesLinkedForMatchingBytes verifies that when a remote skill
// shares its name with an untracked local dir AND the bytes match exactly,
// pull queues a "linked" action — the marker gets claimed silently on the
// next sync, no download, no conflict.
//
// Replaces the older TestPullSkipsUntrackedLocalConflict, which asserted
// the silent-skip behaviour we are deliberately reversing as part of
// doc/changes/cli-untracked-collision-and-resolve.md.
func TestPullDecidesLinkedForMatchingBytes(t *testing.T) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "my-skill")
	os.MkdirAll(skillDir, 0755)
	os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("# shared content"), 0644)
	localFiles := readSkillFiles(skillDir)
	matchingHash := computeMerkleHash(localFiles)

	state := &SyncState{Version: 1, Skills: map[string]*SyncEntry{}}
	remote := []apiSkill{
		{Id: testUUID("skill-1"), Name: "my-skill", Version: "1.0.0", ContentHash: strPtr(matchingHash)},
	}
	local := map[string]string{"my-skill": skillDir}

	actions, _, _ := decidePullActions(remote, local, state, nil)
	if len(actions) != 1 {
		t.Fatalf("expected 1 action (linked), got %d: %+v", len(actions), actions)
	}
	if actions[0].reason != "linked" {
		t.Errorf("expected reason 'linked', got %q", actions[0].reason)
	}
}

// TestPullDecidesUntrackedConflictForDifferingBytes verifies that when a
// remote skill shares its name with an untracked local dir BUT the bytes
// differ, pull queues an "untracked-conflict" action — surfaced via the
// existing conflict UX so the user can merge or pick a side.
func TestPullDecidesUntrackedConflictForDifferingBytes(t *testing.T) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "my-skill")
	os.MkdirAll(skillDir, 0755)
	os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("# my local copy"), 0644)

	state := &SyncState{Version: 1, Skills: map[string]*SyncEntry{}}
	remote := []apiSkill{
		{Id: testUUID("skill-1"), Name: "my-skill", Version: "1.0.0", ContentHash: strPtr("different-server-hash")},
	}
	local := map[string]string{"my-skill": skillDir}

	actions, _, _ := decidePullActions(remote, local, state, nil)
	if len(actions) != 1 {
		t.Fatalf("expected 1 action (untracked-conflict), got %d", len(actions))
	}
	if actions[0].reason != "untracked-conflict" {
		t.Errorf("expected reason 'untracked-conflict', got %q", actions[0].reason)
	}
	if actions[0].localDir != skillDir {
		t.Errorf("expected localDir=%q, got %q", skillDir, actions[0].localDir)
	}
}

// TestPullTombstoneRelinksWhenBytesMatch verifies that a transfer tombstone
// (Deleted + MovedTo, empty skill_id) left on the transferring machine is
// reconciled by the generic untracked-dir path: because it carries no
// skill_id it can't match a tracked skill, so the still-present local dir is
// treated exactly like an untracked one. Bytes matching the new org copy →
// 'linked' (relinked silently on sync). This replaces the bespoke
// repairTransferTombstoneMarkers pass.
func TestPullTombstoneRelinksWhenBytesMatch(t *testing.T) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "home")
	os.MkdirAll(skillDir, 0755)
	os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("# shared content"), 0644)
	matchingHash := computeMerkleHash(readSkillFiles(skillDir))

	state := &SyncState{Version: 1, Skills: map[string]*SyncEntry{
		"home": {Deleted: true, MovedTo: "parsons-home/home"},
	}}
	remote := []apiSkill{
		{Id: testUUID("org-home"), Name: "home", Version: "1.0.4", ContentHash: strPtr(matchingHash)},
	}
	local := map[string]string{"home": skillDir}

	actions, _, diverged := decidePullActions(remote, local, state, nil)
	if len(actions) != 1 {
		t.Fatalf("expected 1 action (linked), got %d: %+v", len(actions), actions)
	}
	if actions[0].reason != "linked" {
		t.Errorf("expected reason 'linked', got %q", actions[0].reason)
	}
	if len(diverged) != 0 {
		t.Errorf("expected no diverged slugs, got %v", diverged)
	}
}

// TestPullTombstoneConflictsWhenBytesDiffer verifies the other half: a transfer
// tombstone whose local bytes diverge from the new org copy is surfaced as an
// 'untracked-conflict' — the same conflict UX as any diverged untracked dir,
// rather than being silently re-bound or left as a dead "upstream archived".
func TestPullTombstoneConflictsWhenBytesDiffer(t *testing.T) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "home")
	os.MkdirAll(skillDir, 0755)
	os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("# my local edits"), 0644)

	state := &SyncState{Version: 1, Skills: map[string]*SyncEntry{
		"home": {Deleted: true, MovedTo: "parsons-home/home"},
	}}
	remote := []apiSkill{
		{Id: testUUID("org-home"), Name: "home", Version: "1.0.4", ContentHash: strPtr("different-server-hash")},
	}
	local := map[string]string{"home": skillDir}

	actions, _, diverged := decidePullActions(remote, local, state, nil)
	if len(actions) != 1 {
		t.Fatalf("expected 1 action (untracked-conflict), got %d: %+v", len(actions), actions)
	}
	if actions[0].reason != "untracked-conflict" {
		t.Errorf("expected reason 'untracked-conflict', got %q", actions[0].reason)
	}
	if actions[0].localDir != skillDir {
		t.Errorf("expected localDir=%q, got %q", skillDir, actions[0].localDir)
	}
	if len(diverged) != 1 || diverged[0] != "home" {
		t.Errorf("expected diverged=[home], got %v", diverged)
	}
}

// TestPullDetectsUpdated verifies that a tracked skill whose remote hash has
// changed is queued as 'updated' (when local hash still matches the marker).
func TestPullDetectsUpdated(t *testing.T) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "tracked-skill")
	os.MkdirAll(skillDir, 0755)
	os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("# old content"), 0644)

	localFiles := readSkillFiles(skillDir)
	localHash := computeMerkleHash(localFiles)

	state := &SyncState{
		Version: 1,
		Skills: map[string]*SyncEntry{
			"tracked-skill": {
				SkillID:     testUUID("skill-1").String(),
				Version:     "1.0.0",
				ContentHash: localHash,
				Tool:        "claude-code",
			},
		},
	}
	remote := []apiSkill{
		{Id: testUUID("skill-1"), Name: "tracked-skill", Version: "1.1.0", ContentHash: strPtr("different-hash")},
	}
	local := map[string]string{"tracked-skill": skillDir}

	actions, _, _ := decidePullActions(remote, local, state, nil)
	if len(actions) != 1 {
		t.Fatalf("expected 1 action, got %d", len(actions))
	}
	if actions[0].reason != "updated" {
		t.Errorf("expected reason 'updated', got %q", actions[0].reason)
	}
}

// TestPullDetectsDiverged verifies that a tracked skill where BOTH local and
// remote have changed since the last sync is queued as 'diverged' (not silently
// overwritten).
func TestPullDetectsDiverged(t *testing.T) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "tracked-skill")
	os.MkdirAll(skillDir, 0755)
	os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("# locally edited"), 0644)

	state := &SyncState{
		Version: 1,
		Skills: map[string]*SyncEntry{
			"tracked-skill": {
				SkillID:     testUUID("skill-1").String(),
				Version:     "1.0.0",
				ContentHash: "marker-hash-from-last-sync",
				Tool:        "claude-code",
			},
		},
	}
	remote := []apiSkill{
		{Id: testUUID("skill-1"), Name: "tracked-skill", Version: "1.1.0", ContentHash: strPtr("remote-changed-hash")},
	}
	local := map[string]string{"tracked-skill": skillDir}

	actions, _, _ := decidePullActions(remote, local, state, nil)
	if len(actions) != 1 {
		t.Fatalf("expected 1 action, got %d", len(actions))
	}
	if actions[0].reason != "diverged" {
		t.Errorf("expected reason 'diverged', got %q", actions[0].reason)
	}
}

// TestPullAutoDetectClassification verifies that a tracked skill whose local
// bytes already match the remote (stale marker from manual reconciliation) is
// queued as 'auto-resolved', not 'diverged' or 'updated'.
func TestPullAutoDetectClassification(t *testing.T) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "tracked-skill")
	os.MkdirAll(skillDir, 0755)
	content := []byte("# reconciled content")
	os.WriteFile(filepath.Join(skillDir, "SKILL.md"), content, 0644)

	localFiles := readSkillFiles(skillDir)
	localHash := computeMerkleHash(localFiles)

	state := &SyncState{
		Version: 1,
		Skills: map[string]*SyncEntry{
			"tracked-skill": {
				SkillID:     testUUID("skill-1").String(),
				Version:     "1.0.0",
				ContentHash: "stale-marker-hash", // different from both local and remote
				Tool:        "claude-code",
			},
		},
	}
	// Remote hash now matches local (user reconciled manually)
	remote := []apiSkill{
		{Id: testUUID("skill-1"), Name: "tracked-skill", Version: "1.1.0", ContentHash: strPtr(localHash)},
	}
	local := map[string]string{"tracked-skill": skillDir}

	actions, warnings, _ := decidePullActions(remote, local, state, nil)
	if len(warnings) != 0 {
		t.Errorf("expected no warnings, got: %v", warnings)
	}
	if len(actions) != 1 {
		t.Fatalf("expected 1 action, got %d: %+v", len(actions), actions)
	}
	if actions[0].reason != "auto-resolved" {
		t.Errorf("expected reason 'auto-resolved', got %q", actions[0].reason)
	}
}

// TestPullAutoDetectUpdatesMarker verifies that the auto-resolved reason
// correctly updates the marker's ContentHash and Version to match remote.
func TestPullAutoDetectUpdatesMarker(t *testing.T) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "tracked-skill")
	os.MkdirAll(skillDir, 0755)
	content := []byte("# reconciled content")
	os.WriteFile(filepath.Join(skillDir, "SKILL.md"), content, 0644)

	localFiles := readSkillFiles(skillDir)
	localHash := computeMerkleHash(localFiles)

	marker := &SyncEntry{
		SkillID:     testUUID("skill-1").String(),
		Version:     "1.0.0",
		ContentHash: "stale-marker-hash",
		Tool:        "claude-code",
	}
	state := &SyncState{
		Version: 1,
		Skills:  map[string]*SyncEntry{"tracked-skill": marker},
	}
	remote := []apiSkill{
		{Id: testUUID("skill-1"), Name: "tracked-skill", Version: "1.1.0", ContentHash: strPtr(localHash)},
	}
	local := map[string]string{"tracked-skill": skillDir}

	actions, _, _ := decidePullActions(remote, local, state, nil)
	if len(actions) != 1 || actions[0].reason != "auto-resolved" {
		t.Fatalf("expected one auto-resolved action, got %+v", actions)
	}

	// Simulate what the pull executor does for auto-resolved
	p := actions[0]
	if p.marker != nil {
		p.marker.ContentHash = strDeref(p.skill.ContentHash)
		p.marker.Version = p.skill.Version
	}

	if marker.ContentHash != localHash {
		t.Errorf("marker ContentHash should be updated to remote hash %q, got %q", localHash, marker.ContentHash)
	}
	if marker.Version != "1.1.0" {
		t.Errorf("marker Version should be updated to %q, got %q", "1.1.0", marker.Version)
	}
}

// TestPullReadoptsTombstonedSkillBackInListing verifies the fix for
// cli-org-skill-wrongly-tombstoned-hides-edits.md: a marker tombstoned as
// transferred (Deleted=true) whose skill_id is back in the caller's listing
// (e.g. re-added to a skillset) is RE-ADOPTED, not silently skipped. A
// genuine v2 transfer mints a new skill_id, so a matching id is by
// construction the same skill the caller still receives. With local bytes
// matching remote, the re-adoption is a silent "auto-resolved" that clears the
// tombstone.
func TestPullReadoptsTombstonedSkillBackInListing(t *testing.T) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "tracked-skill")
	os.MkdirAll(skillDir, 0755)
	content := []byte("# some content")
	os.WriteFile(filepath.Join(skillDir, "SKILL.md"), content, 0644)

	localFiles := readSkillFiles(skillDir)
	localHash := computeMerkleHash(localFiles)

	state := &SyncState{
		Version: 1,
		Skills: map[string]*SyncEntry{
			"tracked-skill": {
				SkillID:     testUUID("skill-1").String(),
				Version:     "1.0.0",
				ContentHash: "stale-marker-hash",
				Deleted:     true, // wrongly tombstoned
				MovedTo:     "some-org/tracked-skill",
				Tool:        "claude-code",
			},
		},
	}
	remote := []apiSkill{
		{Id: testUUID("skill-1"), Name: "tracked-skill", Version: "1.1.0", ContentHash: strPtr(localHash)},
	}
	local := map[string]string{"tracked-skill": skillDir}

	actions, _, _ := decidePullActions(remote, local, state, nil)
	if len(actions) != 1 {
		t.Fatalf("expected 1 re-adopt action, got %d: %+v", len(actions), actions)
	}
	if actions[0].reason != "auto-resolved" {
		t.Errorf("expected reason 'auto-resolved' (bytes match), got %q", actions[0].reason)
	}
	if !actions[0].reAdopt {
		t.Errorf("expected reAdopt=true so the tombstone is cleared")
	}
}

// TestPullReadoptsTombstonedDivergedAppliesNormalRules verifies Chris's stated
// rule: re-adding a transferred skill regains it as normal, and if it diverges
// the normal divergence rules apply. The tombstone is cleared (reAdopt) AND the
// divergence is surfaced rather than silently shadowed.
func TestPullReadoptsTombstonedDivergedAppliesNormalRules(t *testing.T) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "tracked-skill")
	os.MkdirAll(skillDir, 0755)
	os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("# my local edits"), 0644)

	state := &SyncState{
		Version: 1,
		Skills: map[string]*SyncEntry{
			"tracked-skill": {
				SkillID:     testUUID("skill-1").String(),
				Version:     "1.0.0",
				ContentHash: "old-baseline-hash",
				Deleted:     true,
				MovedTo:     "some-org/tracked-skill",
				Tool:        "claude-code",
			},
		},
	}
	remote := []apiSkill{
		{Id: testUUID("skill-1"), Name: "tracked-skill", Version: "1.1.0", ContentHash: strPtr("different-server-hash")},
	}
	local := map[string]string{"tracked-skill": skillDir}

	actions, _, diverged := decidePullActions(remote, local, state, nil)
	if len(actions) != 1 {
		t.Fatalf("expected 1 action, got %d: %+v", len(actions), actions)
	}
	if actions[0].reason != "diverged" {
		t.Errorf("expected reason 'diverged' (normal rules), got %q", actions[0].reason)
	}
	if !actions[0].reAdopt {
		t.Errorf("expected reAdopt=true so the tombstone is cleared while the conflict is surfaced")
	}
	if len(diverged) != 1 || diverged[0] != "tracked-skill" {
		t.Errorf("expected diverged=[tracked-skill], got %v", diverged)
	}
}

// TestPullSameSkillIdRenameDoesNotTombstone verifies that a tracked skill whose
// server-side name differs from the local dir name (a rename or owner-namespace
// normalisation) but whose skill_id still matches is NOT classified as a
// transfer-away. It is the same skill the caller still receives; local edits
// must be reconciled in place by the normal rules, never tombstoned and
// abandoned. Regression guard for cli-org-skill-wrongly-tombstoned-hides-edits.md.
func TestPullSameSkillIdRenameDoesNotTombstone(t *testing.T) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "home")
	os.MkdirAll(skillDir, 0755)
	os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("# my local edits"), 0644)

	state := &SyncState{
		Version: 1,
		Skills: map[string]*SyncEntry{
			"home": {
				SkillID:     testUUID("skill-1").String(),
				Version:     "1.0.0",
				ContentHash: "baseline-hash",
				Tool:        "claude-code",
			},
		},
	}
	// Same skill_id, but the server reports a different name (legacy
	// "parsons-home-home" prefix vs the bare local dir "home").
	remote := []apiSkill{
		{Id: testUUID("skill-1"), Name: "parsons-home-home", Version: "1.1.0", ContentHash: strPtr("server-hash")},
	}
	local := map[string]string{"home": skillDir}

	actions, _, _ := decidePullActions(remote, local, state, nil)
	if len(actions) != 1 {
		t.Fatalf("expected 1 action, got %d: %+v", len(actions), actions)
	}
	if actions[0].reason == "transferred" {
		t.Errorf("a same-skill_id rename must not be classified 'transferred'")
	}
	if actions[0].reason != "diverged" {
		t.Errorf("expected normal 'diverged' rule for the in-place edit, got %q", actions[0].reason)
	}
	if actions[0].localDir != skillDir {
		t.Errorf("expected the existing local dir preserved, got %q", actions[0].localDir)
	}
	if m := state.Skills["home"]; m.Deleted || m.MovedTo != "" {
		t.Errorf("marker must not be tombstoned by classification: Deleted=%v MovedTo=%q", m.Deleted, m.MovedTo)
	}
}

// TestPullSkipsUpstreamOfLocalFork verifies the dedupe added in
// cli-org-member-suggest-via-shadow-fork.md: once push has forked an
// org-member skill into the caller's namespace and rewritten the marker,
// the upstream still appears in the caller's effective skillset listing.
// Pull should skip it — the fork is the canonical local install.
// (Without this filter, pull would flag the upstream as
// "untracked-conflict" because the local dir contains the fork's bytes,
// not the upstream's.)
func TestPullSkipsUpstreamOfLocalFork(t *testing.T) {
	upstreamID := testUUID("upstream-id")
	forkID := testUUID("fork-id")

	state := &SyncState{Version: 1, Skills: map[string]*SyncEntry{
		"shared-skill": {
			SkillID:     forkID.String(),
			Version:     "1.0.1",
			ContentHash: "fork-hash",
			Tool:        "claude-code",
			OwnerKind:   "user",
			OwnerSlug:   "callerslug",
			Source: &skillSource{
				Owner:       "upstream-org",
				Slug:        "shared-skill",
				ID:          upstreamID.String(),
				ContentHash: "upstream-hash",
			},
		},
	}}

	dir := t.TempDir()
	skillDir := filepath.Join(dir, "shared-skill")
	os.MkdirAll(skillDir, 0755)
	os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("forked body"), 0644)

	remote := []apiSkill{
		{Id: upstreamID, Name: "shared-skill", Version: "1.0.0", ContentHash: strPtr("upstream-hash")},
		{Id: forkID, Name: "shared-skill", Version: "1.0.1", ContentHash: strPtr("fork-hash")},
	}
	local := map[string]string{"shared-skill": skillDir}

	actions, _, _ := decidePullActions(remote, local, state, nil)

	// The upstream MUST be filtered out — no action for it. The fork is
	// tracked and in sync with the marker, so its action is "no-op"
	// (filtered out at the remoteHash == marker.ContentHash check).
	for _, a := range actions {
		if a.skill.Id == upstreamID {
			t.Errorf("upstream (id=%s) should have been filtered, got action %q",
				upstreamID, a.reason)
		}
	}
}

func TestPullClassifiesCleanForkWhenUpstreamAdvanced(t *testing.T) {
	upstreamID := testUUID("upstream-id")
	forkID := testUUID("fork-id")

	dir := t.TempDir()
	skillDir := filepath.Join(dir, "shared-skill")
	os.MkdirAll(skillDir, 0755)
	body := []byte("fork body")
	os.WriteFile(filepath.Join(skillDir, "SKILL.md"), body, 0644)
	localHash := computeMerkleHash(readSkillFiles(skillDir))

	state := &SyncState{Version: 1, Skills: map[string]*SyncEntry{
		"shared-skill": {
			SkillID:     forkID.String(),
			Version:     "1.0.1",
			ContentHash: localHash,
			Tool:        "claude-code",
			OwnerKind:   "user",
			OwnerSlug:   "callerslug",
			Source: &skillSource{
				Owner:       "upstream-org",
				Slug:        "shared-skill",
				ID:          upstreamID.String(),
				ContentHash: "upstream-old-hash",
			},
		},
	}}

	remote := []apiSkill{
		{
			Id: upstreamID, Name: "shared-skill", Version: "1.0.2",
			ContentHash: strPtr("upstream-new-hash"),
		},
		{
			Id: forkID, Name: "shared-skill", Version: "1.0.1",
			ContentHash: strPtr(localHash), ForkedFrom: &upstreamID,
			UpstreamContentHash: strPtr("upstream-new-hash"),
		},
	}

	actions, _, _ := decidePullActions(remote, map[string]string{"shared-skill": skillDir}, state, nil)
	if len(actions) != 1 {
		t.Fatalf("expected one upstream incorporate action, got %+v", actions)
	}
	if actions[0].reason != "upstream-updated" {
		t.Fatalf("expected reason upstream-updated, got %q", actions[0].reason)
	}
	if actions[0].skill.Id != forkID {
		t.Fatalf("expected action against fork so pull_upstream can advance it, got %s", actions[0].skill.Id)
	}
}

func TestPullClassifiesEditedForkWhenUpstreamAdvanced(t *testing.T) {
	upstreamID := testUUID("upstream-id")
	forkID := testUUID("fork-id")

	dir := t.TempDir()
	skillDir := filepath.Join(dir, "shared-skill")
	os.MkdirAll(skillDir, 0755)
	os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("locally edited fork body"), 0644)

	state := &SyncState{Version: 1, Skills: map[string]*SyncEntry{
		"shared-skill": {
			SkillID:     forkID.String(),
			Version:     "1.0.1",
			ContentHash: "fork-old-hash",
			Tool:        "claude-code",
			OwnerKind:   "user",
			OwnerSlug:   "callerslug",
			Source: &skillSource{
				Owner:       "upstream-org",
				Slug:        "shared-skill",
				ID:          upstreamID.String(),
				ContentHash: "upstream-old-hash",
			},
		},
	}}

	remote := []apiSkill{
		{
			Id: upstreamID, Name: "shared-skill", Version: "1.0.2",
			ContentHash: strPtr("upstream-new-hash"),
		},
		{
			Id: forkID, Name: "shared-skill", Version: "1.0.1",
			ContentHash: strPtr("fork-old-hash"), ForkedFrom: &upstreamID,
			UpstreamContentHash: strPtr("upstream-new-hash"),
		},
	}

	actions, _, _ := decidePullActions(remote, map[string]string{"shared-skill": skillDir}, state, nil)
	if len(actions) != 1 {
		t.Fatalf("expected one upstream-advanced action, got %+v", actions)
	}
	if actions[0].reason != "upstream-advanced" {
		t.Fatalf("expected reason upstream-advanced, got %q", actions[0].reason)
	}
	if actions[0].skill.Id != forkID {
		t.Fatalf("expected fork action, got %s", actions[0].skill.Id)
	}
}
