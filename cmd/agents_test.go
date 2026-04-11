package cmd

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func setTestHome(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
}

func TestInstallSkillToAgents(t *testing.T) {
	// Create fake agent directories in a temp home
	tmpHome := t.TempDir()
	setTestHome(t, tmpHome)

	// Create Claude Code and Cursor skill parent dirs
	os.MkdirAll(filepath.Join(tmpHome, ".claude", "skills"), 0755)
	os.MkdirAll(filepath.Join(tmpHome, ".cursor", "skills"), 0755)

	files := map[string][]byte{
		"SKILL.md":       []byte("# Test\nHello"),
		"scripts/run.sh": []byte("#!/bin/bash\necho hi"),
	}

	installed, err := installSkillToAgents("test-skill", files)
	if err != nil {
		t.Fatalf("installSkillToAgents: %v", err)
	}

	if len(installed) < 2 {
		t.Errorf("expected at least 2 agents, got %d: %v", len(installed), installed)
	}

	// Verify files exist in Claude Code
	content, err := os.ReadFile(filepath.Join(tmpHome, ".claude", "skills", "test-skill", "SKILL.md"))
	if err != nil {
		t.Fatalf("missing SKILL.md in Claude Code: %v", err)
	}
	if string(content) != "# Test\nHello" {
		t.Errorf("content = %q", string(content))
	}

	// Verify files exist in Cursor
	content, err = os.ReadFile(filepath.Join(tmpHome, ".cursor", "skills", "test-skill", "SKILL.md"))
	if err != nil {
		t.Fatalf("missing SKILL.md in Cursor: %v", err)
	}
	if string(content) != "# Test\nHello" {
		t.Errorf("cursor content = %q", string(content))
	}

	// Verify subdirectory files
	_, err = os.ReadFile(filepath.Join(tmpHome, ".claude", "skills", "test-skill", "scripts", "run.sh"))
	if err != nil {
		t.Error("missing scripts/run.sh in Claude Code")
	}
}

func TestDetectInstalledAgents(t *testing.T) {
	tmpHome := t.TempDir()
	setTestHome(t, tmpHome)

	// No agent dirs — should return empty
	detected := detectInstalledAgents()
	if len(detected) != 0 {
		t.Errorf("expected 0 agents, got %d", len(detected))
	}

	// Create Claude Code dir
	os.MkdirAll(filepath.Join(tmpHome, ".claude"), 0755)
	detected = detectInstalledAgents()
	if len(detected) != 1 || detected[0].Key != "claude-code" {
		t.Errorf("expected [claude-code], got %v", detected)
	}
}

// writeSkillFile is a small test helper that creates parent dirs and writes a file.
func writeSkillFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// TestMirrorPropagatesEditFromNonFirstDir covers the core requirement: when a
// skill exists in two detected agent dirs and the user has edited the copy
// that isn't first in the agent registry, the edit still wins and is mirrored
// to the other copies.
func TestMirrorPropagatesEditFromNonFirstDir(t *testing.T) {
	tmpHome := t.TempDir()
	setTestHome(t, tmpHome)

	claudePath := filepath.Join(tmpHome, ".claude", "skills", "foo", "SKILL.md")
	cursorPath := filepath.Join(tmpHome, ".cursor", "skills", "foo", "SKILL.md")

	// Old (marker-matching) version lives in the first-found dir (.claude).
	writeSkillFile(t, claudePath, "# old")
	// Edited version lives in a later dir.
	writeSkillFile(t, cursorPath, "# edited")

	markerHash := computeMerkleHash(map[string][]byte{"SKILL.md": []byte("# old")})
	state := &SyncState{
		Version: 1,
		Skills: map[string]*SyncEntry{
			"foo": {SkillID: "skill-1", Version: "1.0.0", ContentHash: markerHash, Tool: "claude-code"},
		},
	}

	_, conflicts := mirrorLocalSkills(state)
	if len(conflicts) != 0 {
		t.Fatalf("unexpected conflicts: %+v", conflicts)
	}

	claude, _ := os.ReadFile(claudePath)
	if string(claude) != "# edited" {
		t.Errorf("claude copy = %q, want '# edited'", string(claude))
	}
	cursor, _ := os.ReadFile(cursorPath)
	if string(cursor) != "# edited" {
		t.Errorf("cursor copy = %q, want '# edited'", string(cursor))
	}
}

// TestMirrorCreatesInMissingDetectedDir verifies that sync mirrors a local
// skill into every *detected* agent dir, even if the skill doesn't exist
// there yet. The user explicitly asked for "all other" — not "only existing".
func TestMirrorCreatesInMissingDetectedDir(t *testing.T) {
	tmpHome := t.TempDir()
	setTestHome(t, tmpHome)

	// Both agents are installed (parent dirs exist)…
	os.MkdirAll(filepath.Join(tmpHome, ".claude", "skills"), 0755)
	os.MkdirAll(filepath.Join(tmpHome, ".cursor", "skills"), 0755)

	// …but the skill only lives in .claude.
	claudePath := filepath.Join(tmpHome, ".claude", "skills", "foo", "SKILL.md")
	writeSkillFile(t, claudePath, "# content")

	state := &SyncState{Version: 1, Skills: map[string]*SyncEntry{}}

	_, conflicts := mirrorLocalSkills(state)
	if len(conflicts) != 0 {
		t.Fatalf("unexpected conflicts: %+v", conflicts)
	}

	cursorPath := filepath.Join(tmpHome, ".cursor", "skills", "foo", "SKILL.md")
	cursor, err := os.ReadFile(cursorPath)
	if err != nil {
		t.Fatalf("expected cursor copy to be created: %v", err)
	}
	if string(cursor) != "# content" {
		t.Errorf("cursor copy = %q, want '# content'", string(cursor))
	}
}

// TestMirrorStaleSecondaryCopyLosesToFreshPrimaryEdit covers the case that
// broke the platform e2e: a previous mirror fanned content out to a
// secondary agent dir, and the user has since edited the original in place.
// The marker is stale (pre-edit), neither copy matches it, and naively
// this looks like a conflict. Mirror must fall back to newest mtime so the
// fresh edit wins and overwrites the stale secondary.
func TestMirrorStaleSecondaryCopyLosesToFreshPrimaryEdit(t *testing.T) {
	tmpHome := t.TempDir()
	setTestHome(t, tmpHome)

	claudePath := filepath.Join(tmpHome, ".claude", "skills", "foo", "SKILL.md")
	cursorPath := filepath.Join(tmpHome, ".cursor", "skills", "foo", "SKILL.md")

	// The stale secondary copy is written first, then backdated so its
	// mtime is clearly older than the user's edit.
	writeSkillFile(t, cursorPath, "# stale mirror from last run")
	staleTime := time.Now().Add(-1 * time.Hour)
	if err := os.Chtimes(cursorPath, staleTime, staleTime); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	writeSkillFile(t, claudePath, "# fresh user edit")

	// Marker references a third, older content that doesn't match either copy.
	markerHash := computeMerkleHash(map[string][]byte{"SKILL.md": []byte("# original")})
	state := &SyncState{
		Version: 1,
		Skills: map[string]*SyncEntry{
			"foo": {SkillID: "skill-1", Version: "1.0.0", ContentHash: markerHash, Tool: "claude-code"},
		},
	}

	_, conflicts := mirrorLocalSkills(state)
	if len(conflicts) != 0 {
		t.Fatalf("unexpected conflicts: %+v", conflicts)
	}

	// Both copies should now hold the fresh user edit.
	claude, _ := os.ReadFile(claudePath)
	if string(claude) != "# fresh user edit" {
		t.Errorf("claude copy = %q, want fresh user edit", string(claude))
	}
	cursor, _ := os.ReadFile(cursorPath)
	if string(cursor) != "# fresh user edit" {
		t.Errorf("cursor copy = %q, want fresh user edit", string(cursor))
	}
}

// TestMirrorAllCopiesIdenticalNoOp verifies that when every copy already
// matches, mirror reports no changes (and no conflicts).
func TestMirrorAllCopiesIdenticalNoOp(t *testing.T) {
	tmpHome := t.TempDir()
	setTestHome(t, tmpHome)

	claudePath := filepath.Join(tmpHome, ".claude", "skills", "foo", "SKILL.md")
	cursorPath := filepath.Join(tmpHome, ".cursor", "skills", "foo", "SKILL.md")
	writeSkillFile(t, claudePath, "# same")
	writeSkillFile(t, cursorPath, "# same")

	state := &SyncState{Version: 1, Skills: map[string]*SyncEntry{}}

	changes, conflicts := mirrorLocalSkills(state)
	if len(conflicts) != 0 {
		t.Fatalf("unexpected conflicts: %+v", conflicts)
	}
	for _, c := range changes {
		if len(c.written) != 0 {
			t.Errorf("expected no writes, got %+v", c)
		}
	}
}

// TestMirrorRemovesStaleFilesInTarget verifies that mirror performs a true
// replace: files present in the target but absent from the authoritative
// source are deleted, so both copies end up byte-identical.
func TestMirrorRemovesStaleFilesInTarget(t *testing.T) {
	tmpHome := t.TempDir()
	setTestHome(t, tmpHome)

	claudeDir := filepath.Join(tmpHome, ".claude", "skills", "foo")
	cursorDir := filepath.Join(tmpHome, ".cursor", "skills", "foo")

	// Source (claude) has only SKILL.md.
	writeSkillFile(t, filepath.Join(claudeDir, "SKILL.md"), "# new")
	// Target (cursor) still has an old helper file.
	writeSkillFile(t, filepath.Join(cursorDir, "SKILL.md"), "# old")
	writeSkillFile(t, filepath.Join(cursorDir, "helper.sh"), "#!/bin/sh\n")

	markerHash := computeMerkleHash(map[string][]byte{
		"SKILL.md":  []byte("# old"),
		"helper.sh": []byte("#!/bin/sh\n"),
	})
	state := &SyncState{
		Version: 1,
		Skills: map[string]*SyncEntry{
			"foo": {SkillID: "skill-1", Version: "1.0.0", ContentHash: markerHash, Tool: "claude-code"},
		},
	}

	_, conflicts := mirrorLocalSkills(state)
	if len(conflicts) != 0 {
		t.Fatalf("unexpected conflicts: %+v", conflicts)
	}

	if _, err := os.Stat(filepath.Join(cursorDir, "helper.sh")); !os.IsNotExist(err) {
		t.Errorf("stale helper.sh should have been removed from cursor, err=%v", err)
	}
	cursorSkill, _ := os.ReadFile(filepath.Join(cursorDir, "SKILL.md"))
	if string(cursorSkill) != "# new" {
		t.Errorf("cursor SKILL.md = %q, want '# new'", string(cursorSkill))
	}
}
