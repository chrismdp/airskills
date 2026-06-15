package cmd

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
)

func init() {
	listCmd.Flags().String("scope", "", "Filter by scope: personal, org")
	listCmd.Flags().Bool("deleted", false, "Show soft-deleted skills instead of live ones")
	rootCmd.AddCommand(listCmd)
}

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "Show skills in your skillset with descriptions and sync state",
	Long: `Lists skills in your airskills skillset, including the ones you
have added from other people. Shows the description, version, and the
sync state of each skill on this machine: synced, modified, modified*
(sourced + customised + original moved), untracked, or — (server-only).

Use --scope org to filter to org skills only.
Use --deleted to show soft-deleted skills.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		scope, _ := cmd.Flags().GetString("scope")
		showDeleted, _ := cmd.Flags().GetBool("deleted")

		client, err := newAPIClientAuto()
		if err != nil {
			return err
		}

		if showDeleted {
			skills, err := client.listDeletedSkills()
			if err != nil {
				return fmt.Errorf("fetching deleted skills: %w", err)
			}
			if len(skills) == 0 {
				fmt.Println("No deleted skills found.")
				return nil
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "NAME\tDESCRIPTION\tVERSION\tDELETED AT")
			for _, s := range skills {
				deletedAt := ""
				if s.DeletedAt != nil {
					deletedAt = s.DeletedAt.Format(time.RFC3339)
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", s.Name, truncateDescription(strDeref(s.Description), 60), s.Version, deletedAt)
			}
			w.Flush()
			return nil
		}

		var skills []apiSkill
		if scope == "" {
			skills, _, err = client.listPersonalSkillsInSkillset(rememberedSkillsetSlug())
		} else {
			skills, err = client.listSkills(scope)
		}
		if err != nil {
			return fmt.Errorf("fetching skills: %w", err)
		}

		if len(skills) == 0 {
			fmt.Println("No skills found. Run 'airskills sync' to get started.")
			return nil
		}

		localNames, _ := scanSkillsFromAgents()
		syncState := loadSyncState()
		hashLocal := func(p string) string { return computeMerkleHash(readSkillFiles(p)) }
		states := classifySkills(skills, localNames, syncState, hashLocal)
		infoByName := map[string]SkillStateInfo{}
		for _, st := range states {
			infoByName[st.Name] = st
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "NAME\tDESCRIPTION\tVERSION\tSTATE")
		for _, s := range skills {
			// One row per skill: hidden backup forks never render — their
			// id only ever appears inside the marker's Backup ref.
			if isBackupRow(&s) {
				continue
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", s.Name, truncateDescription(strDeref(s.Description), 60), s.Version, listStateLabel(infoByName[s.Name]))
		}
		w.Flush()
		return nil
	},
}

// listStateLabel maps one unified-model row to the short label that appears
// in `airskills list`'s STATE column. It projects the presence state and the
// divergence booleans onto the same five labels the column always used:
//
//   - "synced"    — tracked, no local edits and no upstream move
//   - "modified"  — tracked with local edits not yet pushed
//   - "modified*" — a fork whose upstream moved past what you acknowledged
//     (shown whether or not you also have local edits — this is the case the
//     old 1-D encoding could not name, so `list` used to say "synced" while
//     `status` said "upstream": spec bug #1)
//   - "local changes" — an overlay (non-owned skill) with standing local
//     edits, backed up server-side; "+ suggestion pending" when one is open.
//     ONE row for one skill — the hidden backup fork never renders.
//   - "untracked" — presence states with no marker (untracked / adoptable /
//     conflict), which a user reading `list` just sees as untracked dirs the
//     next sync resolves
//   - "—"         — server-only (available)
func listStateLabel(info SkillStateInfo) string {
	switch info.State {
	case StateTracked:
		switch {
		case info.UpstreamMoved:
			return "modified*"
		case info.Overlay && info.OverlayDiverged:
			if info.Marker != nil && info.Marker.SuggestionID != "" {
				return "local changes (suggestion pending)"
			}
			return "local changes"
		case info.LocalDirty:
			return "modified"
		case isPersonalSubscriptionMarker(info.Marker):
			// A clean subscription — added from someone else, not owned. Show
			// the upstream owner so it never reads as one of your own skills.
			return "added from " + subscriptionOwnerLabel(info.Marker)
		default:
			return "synced"
		}
	case StateUntracked, StateAdoptable, StateConflict:
		return "untracked"
	case StateAvailable:
		return "—"
	}
	return "—"
}

// truncateDescription shortens a description for the list table, collapsing
// internal whitespace and ending with an ellipsis if it exceeds max runes.
func truncateDescription(desc string, max int) string {
	desc = strings.Join(strings.Fields(desc), " ")
	if desc == "" {
		return "—"
	}
	runes := []rune(desc)
	if len(runes) <= max {
		return desc
	}
	return string(runes[:max-1]) + "…"
}
