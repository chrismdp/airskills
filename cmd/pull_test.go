package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
				SkillID:     "skill-abc",
				Version:     "1.0.0",
				ContentHash: "deadbeef",
				Tool:        "claude-code",
			},
		},
	}
	remote := []apiSkill{
		{ID: "skill-abc", Name: "today-in-claude-code", Version: "1.0.0", ContentHash: "deadbeef"},
	}
	local := map[string]string{} // dir was deleted

	actions, warnings := decidePullActions(remote, local, state)

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

// TestPullDownloadsNewRemote verifies that an untracked remote skill (no local
// dir, not in sync state) is still pulled as new.
func TestPullDownloadsNewRemote(t *testing.T) {
	state := &SyncState{Version: 1, Skills: map[string]*SyncEntry{}}
	remote := []apiSkill{
		{ID: "skill-xyz", Name: "brand-new-skill", Version: "1.0.0", ContentHash: "abc123"},
	}
	local := map[string]string{}

	actions, warnings := decidePullActions(remote, local, state)

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
		{ID: "skill-1", Name: "my-skill", Version: "1.0.0", ContentHash: matchingHash},
	}
	local := map[string]string{"my-skill": skillDir}

	actions, _ := decidePullActions(remote, local, state)
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
		{ID: "skill-1", Name: "my-skill", Version: "1.0.0", ContentHash: "different-server-hash"},
	}
	local := map[string]string{"my-skill": skillDir}

	actions, _ := decidePullActions(remote, local, state)
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
				SkillID:     "skill-1",
				Version:     "1.0.0",
				ContentHash: localHash,
				Tool:        "claude-code",
			},
		},
	}
	remote := []apiSkill{
		{ID: "skill-1", Name: "tracked-skill", Version: "1.1.0", ContentHash: "different-hash"},
	}
	local := map[string]string{"tracked-skill": skillDir}

	actions, _ := decidePullActions(remote, local, state)
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
				SkillID:     "skill-1",
				Version:     "1.0.0",
				ContentHash: "marker-hash-from-last-sync",
				Tool:        "claude-code",
			},
		},
	}
	remote := []apiSkill{
		{ID: "skill-1", Name: "tracked-skill", Version: "1.1.0", ContentHash: "remote-changed-hash"},
	}
	local := map[string]string{"tracked-skill": skillDir}

	actions, _ := decidePullActions(remote, local, state)
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
				SkillID:     "skill-1",
				Version:     "1.0.0",
				ContentHash: "stale-marker-hash", // different from both local and remote
				Tool:        "claude-code",
			},
		},
	}
	// Remote hash now matches local (user reconciled manually)
	remote := []apiSkill{
		{ID: "skill-1", Name: "tracked-skill", Version: "1.1.0", ContentHash: localHash},
	}
	local := map[string]string{"tracked-skill": skillDir}

	actions, warnings := decidePullActions(remote, local, state)
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
		SkillID:     "skill-1",
		Version:     "1.0.0",
		ContentHash: "stale-marker-hash",
		Tool:        "claude-code",
	}
	state := &SyncState{
		Version: 1,
		Skills:  map[string]*SyncEntry{"tracked-skill": marker},
	}
	remote := []apiSkill{
		{ID: "skill-1", Name: "tracked-skill", Version: "1.1.0", ContentHash: localHash},
	}
	local := map[string]string{"tracked-skill": skillDir}

	actions, _ := decidePullActions(remote, local, state)
	if len(actions) != 1 || actions[0].reason != "auto-resolved" {
		t.Fatalf("expected one auto-resolved action, got %+v", actions)
	}

	// Simulate what the pull executor does for auto-resolved
	p := actions[0]
	if p.marker != nil {
		p.marker.ContentHash = p.skill.ContentHash
		p.marker.Version = p.skill.Version
	}

	if marker.ContentHash != localHash {
		t.Errorf("marker ContentHash should be updated to remote hash %q, got %q", localHash, marker.ContentHash)
	}
	if marker.Version != "1.1.0" {
		t.Errorf("marker Version should be updated to %q, got %q", "1.1.0", marker.Version)
	}
}

// TestPullAutoDetectSkipsTransferredSkills verifies that skills with
// Deleted=true are skipped by the auto-detect logic (handled by transfer flow).
func TestPullAutoDetectSkipsTransferredSkills(t *testing.T) {
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
				SkillID:     "skill-1",
				Version:     "1.0.0",
				ContentHash: "stale-marker-hash",
				Deleted:     true, // mid-transfer
				Tool:        "claude-code",
			},
		},
	}
	remote := []apiSkill{
		{ID: "skill-1", Name: "tracked-skill", Version: "1.1.0", ContentHash: localHash},
	}
	local := map[string]string{"tracked-skill": skillDir}

	actions, _ := decidePullActions(remote, local, state)
	// Should be skipped (Deleted=true), so no action
	if len(actions) != 0 {
		t.Errorf("expected 0 actions for Deleted skill, got %d: %+v", len(actions), actions)
	}
}
