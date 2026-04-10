package cmd

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

func init() {
	listCmd.Flags().String("scope", "", "Filter by scope: personal, org")
	rootCmd.AddCommand(listCmd)
}

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "Show skills from your account with install status",
	Long: `Lists skills from your airskills account (personal and org).
Use --scope to filter by personal or org skills only.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		scope, _ := cmd.Flags().GetString("scope")

		client, err := newAPIClientAuto()
		if err != nil {
			return err
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
		fmt.Fprintln(w, "NAME\tSOURCE\tVERSION\tINSTALLED")
		for _, s := range skills {
			source := "personal"
			if s.OrgID != nil {
				source = "org"
			}
			installed := "no"
			if _, exists := localNames[s.Name]; exists {
				installed = "yes"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", s.Name, source, s.Version, installed)
		}
		w.Flush()
		return nil
	},
}
