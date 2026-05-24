package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeToolRefSkill lays out a skill dir with the given SKILL.md body, an
// optional .askignore, and a set of on-disk files (relative paths). Returns
// the dir.
func writeToolRefSkill(t *testing.T, skillMd, askignore string, files []string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(skillMd), 0644); err != nil {
		t.Fatal(err)
	}
	if askignore != "" {
		if err := os.WriteFile(filepath.Join(dir, ".askignore"), []byte(askignore), 0644); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range files {
		full := filepath.Join(dir, filepath.FromSlash(f))
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("#!/bin/sh\necho hi\n"), 0755); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestCheckIgnoredToolReferences_WarnsWhenIgnored(t *testing.T) {
	skillMd := "---\nname: demo\nallowed-tools:\n  - Bash(scripts/pick-lens.sh:*)\n  - Read\n---\nbody\n"
	dir := writeToolRefSkill(t, skillMd, "*.sh\n", []string{"scripts/pick-lens.sh"})

	warns := checkIgnoredToolReferences(dir, newIgnoreMatcher(dir))
	if len(warns) != 1 {
		t.Fatalf("expected 1 warning, got %d: %v", len(warns), warns)
	}
	if !strings.Contains(warns[0], "scripts/pick-lens.sh") || !strings.Contains(warns[0], "*.sh") {
		t.Errorf("warning should name the file and the rule, got: %q", warns[0])
	}
}

func TestCheckIgnoredToolReferences_ScriptField(t *testing.T) {
	skillMd := "---\nname: demo\nscript: helpers/run.py\n---\nbody\n"
	dir := writeToolRefSkill(t, skillMd, "helpers/\n", []string{"helpers/run.py"})

	warns := checkIgnoredToolReferences(dir, newIgnoreMatcher(dir))
	if len(warns) != 1 {
		t.Fatalf("expected 1 warning for ignored script:, got %d: %v", len(warns), warns)
	}
}

func TestCheckIgnoredToolReferences_NoWarnWhenUploaded(t *testing.T) {
	// The referenced file exists and is NOT ignored (the rule hides logs, not scripts).
	skillMd := "---\nname: demo\nallowed-tools:\n  - Bash(scripts/pick-lens.sh:*)\n---\nbody\n"
	dir := writeToolRefSkill(t, skillMd, "*.log\n", []string{"scripts/pick-lens.sh"})

	if warns := checkIgnoredToolReferences(dir, newIgnoreMatcher(dir)); len(warns) != 0 {
		t.Errorf("expected no warning when the file is uploaded, got: %v", warns)
	}
}

func TestCheckIgnoredToolReferences_NoToolReferences(t *testing.T) {
	skillMd := "---\nname: demo\ndescription: just a skill\n---\nbody\n"
	dir := writeToolRefSkill(t, skillMd, "*.sh\n", []string{"scripts/pick-lens.sh"})

	if warns := checkIgnoredToolReferences(dir, newIgnoreMatcher(dir)); len(warns) != 0 {
		t.Errorf("expected no warning when SKILL.md declares no tool references, got: %v", warns)
	}
}

func TestCheckIgnoredToolReferences_BareToolNamesNoFalsePositive(t *testing.T) {
	// Plain tool names must never trip the check, even with an aggressive rule.
	skillMd := "---\nname: demo\nallowed-tools:\n  - Bash\n  - Read\n  - WebFetch\n---\nbody\n"
	dir := writeToolRefSkill(t, skillMd, "*\n!SKILL.md\n", nil)

	if warns := checkIgnoredToolReferences(dir, newIgnoreMatcher(dir)); len(warns) != 0 {
		t.Errorf("bare tool names should not warn, got: %v", warns)
	}
}

func TestCheckIgnoredToolReferences_NoIgnoreMatcherSuppresses(t *testing.T) {
	// Under --no-ignore push hands an empty matcher; nothing user-defined is
	// applied, so even a normally-ignored reference produces no warning.
	skillMd := "---\nname: demo\nallowed-tools:\n  - Bash(scripts/pick-lens.sh:*)\n---\nbody\n"
	dir := writeToolRefSkill(t, skillMd, "*.sh\n", []string{"scripts/pick-lens.sh"})

	if warns := checkIgnoredToolReferences(dir, &ignoreMatcher{}); len(warns) != 0 {
		t.Errorf("empty matcher (--no-ignore) should suppress warnings, got: %v", warns)
	}
}

func TestCheckIgnoredToolReferences_MissingFileNotWarned(t *testing.T) {
	// A reference to a file that doesn't exist on disk is a different problem
	// (broken reference), out of scope here — must not warn.
	skillMd := "---\nname: demo\nscript: scripts/absent.sh\n---\nbody\n"
	dir := writeToolRefSkill(t, skillMd, "*.sh\n", nil)

	if warns := checkIgnoredToolReferences(dir, newIgnoreMatcher(dir)); len(warns) != 0 {
		t.Errorf("missing referenced file should not warn, got: %v", warns)
	}
}
