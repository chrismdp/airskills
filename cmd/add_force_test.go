package cmd

import (
	"strings"
	"testing"
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
