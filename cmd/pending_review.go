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
// moved past the user's last resolved point. Terse for human TTYs;
// verbose for agents and `--verbose` runs, including a "don't guess —
// ask the user" affordance so an agent doesn't make incorporation
// decisions on the user's behalf.
func printPendingReviewSummary(verbose bool) {
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
	if len(pending) == 0 {
		return
	}

	fmt.Println()
	if len(pending) == 1 {
		fmt.Printf("%s Pending review (1 skill)\n", yellow("M*"))
	} else {
		fmt.Printf("%s Pending review (%d skills)\n", yellow("M*"), len(pending))
	}

	if !verbose {
		for _, s := range pending {
			fmt.Printf("  %s — your customised copy needs review against the new upstream\n", s.Name)
		}
		fmt.Printf("\n  Run %s for paths, options, and resolution steps.\n", cyan("airskills sync --verbose"))
		return
	}

	for _, s := range pending {
		fmt.Println()
		fmt.Printf("%s — modified, pending review\n", s.Name)
		if s.Marker != nil && s.Marker.Source != nil {
			fmt.Printf("  upstream: %s/%s — moved past the version you last resolved\n",
				s.Marker.Source.Owner, s.Marker.Source.Slug)
		}
		fmt.Printf("  local:    ~/.claude/skills/%s (mirrored to every detected agent dir)\n", s.Name)
		fmt.Println()
		fmt.Println("  ASK THE USER before changing any of their files. Don't guess.")
		fmt.Println("  Specifically confirm:")
		fmt.Println("    - which changes from the upstream update they want to take")
		fmt.Println("    - which of their local edits must be preserved")
		fmt.Println()
		fmt.Println("  Then run ONE of:")
		if s.Marker != nil && s.Marker.Source != nil {
			fmt.Printf("    airskills add %s/%s --preview      # read upstream without installing\n",
				s.Marker.Source.Owner, s.Marker.Source.Slug)
		}
		fmt.Printf("    airskills pull --force %s            # take all upstream, drop local\n", s.Name)
		fmt.Printf("    airskills resolve %s                 # keep local as-is, acknowledge upstream\n", s.Name)
		fmt.Printf("    # or: edit local files yourself, then airskills resolve %s\n", s.Name)
	}
}
