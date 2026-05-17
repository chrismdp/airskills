package cmd

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// TestIncomingCommandIsGone is the spec's "command surface gone" test:
// the airskills incoming command tree was removed in favour of
// airskills add ... --force.
func TestIncomingCommandIsGone(t *testing.T) {
	for _, c := range rootCmd.Commands() {
		if c.Name() == "incoming" {
			t.Fatalf("incoming command still registered on rootCmd; expected it deleted")
		}
	}
}

// TestAddCmdHasForceFlag asserts the --force flag is wired up on
// airskills add. The flag is the only user-facing surface for
// adopting upstream's current bytes after the incoming command tree
// was deleted; if the wiring breaks, users have no path to take
// upstream.
func TestAddCmdHasForceFlag(t *testing.T) {
	flag := addCmd.Flags().Lookup("force")
	if flag == nil {
		t.Fatal("airskills add is missing the --force flag")
	}
	if flag.Value.Type() != "bool" {
		t.Errorf("--force should be bool, got %s", flag.Value.Type())
	}
}

// TestPendingReviewSummaryUsesAddForce verifies the collapsed
// one-line-per-skill output points at `airskills add owner/slug
// --force` rather than the old `airskills incoming` surface or the
// pre-spec multi-paragraph form. Drives renderPendingReviewSummary
// directly with a synthesised pending row so we don't have to mock
// the listing API.
func TestPendingReviewSummaryUsesAddForce(t *testing.T) {
	pending := []SkillStateInfo{{
		Name:  "my-fork",
		State: StateModifiedPending,
		Local: true,
		Marker: &SyncEntry{
			Source: &skillSource{Owner: "alice", Slug: "my-fork"},
		},
	}}

	out := captureStdout(t, func() {
		renderPendingReviewSummary(pending)
	})

	if strings.Contains(out, "airskills incoming") {
		t.Errorf("output still references airskills incoming:\n%s", out)
	}
	if !strings.Contains(out, "airskills add alice/my-fork --force") {
		t.Errorf("expected new add --force hint pointing at alice/my-fork, got:\n%s", out)
	}
	// One line per skill: the verbose multi-paragraph form is gone.
	// "ASK THE USER" was the load-bearing string on the old verbose
	// branch — its absence is a useful collapse-format canary.
	if strings.Contains(out, "ASK THE USER") {
		t.Errorf("expected collapsed one-liner output, got the old multi-paragraph form:\n%s", out)
	}
}

// TestPullPersistsRestoreHintWhenNothingToDownload exists because v0.6.25
// shipped with a bug: runPull's "all up to date" early-return skipped
// saveSyncState, so a mirror-side mutation like RestoreHintShown=true was
// only kept in memory and the hint re-fired every sync. Reproduces the
// scenario via a httptest server returning the skill unchanged.
func TestPullPersistsRestoreHintWhenNothingToDownload(t *testing.T) {
	resetMirrorHintMemo()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	oldTTY := isTTY
	isTTY = false
	t.Cleanup(func() { isTTY = oldTTY })

	skillID := testUUID("restore-hint-test").String()
	skillBytes := []byte("---\nname: hint-test\ndescription: x\n---\nbody\n")
	skillHash := computeMerkleHash(map[string][]byte{"SKILL.md": skillBytes})

	// Install the same skill into two agent dirs, write a marker that
	// tracks it. Then hand-`rm` the .claude copy — mirror should refill
	// from .cursor on the next pull.
	for _, agentDir := range []string{".claude/skills", ".cursor/skills"} {
		dir := filepath.Join(home, agentDir, "hint-test")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), skillBytes, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	state := &SyncState{Version: 1, Skills: map[string]*SyncEntry{
		"hint-test": {
			SkillID:     skillID,
			Version:     "1.0.0",
			ContentHash: skillHash,
			Tool:        "claude-code",
			OwnerKind:   "user",
			OwnerSlug:   "test-user",
		},
	}}
	if err := saveSyncState(state); err != nil {
		t.Fatal(err)
	}

	// Hand-`rm` one copy so mirror has something to restore.
	if err := os.RemoveAll(filepath.Join(home, ".claude", "skills", "hint-test")); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/skills":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"skills":[{"id":%q,"name":"hint-test","slug":"hint-test","version":"1.0.0","content_hash":%q,"tool_formats":["claude-code"],"visibility":"private","dependency_count":0}]}`, skillID, skillHash)
		default:
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `[]`)
		}
	}))
	defer srv.Close()
	writeTestConfigAndToken(t, home, srv.URL)

	_ = captureStdout(t, func() {
		if err := runPull(&cobra.Command{Use: "pull"}, nil); err != nil {
			t.Fatalf("runPull: %v", err)
		}
	})

	// The .claude copy should be back AND the marker should have
	// RestoreHintShown=true on disk so the next sync stays quiet.
	if _, err := os.Stat(filepath.Join(home, ".claude", "skills", "hint-test", "SKILL.md")); err != nil {
		t.Fatalf("expected mirror to restore .claude copy: %v", err)
	}
	persisted := loadSyncState()
	entry := persisted.Skills["hint-test"]
	if entry == nil {
		t.Fatal("marker for hint-test missing after pull")
	}
	if !entry.RestoreHintShown {
		t.Fatal("RestoreHintShown not persisted after pull's 'all up to date' early-return — regression of v0.6.25 bug")
	}
}
