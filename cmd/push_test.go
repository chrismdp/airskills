package cmd

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
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

func TestWriteAndReadMarker(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".airskills")

	marker := &airskillsMarker{SkillID: "abc-123", Version: "1.0.0", Tool: "claude-code"}
	writeMarker(path, marker)

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read marker: %v", err)
	}

	var got airskillsMarker
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("parse marker: %v", err)
	}

	if got.SkillID != "abc-123" || got.Version != "1.0.0" || got.Tool != "claude-code" {
		t.Errorf("marker = %+v, want abc-123/1.0.0/claude-code", got)
	}
}
