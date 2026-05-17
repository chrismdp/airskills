package cmd

import (
	"fmt"
	"os"
)

// verboseEnabled returns true when the caller has asked for verbose
// output. Sources, in priority order:
//   - explicit --verbose flag (forced on)
//   - AIRSKILLS_VERBOSE=1 env var (for scripted contexts)
//   - non-TTY stdout (agents and pipes get the long form by default;
//     a human terminal stays terse unless they ask)
func verboseEnabled(flag bool) bool {
	if flag {
		return true
	}
	if os.Getenv("AIRSKILLS_VERBOSE") == "1" {
		return true
	}
	return !isTTY
}

// printPendingReviewSummary surfaces sourced skills whose upstream has
// moved past the user's last resolved point. One line per skill —
// agents and humans get the same compact form. The verbose flag is
// accepted for caller compatibility but no longer changes the output.
//
// Pointing at `airskills add owner/slug --force` replaces the older
// `airskills incoming` command tree (deleted in
// platform/doc/changes/cli-kill-incoming-and-fold-into-add-force.md).
func printPendingReviewSummary(_ bool) {
	states, err := gatherSyncState()
	if err != nil {
		return
	}
	var pending []SkillStateInfo
	for _, s := range states {
		if s.State == StateModifiedPending {
			pending = append(pending, s)
		}
	}
	renderPendingReviewSummary(pending)
}

// renderPendingReviewSummary is the pure formatter — split from
// printPendingReviewSummary so tests can drive the format without
// having to mock the API client behind gatherSyncState.
func renderPendingReviewSummary(pending []SkillStateInfo) {
	if len(pending) == 0 {
		return
	}

	fmt.Println()
	if len(pending) == 1 {
		fmt.Printf("%s Pending review (1 skill)\n", yellow("M*"))
	} else {
		fmt.Printf("%s Pending review (%d skills)\n", yellow("M*"), len(pending))
	}

	for _, s := range pending {
		if s.Marker != nil && s.Marker.Source != nil {
			src := s.Marker.Source.Owner + "/" + s.Marker.Source.Slug
			fmt.Printf("  %s %s — upstream %s advanced; take it with: airskills add %s --force\n",
				yellow("!"), s.Name, src, src)
		} else {
			fmt.Printf("  %s %s — upstream advanced; take it with: airskills add <owner>/<slug> --force\n",
				yellow("!"), s.Name)
		}
	}
}
