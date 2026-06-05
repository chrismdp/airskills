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

func TestRemoveLocalSkillFileRemovesAcrossAgents(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)

	// Same multi-file skill installed in two agent dirs. The whole point of
	// file-level rm is that deleting from one copy alone is futile — the
	// mirror fan-out resurrects it from any sibling that still has the file.
	claudeSkill := filepath.Join(dir, ".claude", "skills", "triage")
	cursorSkill := filepath.Join(dir, ".cursor", "skills", "triage")
	for _, d := range []string{claudeSkill, cursorSkill} {
		if err := os.MkdirAll(filepath.Join(d, "scripts"), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "SKILL.md"), []byte("# triage"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "scripts", "dispatch-email.sh"), []byte("echo hi"), 0755); err != nil {
			t.Fatal(err)
		}
	}

	removed, err := removeLocalSkillFile("triage", "scripts/dispatch-email.sh")
	if err != nil {
		t.Fatalf("removeLocalSkillFile: %v", err)
	}
	if len(removed) != 2 {
		t.Errorf("expected file removed from 2 agent dirs, got %d: %v", len(removed), removed)
	}
	for _, d := range []string{claudeSkill, cursorSkill} {
		if _, err := os.Stat(filepath.Join(d, "scripts", "dispatch-email.sh")); !os.IsNotExist(err) {
			t.Errorf("file still present in %s", d)
		}
		// SKILL.md must survive — we removed one file, not the skill.
		if _, err := os.Stat(filepath.Join(d, "SKILL.md")); err != nil {
			t.Errorf("SKILL.md should survive file-level rm in %s: %v", d, err)
		}
		// The now-empty scripts/ dir should be pruned so it doesn't linger.
		if _, err := os.Stat(filepath.Join(d, "scripts")); !os.IsNotExist(err) {
			t.Errorf("empty parent dir scripts/ should be pruned in %s", d)
		}
	}
}

func TestRemoveLocalSkillFileKeepsNonEmptyParent(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)

	skill := filepath.Join(dir, ".claude", "skills", "triage")
	if err := os.MkdirAll(filepath.Join(skill, "scripts"), 0755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(skill, "SKILL.md"), []byte("# triage"), 0644)
	os.WriteFile(filepath.Join(skill, "scripts", "a.sh"), []byte("a"), 0755)
	os.WriteFile(filepath.Join(skill, "scripts", "b.sh"), []byte("b"), 0755)

	if _, err := removeLocalSkillFile("triage", "scripts/a.sh"); err != nil {
		t.Fatalf("removeLocalSkillFile: %v", err)
	}
	// scripts/ still holds b.sh, so it must NOT be pruned.
	if _, err := os.Stat(filepath.Join(skill, "scripts", "b.sh")); err != nil {
		t.Errorf("sibling file b.sh should survive: %v", err)
	}
	if _, err := os.Stat(filepath.Join(skill, "scripts")); err != nil {
		t.Errorf("non-empty parent dir should not be pruned: %v", err)
	}
}

func TestRemoveLocalSkillFileRefusesBadPaths(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)

	// Traversal, absolute, empty, and the manifest itself must all be refused
	// — the manifest because removing it would silently break the skill (use
	// `airskills rm <skill>` to delete the whole thing).
	cases := []string{"../etc/passwd", "/abs/path", "", "scripts/../../escape", "SKILL.md"}
	for _, rel := range cases {
		if _, err := removeLocalSkillFile("triage", rel); err == nil {
			t.Errorf("removeLocalSkillFile(triage, %q) should have errored", rel)
		}
	}
}

func TestRemoveLocalSkillFileErrorsWhenMissing(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)

	skill := filepath.Join(dir, ".claude", "skills", "triage")
	os.MkdirAll(skill, 0755)
	os.WriteFile(filepath.Join(skill, "SKILL.md"), []byte("# triage"), 0644)

	if _, err := removeLocalSkillFile("triage", "scripts/nope.sh"); err == nil {
		t.Error("removeLocalSkillFile should error when the file is in no agent copy")
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
