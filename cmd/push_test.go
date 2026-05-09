package cmd

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestCreateTarGz(t *testing.T) {
	// Create a temp skill directory
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "test-skill")
	os.MkdirAll(skillDir, 0755)
	os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("# Test skill\nHello"), 0644)
	os.WriteFile(filepath.Join(skillDir, "helper.sh"), []byte("#!/bin/bash\necho hi"), 0755)
	os.MkdirAll(filepath.Join(skillDir, "scripts"), 0755)
	os.WriteFile(filepath.Join(skillDir, "scripts", "run.py"), []byte("print('hi')"), 0644)
	// Marker should be excluded
	os.WriteFile(filepath.Join(skillDir, ".airskills"), []byte(`{"skill_id":"x"}`), 0644)

	// Universal noise should be excluded
	os.MkdirAll(filepath.Join(skillDir, "scripts", "__pycache__"), 0755)
	os.WriteFile(filepath.Join(skillDir, "scripts", "__pycache__", "run.cpython-312.pyc"), []byte("noise"), 0644)
	os.WriteFile(filepath.Join(skillDir, ".DS_Store"), []byte("noise"), 0644)

	data, err := createTarGz(skillDir)
	if err != nil {
		t.Fatalf("createTarGz failed: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("archive is empty")
	}

	// Read back and verify contents
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("gzip.NewReader: %v", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	files := map[string]bool{}
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("tar read error: %v", err)
		}
		files[header.Name] = true
	}

	if !files["test-skill/SKILL.md"] {
		t.Error("missing SKILL.md in archive")
	}
	if !files["test-skill/helper.sh"] {
		t.Error("missing helper.sh in archive")
	}
	if !files["test-skill/scripts/run.py"] {
		t.Error("missing scripts/run.py in archive")
	}
	if files["test-skill/.airskills"] {
		t.Error(".airskills marker should be excluded from archive")
	}
	if files["test-skill/scripts/__pycache__/run.cpython-312.pyc"] {
		t.Error("__pycache__ contents should be excluded from archive")
	}
	if files["test-skill/.DS_Store"] {
		t.Error(".DS_Store should be excluded from archive")
	}
}

func TestReadSkillFilesIgnoresNoise(t *testing.T) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "noisy-skill")
	os.MkdirAll(skillDir, 0755)
	os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("# x"), 0644)
	os.MkdirAll(filepath.Join(skillDir, "scripts", "__pycache__"), 0755)
	os.WriteFile(filepath.Join(skillDir, "scripts", "__pycache__", "run.cpython-312.pyc"), []byte("noise"), 0644)
	os.WriteFile(filepath.Join(skillDir, "scripts", "run.py"), []byte("print('hi')"), 0644)
	os.WriteFile(filepath.Join(skillDir, ".DS_Store"), []byte("noise"), 0644)

	got := readSkillFiles(skillDir)

	if _, ok := got["SKILL.md"]; !ok {
		t.Error("SKILL.md missing")
	}
	if _, ok := got["scripts/run.py"]; !ok {
		t.Error("scripts/run.py missing")
	}
	for k := range got {
		if filepath.Ext(k) == ".pyc" {
			t.Errorf("expected .pyc to be ignored, got %q", k)
		}
		if filepath.Base(k) == ".DS_Store" {
			t.Errorf("expected .DS_Store to be ignored, got %q", k)
		}
	}
}

func TestExtractTarGzToMap(t *testing.T) {
	srcDir := t.TempDir()
	skillDir := filepath.Join(srcDir, "my-skill")
	os.MkdirAll(skillDir, 0755)
	os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("# My skill"), 0644)
	os.WriteFile(filepath.Join(skillDir, "data.json"), []byte(`{"key":"value"}`), 0644)

	data, err := createTarGz(skillDir)
	if err != nil {
		t.Fatalf("createTarGz: %v", err)
	}

	files, err := extractTarGzToMap(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("extractTarGzToMap: %v", err)
	}

	if string(files["SKILL.md"]) != "# My skill" {
		t.Errorf("SKILL.md = %q, want %q", string(files["SKILL.md"]), "# My skill")
	}
	if string(files["data.json"]) != `{"key":"value"}` {
		t.Errorf("data.json = %q", string(files["data.json"]))
	}
}

func TestSyncState(t *testing.T) {
	// Override HOME so sync state goes to temp dir
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)

	state := loadSyncState()
	if len(state.Skills) != 0 {
		t.Errorf("fresh sync state should be empty, got %d entries", len(state.Skills))
	}

	state.Skills["test-skill"] = &SyncEntry{SkillID: testUUID("abc-123").String(), Version: "1.0.0", Tool: "claude-code"}
	if err := saveSyncState(state); err != nil {
		t.Fatalf("save sync state: %v", err)
	}

	loaded := loadSyncState()
	entry := loaded.Skills["test-skill"]
	if entry == nil {
		t.Fatal("test-skill not found in loaded sync state")
	}
	if entry.SkillID != testUUID("abc-123").String() || entry.Version != "1.0.0" || entry.Tool != "claude-code" {
		t.Errorf("entry = %+v, want abc-123/1.0.0/claude-code", entry)
	}
}

// TestPropagatePartialRenames verifies that a manual rename in one agent
// dir is detected via content hash and propagated to other agent dirs that
// still hold the old name. Without this, push would silently create a
// duplicate skill on the server.
func TestPropagatePartialRenames(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	// Two agent dirs. Skill is at "today" in claude-code (representing the
	// renamed-already case) and at "old-name" in cursor (the dir that was
	// not renamed). Same content in both.
	claudeDir := filepath.Join(home, ".claude/skills")
	cursorDir := filepath.Join(home, ".cursor/skills")
	for _, d := range []string{claudeDir, cursorDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	skillContent := []byte("---\nname: old-name\ndescription: x\n---\nbody\n")
	if err := os.MkdirAll(filepath.Join(claudeDir, "today"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(claudeDir, "today", "SKILL.md"), skillContent, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(cursorDir, "old-name"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cursorDir, "old-name", "SKILL.md"), skillContent, 0o644); err != nil {
		t.Fatal(err)
	}

	// Compute the hash so the marker matches.
	hash := computeMerkleHash(readSkillFiles(filepath.Join(claudeDir, "today")))

	syncState := &SyncState{Skills: map[string]*SyncEntry{
		"old-name": {SkillID: testUUID("uuid-1").String(), Version: "1.0.0", ContentHash: hash, Tool: "claude-code"},
	}}
	localSkills := map[string]string{
		"today":    filepath.Join(claudeDir, "today"),
		"old-name": filepath.Join(cursorDir, "old-name"),
	}

	propagatePartialRenames(localSkills, syncState)

	if _, still := localSkills["old-name"]; still {
		t.Errorf("old-name should be removed from localSkills after propagation; got %v", localSkills)
	}
	if _, ok := localSkills["today"]; !ok {
		t.Errorf("today should remain in localSkills; got %v", localSkills)
	}
	if _, err := os.Stat(filepath.Join(cursorDir, "old-name")); !os.IsNotExist(err) {
		t.Errorf("cursor/old-name should be gone after rename, err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(cursorDir, "today")); err != nil {
		t.Errorf("cursor/today should exist after rename, err=%v", err)
	}
}

// TestRenameLocalSkillRewritesNameField verifies that `airskills mv` not
// only moves the directory but also rewrites the `name:` field inside
// SKILL.md. Without this rewrite, the next content edit + push fails
// server-side with name_slug_mismatch.
func TestRenameLocalSkillRewritesNameField(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	claudeDir := filepath.Join(home, ".claude/skills/old-name")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	skillMd := []byte("---\nname: old-name\ndescription: x\n---\n\nbody\n")
	if err := os.WriteFile(filepath.Join(claudeDir, "SKILL.md"), skillMd, 0o644); err != nil {
		t.Fatal(err)
	}

	moves, err := renameLocalSkill("old-name", "new-name")
	if err != nil {
		t.Fatalf("rename failed: %v", err)
	}
	if len(moves) == 0 {
		t.Fatal("expected at least one move")
	}

	newPath := filepath.Join(home, ".claude/skills/new-name/SKILL.md")
	got, err := os.ReadFile(newPath)
	if err != nil {
		t.Fatalf("reading post-rename SKILL.md: %v", err)
	}
	if !bytes.Contains(got, []byte("name: new-name")) {
		t.Errorf("SKILL.md `name:` field not rewritten; got: %s", got)
	}
	if bytes.Contains(got, []byte("name: old-name")) {
		t.Errorf("SKILL.md still has old name field; got: %s", got)
	}
}

// TestPropagatePartialRenames_FullOrphan covers the case where the marker
// has no surviving dirs anywhere. The function should leave it alone (the
// existing orphan-hash detector in push handles those).
func TestPropagatePartialRenames_FullOrphan(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	claudeDir := filepath.Join(home, ".claude/skills")
	if err := os.MkdirAll(filepath.Join(claudeDir, "today"), 0o755); err != nil {
		t.Fatal(err)
	}
	skillContent := []byte("---\nname: old-name\ndescription: x\n---\nbody\n")
	if err := os.WriteFile(filepath.Join(claudeDir, "today", "SKILL.md"), skillContent, 0o644); err != nil {
		t.Fatal(err)
	}
	hash := computeMerkleHash(readSkillFiles(filepath.Join(claudeDir, "today")))

	syncState := &SyncState{Skills: map[string]*SyncEntry{
		"old-name": {SkillID: testUUID("uuid-1").String(), Version: "1.0.0", ContentHash: hash, Tool: "claude-code"},
	}}
	localSkills := map[string]string{
		"today": filepath.Join(claudeDir, "today"),
		// old-name is NOT in localSkills — full orphan, should be left alone
	}

	propagatePartialRenames(localSkills, syncState)

	// localSkills should be unchanged — full-orphan rename is not this
	// function's job.
	if _, still := localSkills["today"]; !still {
		t.Errorf("today should still be in localSkills")
	}
	if len(localSkills) != 1 {
		t.Errorf("localSkills should have exactly one entry, got %v", localSkills)
	}
}
