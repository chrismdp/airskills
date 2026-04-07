package cmd

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRenameLocalSkillMovesDirAcrossAgents(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)

	claudeOld := filepath.Join(dir, ".claude", "skills", "old-name")
	cursorOld := filepath.Join(dir, ".cursor", "skills", "old-name")
	for _, d := range []string{claudeOld, cursorOld} {
		os.MkdirAll(d, 0755)
		os.WriteFile(filepath.Join(d, "SKILL.md"), []byte("# old"), 0644)
	}

	moves, err := renameLocalSkill("old-name", "new-name")
	if err != nil {
		t.Fatalf("renameLocalSkill: %v", err)
	}
	if len(moves) != 2 {
		t.Errorf("expected 2 moves, got %d: %v", len(moves), moves)
	}

	for _, oldDir := range []string{claudeOld, cursorOld} {
		if _, err := os.Stat(oldDir); !os.IsNotExist(err) {
			t.Errorf("old dir %s should be gone", oldDir)
		}
	}
	for _, newDir := range []string{
		filepath.Join(dir, ".claude", "skills", "new-name"),
		filepath.Join(dir, ".cursor", "skills", "new-name"),
	} {
		if _, err := os.Stat(filepath.Join(newDir, "SKILL.md")); err != nil {
			t.Errorf("new dir %s missing SKILL.md: %v", newDir, err)
		}
	}
}

func TestRenameLocalSkillRefusesExistingTarget(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)

	src := filepath.Join(dir, ".claude", "skills", "src")
	dst := filepath.Join(dir, ".claude", "skills", "dst")
	os.MkdirAll(src, 0755)
	os.MkdirAll(dst, 0755)
	os.WriteFile(filepath.Join(src, "SKILL.md"), []byte("# src"), 0644)
	os.WriteFile(filepath.Join(dst, "SKILL.md"), []byte("# dst"), 0644)

	_, err := renameLocalSkill("src", "dst")
	if err == nil {
		t.Errorf("expected error when target exists")
	}

	// Source must remain intact (no partial moves)
	if _, err := os.Stat(filepath.Join(src, "SKILL.md")); err != nil {
		t.Errorf("source should not have been touched: %v", err)
	}
}

func TestRenameLocalSkillNoSourceFound(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
	os.MkdirAll(filepath.Join(dir, ".claude", "skills"), 0755)

	_, err := renameLocalSkill("missing", "anything")
	if err == nil {
		t.Errorf("expected error when source missing")
	}
}

func TestRenameLocalSkillValidatesNames(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)

	cases := [][2]string{
		{"", "new"},
		{"old", ""},
		{"../etc", "new"},
		{"old", "../etc"},
		{"old", "foo/bar"},
	}
	for _, c := range cases {
		if _, err := renameLocalSkill(c[0], c[1]); err == nil {
			t.Errorf("renameLocalSkill(%q, %q) should have errored", c[0], c[1])
		}
	}
}

func TestMvUpdatesSyncStateKey(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)

	state := loadSyncState()
	state.Skills["old-name"] = &SyncEntry{
		SkillID:     "skill-1",
		Version:     "1.0.0",
		ContentHash: "h1",
		Tool:        "claude-code",
	}
	saveSyncState(state)

	// Simulate what the mv command does to sync state.
	state2 := loadSyncState()
	entry := state2.Skills["old-name"]
	delete(state2.Skills, "old-name")
	state2.Skills["new-name"] = entry
	saveSyncState(state2)

	loaded := loadSyncState()
	if _, exists := loaded.Skills["old-name"]; exists {
		t.Errorf("old-name should be removed")
	}
	moved := loaded.Skills["new-name"]
	if moved == nil {
		t.Fatalf("new-name not found")
	}
	if moved.SkillID != "skill-1" {
		t.Errorf("SkillID lost across rename: got %q", moved.SkillID)
	}
}
