package cmd

import (
	"os"
	"path/filepath"
	"testing"
)

// A marker created after the mirror pass (first push of a new skill, a pull
// install, a fork drain) must get its per-copy ledger seeded immediately —
// otherwise an edit made before the next CLI run is misclassified as a
// divergence-without-history fork ("edited differently in two agent
// copies") and never pushed.
func TestSeedCopyLedgerFromDiskStampsAllCopies(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	body := "---\nname: foo\ndescription: d\n---\n# v1\n"
	for _, dir := range []string{".claude", ".cursor"} {
		p := filepath.Join(home, dir, "skills", "foo")
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(p, "SKILL.md"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	marker := &SyncEntry{SkillID: "x", ContentHash: "pushed-hash"}
	seedCopyLedgerFromDisk(marker, "foo")

	if len(marker.Copies) != 2 {
		t.Fatalf("ledger should cover both copies, got %+v", marker.Copies)
	}
	wantHash := computeMerkleHash(readSkillFiles(filepath.Join(home, ".claude", "skills", "foo")))
	for dir, cs := range marker.Copies {
		if cs.Hash != wantHash {
			t.Errorf("copy %s seeded with %q, want each copy's own current hash %q", dir, cs.Hash, wantHash)
		}
	}
}

// Seeding must never clobber a ledger the mirror is already maintaining.
func TestSeedCopyLedgerFromDiskNoOpWhenLedgerExists(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	existing := map[string]CopyState{"/somewhere": {Hash: "h"}}
	marker := &SyncEntry{SkillID: "x", Copies: existing}
	seedCopyLedgerFromDisk(marker, "foo")

	if len(marker.Copies) != 1 || marker.Copies["/somewhere"].Hash != "h" {
		t.Errorf("existing ledger must be left alone, got %+v", marker.Copies)
	}
}
