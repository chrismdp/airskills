package cmd

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

func init() {
	listCmd.Flags().String("scope", "", "Filter by scope: personal, org")
	listCmd.Flags().Bool("deleted", false, "Show soft-deleted skills instead of live ones")
	rootCmd.AddCommand(listCmd)
}

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "Show skills in your skillset with descriptions and install status",
	Long: `Lists skills in your airskills skillset, including the ones you
have added from other people. Shows the description, version, and whether
each skill is currently installed on this machine.

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
					deletedAt = *s.DeletedAt
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", s.Name, truncateDescription(s.Description, 60), s.Version, deletedAt)
			}
			w.Flush()
			return nil
		}

		if scope == "" {
			scope = "personal"
		}
		skills, err := client.listSkills(scope)
		if err != nil {
			return fmt.Errorf("fetching skills: %w", err)
		}

		if len(skills) == 0 {
			fmt.Println("No skills found. Run 'airskills install' to get started.")
			return nil
		}

		localNames, _ := scanSkillsFromAgents()

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "NAME\tDESCRIPTION\tVERSION\tINSTALLED")
		for _, s := range skills {
			installed := "no"
			if _, exists := localNames[s.Name]; exists {
				installed = "yes"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", s.Name, truncateDescription(s.Description, 60), s.Version, installed)
		}
		w.Flush()
		return nil
	},
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
