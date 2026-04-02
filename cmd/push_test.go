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

	state := loadSyncState()
	if len(state.Skills) != 0 {
		t.Errorf("fresh sync state should be empty, got %d entries", len(state.Skills))
	}

	state.Skills["test-skill"] = &SyncEntry{SkillID: "abc-123", Version: "1.0.0", Tool: "claude-code"}
	if err := saveSyncState(state); err != nil {
		t.Fatalf("save sync state: %v", err)
	}

	loaded := loadSyncState()
	entry := loaded.Skills["test-skill"]
	if entry == nil {
		t.Fatal("test-skill not found in loaded sync state")
	}
	if entry.SkillID != "abc-123" || entry.Version != "1.0.0" || entry.Tool != "claude-code" {
		t.Errorf("entry = %+v, want abc-123/1.0.0/claude-code", entry)
	}
}
