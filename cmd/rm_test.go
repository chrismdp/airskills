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
		SkillID:     testUUID("skill-doomed").String(),
		Version:     "1.0.0",
		ContentHash: "h1",
		Tool:        "claude-code",
	}
	state.Skills["keeper"] = &SyncEntry{
		SkillID:     testUUID("skill-keeper").String(),
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

func TestRmDiscardsPendingConflictWhenNoSkillExists(t *testing.T) {
	home := t.TempDir()
	tmp := filepath.Join(home, "tmp")
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("TMPDIR", tmp)
	t.Setenv("TMP", tmp)
	t.Setenv("TEMP", tmp)

	conflictDir := filepath.Join(tmp, "airskills-conflicts", "borrowed")
	if err := os.MkdirAll(conflictDir, 0700); err != nil {
		t.Fatalf("MkdirAll conflict: %v", err)
	}
	if err := os.WriteFile(filepath.Join(conflictDir, "SKILL.md"), []byte("remote"), 0600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	oldForce := rmForce
	oldKeepRemote := rmKeepRemote
	oldKeepLocal := rmKeepLocal
	t.Cleanup(func() {
		rmForce = oldForce
		rmKeepRemote = oldKeepRemote
		rmKeepLocal = oldKeepLocal
	})
	rmForce = true
	rmKeepRemote = false
	rmKeepLocal = false

	_ = captureStdout(t, func() {
		if err := rmCmd.RunE(rmCmd, []string{"borrowed"}); err != nil {
			t.Fatalf("rm pending conflict: %v", err)
		}
	})

	if _, err := os.Stat(conflictDir); !os.IsNotExist(err) {
		t.Fatalf("pending conflict dir still exists: %v", err)
	}
}

// The dangerous case: a real installed+tracked skill AND a parked pending
// conflict copy share the name "home". Bare `rm home` deletes the real
// skill (its documented job). `rm home --pending` must discard ONLY the
// parked copy and leave the installed skill and sync state untouched —
// otherwise the resolution status recommends would destroy the user's
// skill while leaving the conflict in place.
func TestRmPendingDiscardsConflictNotInstalledSkill(t *testing.T) {
	home := t.TempDir()
	tmp := filepath.Join(home, "tmp")
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("TMPDIR", tmp)
	t.Setenv("TMP", tmp)
	t.Setenv("TEMP", tmp)

	// Real installed skill across an agent dir.
	skillDir := filepath.Join(home, ".claude", "skills", "home")
	if err := os.MkdirAll(skillDir, 0755); err != nil {
		t.Fatalf("MkdirAll skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: home\ndescription: real\n---\n# home\n"), 0644); err != nil {
		t.Fatalf("write skill: %v", err)
	}

	// Tracked in sync state.
	state := loadSyncState()
	state.Skills["home"] = &SyncEntry{
		SkillID:     testUUID("skill-home").String(),
		Version:     "1.0.0",
		ContentHash: "h1",
		Tool:        "claude-code",
	}
	if err := saveSyncState(state); err != nil {
		t.Fatalf("saveSyncState: %v", err)
	}

	// Parked pending conflict copy with the same name.
	conflictDir := filepath.Join(tmp, "airskills-conflicts", "home")
	if err := os.MkdirAll(conflictDir, 0700); err != nil {
		t.Fatalf("MkdirAll conflict: %v", err)
	}
	if err := os.WriteFile(filepath.Join(conflictDir, "SKILL.md"), []byte("remote"), 0600); err != nil {
		t.Fatalf("write conflict manifest: %v", err)
	}

	oldForce, oldKeepRemote, oldKeepLocal, oldPending := rmForce, rmKeepRemote, rmKeepLocal, rmPending
	t.Cleanup(func() {
		rmForce, rmKeepRemote, rmKeepLocal, rmPending = oldForce, oldKeepRemote, oldKeepLocal, oldPending
	})
	rmForce, rmKeepRemote, rmKeepLocal, rmPending = true, false, false, true

	_ = captureStdout(t, func() {
		if err := rmCmd.RunE(rmCmd, []string{"home"}); err != nil {
			t.Fatalf("rm --pending: %v", err)
		}
	})

	if _, err := os.Stat(conflictDir); !os.IsNotExist(err) {
		t.Fatalf("--pending must discard the parked copy; it still exists: %v", err)
	}
	if _, err := os.Stat(skillDir); err != nil {
		t.Fatalf("--pending must NOT touch the installed skill; got: %v", err)
	}
	if _, ok := loadSyncState().Skills["home"]; !ok {
		t.Fatalf("--pending must NOT drop the sync state entry")
	}
}

// --pending with no parked copy is a clear error, not a silent success
// (and must never fall through to deleting the real skill).
func TestRmPendingErrorsWhenNoConflict(t *testing.T) {
	home := t.TempDir()
	tmp := filepath.Join(home, "tmp")
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("TMPDIR", tmp)
	t.Setenv("TMP", tmp)
	t.Setenv("TEMP", tmp)

	oldForce, oldPending := rmForce, rmPending
	t.Cleanup(func() { rmForce, rmPending = oldForce, oldPending })
	rmForce, rmPending = true, true

	err := rmCmd.RunE(rmCmd, []string{"nope"})
	if err == nil {
		t.Fatal("rm --pending should error when no parked copy exists")
	}
}
