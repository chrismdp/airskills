package cmd

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDetectAddCollision_NoLocalDir(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("USERPROFILE", tmp)
	state := &SyncState{Skills: map[string]*SyncEntry{}}
	if path, conflict := detectAddCollision("plan", "skill-id-1", state); conflict {
		t.Fatalf("expected no conflict for fresh install, got conflict at %q", path)
	}
}

func TestDetectAddCollision_SameSkillIsNoConflict(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("USERPROFILE", tmp)
	// Create a local dir for "plan" that the marker links to skill-id-1.
	for _, a := range agents {
		dir := filepath.Join(resolveGlobalDir(tmp, a.GlobalDir), "plan")
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("x"), 0644)
	}
	state := &SyncState{Skills: map[string]*SyncEntry{
		"plan": {SkillID: testUUID("skill-id-1").String()},
	}}
	if _, conflict := detectAddCollision("plan", testUUID("skill-id-1").String(), state); conflict {
		t.Fatalf("re-add of same skill should not be a conflict")
	}
}

func TestDetectAddCollision_DifferentSkillIsConflict(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("USERPROFILE", tmp)
	for _, a := range agents {
		dir := filepath.Join(resolveGlobalDir(tmp, a.GlobalDir), "plan")
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("local plan"), 0644)
	}
	state := &SyncState{Skills: map[string]*SyncEntry{
		"plan": {SkillID: testUUID("skill-id-OLD").String()},
	}}
	path, conflict := detectAddCollision("plan", "skill-id-NEW", state)
	if !conflict {
		t.Fatalf("expected conflict for different skill_id, got none")
	}
	if path == "" {
		t.Fatalf("expected conflict path, got empty")
	}
}

func TestDetectAddSilentLink_BytesMatch(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("USERPROFILE", tmp)
	skillMD := []byte("---\nname: foo\ndescription: bar\n---\nbody\n")
	for _, a := range agents {
		dir := filepath.Join(resolveGlobalDir(tmp, a.GlobalDir), "foo")
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		os.WriteFile(filepath.Join(dir, "SKILL.md"), skillMD, 0644)
	}
	remote := map[string][]byte{"SKILL.md": skillMD}
	dir, silent := detectAddSilentLink("foo", remote)
	if !silent {
		t.Fatalf("expected silent link, got false (dir=%q)", dir)
	}
	if dir == "" {
		t.Fatal("expected non-empty local dir path")
	}
}

func TestDetectAddSilentLink_BytesDiffer(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("USERPROFILE", tmp)
	for _, a := range agents {
		dir := filepath.Join(resolveGlobalDir(tmp, a.GlobalDir), "foo")
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("local"), 0644)
	}
	remote := map[string][]byte{"SKILL.md": []byte("remote")}
	if _, silent := detectAddSilentLink("foo", remote); silent {
		t.Fatal("expected no silent link when bytes differ")
	}
}

func TestDetectAddSilentLink_NoLocalDir(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("USERPROFILE", tmp)
	if _, silent := detectAddSilentLink("foo", map[string][]byte{"SKILL.md": []byte("x")}); silent {
		t.Fatal("expected no silent link when no local dir exists")
	}
}

func TestWriteConflictToTmp(t *testing.T) {
	files := map[string][]byte{
		"SKILL.md":   []byte("incoming"),
		"helper.md":  []byte("helper"),
		"sub/sub.md": []byte("nested"),
	}
	path, err := writeConflictToTmp("test-conflict-skill-xyz123", files)
	if err != nil {
		t.Fatalf("write failed: %v", err)
	}
	defer os.RemoveAll(filepath.Dir(path))

	if filepath.Base(path) != "SKILL.md" {
		t.Errorf("expected returned path to be SKILL.md, got %s", filepath.Base(path))
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read SKILL.md: %v", err)
	}
	if string(got) != "incoming" {
		t.Errorf("SKILL.md content: got %q want %q", got, "incoming")
	}
	dir := filepath.Dir(path)
	if _, err := os.Stat(filepath.Join(dir, "helper.md")); err != nil {
		t.Errorf("helper.md missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "sub", "sub.md")); err != nil {
		t.Errorf("sub/sub.md missing (nested dir not created): %v", err)
	}
}
