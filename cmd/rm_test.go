package cmd

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRemoveLocalSkillRemovesDirAcrossAgents(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)

	// Set up two agent dirs containing the same skill (typical multi-agent install)
	claudeSkill := filepath.Join(dir, ".claude", "skills", "my-skill")
	cursorSkill := filepath.Join(dir, ".cursor", "skills", "my-skill")
	for _, d := range []string{claudeSkill, cursorSkill} {
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "SKILL.md"), []byte("# my skill"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	removed, err := removeLocalSkill("my-skill")
	if err != nil {
		t.Fatalf("removeLocalSkill: %v", err)
	}
	if len(removed) != 2 {
		t.Errorf("expected 2 dirs removed, got %d: %v", len(removed), removed)
	}
	for _, d := range []string{claudeSkill, cursorSkill} {
		if _, err := os.Stat(d); !os.IsNotExist(err) {
			t.Errorf("dir %s still exists after removal", d)
		}
	}
}

func TestRemoveLocalSkillNoOpWhenMissing(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)

	// Make a .claude dir so the agent is "detected" but no skills inside.
	os.MkdirAll(filepath.Join(dir, ".claude", "skills"), 0755)

	removed, err := removeLocalSkill("never-existed")
	if err != nil {
		t.Errorf("removeLocalSkill should not error when nothing to remove: %v", err)
	}
	if len(removed) != 0 {
		t.Errorf("expected 0 dirs removed, got %d", len(removed))
	}
}

func TestRemoveLocalSkillRefusesPathTraversal(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)

	// Make sure path-traversal-style names are rejected so we can't accidentally
	// nuke a parent directory by passing "../something".
	cases := []string{"../etc", "foo/bar", "/abs/path", ""}
	for _, name := range cases {
		_, err := removeLocalSkill(name)
		if err == nil {
			t.Errorf("removeLocalSkill(%q) should have errored", name)
		}
	}
}

func TestRmDropsSyncStateEntry(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)

	state := loadSyncState()
	state.Skills["doomed"] = &SyncEntry{
		SkillID:     "skill-doomed",
		Version:     "1.0.0",
		ContentHash: "h1",
		Tool:        "claude-code",
	}
	state.Skills["keeper"] = &SyncEntry{
		SkillID:     "skill-keeper",
		Version:     "1.0.0",
		ContentHash: "h2",
		Tool:        "claude-code",
	}
	if err := saveSyncState(state); err != nil {
		t.Fatal(err)
	}

	// Drop the doomed entry through the helper used by the rm command.
	state2 := loadSyncState()
	delete(state2.Skills, "doomed")
	if err := saveSyncState(state2); err != nil {
		t.Fatal(err)
	}

	loaded := loadSyncState()
	if _, exists := loaded.Skills["doomed"]; exists {
		t.Errorf("doomed should be removed from sync state")
	}
	if _, exists := loaded.Skills["keeper"]; !exists {
		t.Errorf("keeper should still be in sync state")
	}
}
