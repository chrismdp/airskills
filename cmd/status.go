package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

func init() {
	statusCmd.Flags().BoolP("quiet", "q", false, "Only output when updates are available")
	rootCmd.AddCommand(statusCmd)
}

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Check for skill updates and new remote skills",
	Long: `Compares your local skills with your airskills.ai account.
Shows new skills available for download, and skills with remote updates.

Designed for shell startup hook:
  [airskills] 2 updated, 3 new. Run 'airskills sync' to sync.

Use --quiet to suppress output when everything is up to date.`,
	RunE: runStatus,
}

func runStatus(cmd *cobra.Command, args []string) error {
	quiet, _ := cmd.Flags().GetBool("quiet")

	client, err := newAPIClientAuto()
	if err != nil {
		if !quiet {
			fmt.Fprintf(os.Stderr, "[airskills] %v\n", err)
		}
		return nil
	}

	localSkills, err := scanSkillsFromAgents()
	if err != nil && !quiet {
		fmt.Fprintf(os.Stderr, "[airskills] Could not scan local skills: %v\n", err)
	}

	remoteSkills, err := client.listSkills("")
	if err != nil {
		if !quiet {
			fmt.Fprintf(os.Stderr, "[airskills] Could not fetch remote skills: %v\n", err)
		}
		return nil
	}

	var notInstalled []string
	var updated []string
	var localOnly []string

	// Check remote skills vs local
	remoteByName := map[string]bool{}
	for _, remote := range remoteSkills {
		remoteByName[remote.Name] = true
		localPath, exists := localSkills[remote.Name]
		if !exists {
			notInstalled = append(notInstalled, remote.Name)
			continue
		}

		// Check version via marker
		data, err := os.ReadFile(localPath + "/.airskills")
		if err != nil {
			continue
		}
		var marker airskillsMarker
		if err := parseJSON(data, &marker); err != nil {
			continue
		}
		if marker.ContentHash != "" && remote.ContentHash != "" && marker.ContentHash != remote.ContentHash {
			updated = append(updated, fmt.Sprintf("%s (content changed)", remote.Name))
		} else if marker.ContentHash == "" && marker.Version != remote.Version {
			// Fallback for old markers without content hash
			updated = append(updated, fmt.Sprintf("%s (v%s → v%s)", remote.Name, marker.Version, remote.Version))
		}
	}

	// Check local skills not in remote (need pushing)
	for name := range localSkills {
		if !remoteByName[name] {
			localOnly = append(localOnly, name)
		}
	}

	if len(notInstalled) == 0 && len(updated) == 0 && len(localOnly) == 0 {
		if !quiet {
			fmt.Printf("[airskills] All %d skills in sync.\n", len(remoteSkills))
		}
		return nil
	}

	if len(localOnly) > 0 {
		fmt.Printf("[airskills] %d skill(s) only on this machine (need push):\n", len(localOnly))
		for _, name := range localOnly {
			fmt.Printf("  ↑ %s\n", name)
		}
	}

	if len(notInstalled) > 0 {
		fmt.Printf("[airskills] %d skill(s) not on this machine (need pull):\n", len(notInstalled))
		for _, name := range notInstalled {
			fmt.Printf("  ↓ %s\n", name)
		}
	}

	if len(updated) > 0 {
		fmt.Printf("[airskills] %d skill(s) with version differences:\n", len(updated))
		for _, desc := range updated {
			fmt.Printf("  ~ %s\n", desc)
		}
	}

	fmt.Println("\nRun 'airskills sync' to push and pull.")
	return nil
}
