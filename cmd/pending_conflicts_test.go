package cmd

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

func TestPendingConflictNamesFindsAddAndPullConflictDirs(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	t.Setenv("TMP", tmp)
	t.Setenv("TEMP", tmp)

	addDir := filepath.Join(tmp, "airskills-conflicts", "home")
	pullDir := filepath.Join(tmp, "airskills-conflicts-123", "dream")
	for _, dir := range []string{addDir, pullDir} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatalf("MkdirAll %s: %v", dir, err)
		}
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("remote"), 0600); err != nil {
			t.Fatalf("write manifest: %v", err)
		}
	}

	got := pendingConflictNames()
	want := []string{"dream", "home"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("pending conflicts = %v, want %v", got, want)
	}
}

func TestPendingConflictDirsReturnsAllCopiesForName(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	t.Setenv("TMP", tmp)
	t.Setenv("TEMP", tmp)

	addDir := filepath.Join(tmp, "airskills-conflicts", "home")
	pullDir := filepath.Join(tmp, "airskills-conflicts-123", "home")
	for _, dir := range []string{addDir, pullDir} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatalf("MkdirAll %s: %v", dir, err)
		}
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("remote"), 0600); err != nil {
			t.Fatalf("write manifest: %v", err)
		}
	}

	got := pendingConflictDirs("home")
	want := []string{addDir, pullDir}
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("pending conflict dirs = %v, want %v", got, want)
	}
}
