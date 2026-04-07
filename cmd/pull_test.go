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

// TestPullSkipsUntrackedLocalConflict verifies that when a remote skill has the
// same name as an untracked local dir, pull leaves the local dir alone.
func TestPullSkipsUntrackedLocalConflict(t *testing.T) {
	state := &SyncState{Version: 1, Skills: map[string]*SyncEntry{}}
	remote := []apiSkill{
		{ID: "skill-1", Name: "my-skill", Version: "1.0.0", ContentHash: "h1"},
	}
	local := map[string]string{"my-skill": "/tmp/fake"}

	actions, _ := decidePullActions(remote, local, state)
	if len(actions) != 0 {
		t.Errorf("expected 0 actions (untracked local with same name), got %d", len(actions))
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
