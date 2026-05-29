package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// sync must not finish silently while parked conflict copies remain — its
// "all up to date" line otherwise reads as a clean state, which is half of
// what sends users into the status→sync→status loop. It should name the
// count and redirect to status (which carries the resolution menu).
func TestPrintLingeringConflicts(t *testing.T) {
	var buf bytes.Buffer
	printLingeringConflicts(&buf, []string{"home", "dream"})
	out := buf.String()
	if !strings.Contains(out, "2") || !strings.Contains(out, "airskills status") {
		t.Fatalf("expected count and status pointer, got:\n%s", out)
	}
	if strings.Contains(out, "airskills sync") {
		t.Fatalf("must not loop the user back to sync, got:\n%s", out)
	}

	buf.Reset()
	printLingeringConflicts(&buf, nil)
	if buf.Len() != 0 {
		t.Fatalf("no conflicts → no output, got:\n%s", buf.String())
	}
}

// Idempotent parking: a parked copy that already matches the remote hash
// needs no re-download/re-write (the warning recurs against the existing
// copy); a missing copy or a changed remote does.
func TestConflictReparkIdempotency(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	t.Setenv("TMP", tmp)
	t.Setenv("TEMP", tmp)

	dir := conflictParkPath("home")
	if got := filepath.Dir(dir); filepath.Base(got) != "airskills-conflicts" {
		t.Fatalf("park path should sit under airskills-conflicts, got %s", dir)
	}

	// Nothing parked yet → must write.
	if _, need := conflictNeedsRepark("home", "anyhash"); !need {
		t.Fatal("missing parked copy should need a (re)park")
	}

	// Park a copy and compute its real hash.
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: home\n---\nremote\n"), 0600); err != nil {
		t.Fatalf("write: %v", err)
	}
	parkedHash := computeMerkleHash(readSkillFiles(dir))

	// Same remote hash → no churn.
	if !parkedConflictCurrent(dir, parkedHash) {
		t.Fatal("parked copy matching remote hash should be current")
	}
	if _, need := conflictNeedsRepark("home", parkedHash); need {
		t.Fatal("unchanged remote should NOT need a re-park")
	}

	// Remote changed → re-park.
	if _, need := conflictNeedsRepark("home", parkedHash+"x"); !need {
		t.Fatal("changed remote should need a re-park")
	}
	// Empty remote hash is never 'current'.
	if parkedConflictCurrent(dir, "") {
		t.Fatal("empty remote hash should not count as current")
	}
}

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
