package cmd

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInstallSkillToAgents(t *testing.T) {
	// Create fake agent directories in a temp home
	tmpHome := t.TempDir()
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpHome)
	defer os.Setenv("HOME", origHome)

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
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpHome)
	defer os.Setenv("HOME", origHome)

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
