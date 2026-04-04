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

	remoteSkills, err := client.listSkills("personal")
	if err != nil {
		if !quiet {
			fmt.Fprintf(os.Stderr, "[airskills] Could not fetch remote skills: %v\n", err)
		}
		return nil
	}

	syncState := loadSyncState()

	// Build reverse map: skill_id → dir name
	skillIdToName := map[string]string{}
	for name, entry := range syncState.Skills {
		if entry.SkillID != "" {
			skillIdToName[entry.SkillID] = name
		}
	}

	var notInstalled []string
	var updated []string
	var localOnly []string

	// Check remote skills vs local
	remoteByName := map[string]bool{}
	for _, remote := range remoteSkills {
		remoteByName[remote.Name] = true

		// Match by skill_id first (survives renames), then by name
		trackedName := ""
		if name, ok := skillIdToName[remote.ID]; ok {
			trackedName = name
		}

		if trackedName != "" {
			if _, exists := localSkills[trackedName]; !exists {
				continue // dir removed locally
			}
			marker := syncState.Skills[trackedName]
			if marker.ContentHash != "" && remote.ContentHash != "" && marker.ContentHash != remote.ContentHash {
				updated = append(updated, fmt.Sprintf("%s (content changed)", trackedName))
			} else if marker.ContentHash == "" && marker.Version != remote.Version {
				updated = append(updated, fmt.Sprintf("%s (v%s → v%s)", trackedName, marker.Version, remote.Version))
			}
			continue
		}

		if _, exists := localSkills[remote.Name]; !exists {
			notInstalled = append(notInstalled, remote.Name)
		}
	}

	// Check local skills not in remote (need pushing)
	for name := range localSkills {
		if !remoteByName[name] {
			localOnly = append(localOnly, name)
		}
	}

	// Show logo + version info
	if !quiet {
		fmt.Println(cyan(`   _          _    _ _ _
  (_)        | |  (_) | |
__ _ _ _ __ ___| | ___| | |___
/ _` + "`" + ` | | '__/ __| |/ / | | / __|
| (_| | | |  \__ \   <| | | \__ \
\__,_|_|_|  |___/_|\_\_|_|_|___/`))
		fmt.Println()

		if healthBody, err := client.get("/api/v1/health"); err == nil {
			var health struct {
				Version   string `json:"version"`
				Commit    string `json:"commit"`
				LatestCLI string `json:"latest_cli"`
			}
			if parseJSON(healthBody, &health) == nil {
				commitStr := health.Commit
				if len(commitStr) > 7 {
					commitStr = commitStr[:7]
				}
				fmt.Printf("%s platform %s %s | cli %s\n",
					cyan("[airskills]"), health.Version, dim("("+commitStr+")"), version)
				if health.LatestCLI != "" && isNewer(health.LatestCLI, version) && version != "dev" {
					fmt.Printf("%s %s\n",
						yellow("Update available:"),
						fmt.Sprintf("%s → %s. Run 'airskills self-update'.", version, health.LatestCLI))
				}
			}
		}
	}

	if len(notInstalled) == 0 && len(updated) == 0 && len(localOnly) == 0 {
		if !quiet {
			fmt.Printf("%s All %d skills in sync.\n", green("✓"), len(remoteSkills))
		}
		return nil
	}

	if len(localOnly) > 0 {
		fmt.Printf("\n%s %d skill(s) only on this machine %s:\n", yellow("↑"), len(localOnly), dim("(need push)"))
		for _, name := range localOnly {
			fmt.Printf("  %s %s\n", yellow("↑"), name)
		}
	}

	if len(notInstalled) > 0 {
		fmt.Printf("\n%s %d skill(s) not on this machine %s:\n", cyan("↓"), len(notInstalled), dim("(need pull)"))
		for _, name := range notInstalled {
			fmt.Printf("  %s %s\n", cyan("↓"), name)
		}
	}

	if len(updated) > 0 {
		fmt.Printf("\n%s %d skill(s) with version differences:\n", yellow("~"), len(updated))
		for _, desc := range updated {
			fmt.Printf("  %s %s\n", yellow("~"), desc)
		}
	}

	fmt.Printf("\nRun '%s' to push and pull.\n", cyan("airskills sync"))
	return nil
}
