package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

func init() {
	statusCmd.Flags().BoolP("quiet", "q", false, "Only output when updates are available")
	rootCmd.AddCommand(statusCmd)
}

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Check for skill updates",
	Long: `One-line sync status, designed for shell startup:
  eval "$(airskills status)"`,
	RunE: runStatus,
}

func runStatus(cmd *cobra.Command, args []string) error {
	quiet, _ := cmd.Flags().GetBool("quiet")

	client, err := newAPIClientAuto()
	if err != nil {
		return nil
	}

	// Parallel: fetch skills and health check at the same time
	type skillsResult struct {
		skills []apiSkill
		err    error
	}
	type healthResult struct {
		latestCLI string
	}

	skillsCh := make(chan skillsResult, 1)
	healthCh := make(chan healthResult, 1)

	go func() {
		skills, err := client.listSkills("personal")
		skillsCh <- skillsResult{skills, err}
	}()

	go func() {
		var latest string
		if body, err := client.get("/api/v1/health"); err == nil {
			var h struct {
				LatestCLI string `json:"latest_cli"`
			}
			if parseJSON(body, &h) == nil && h.LatestCLI != "" && isNewer(h.LatestCLI, version) && version != "dev" {
				latest = h.LatestCLI
			}
		}
		healthCh <- healthResult{latest}
	}()

	localSkills, _ := scanSkillsFromAgents()
	syncState := loadSyncState()

	sr := <-skillsCh
	if sr.err != nil {
		return nil
	}
	hr := <-healthCh

	skillIdToName := map[string]string{}
	for name, entry := range syncState.Skills {
		if entry.SkillID != "" {
			skillIdToName[entry.SkillID] = name
		}
	}

	var needPush, needPull, needUpdate, upstreamUpdates int

	remoteByName := map[string]bool{}
	for _, remote := range sr.skills {
		remoteByName[remote.Name] = true

		if remote.HasUpstreamUpdate() {
			upstreamUpdates++
		}

		trackedName := ""
		if name, ok := skillIdToName[remote.ID]; ok {
			trackedName = name
		}

		if trackedName != "" {
			if _, exists := localSkills[trackedName]; !exists {
				continue
			}
			marker := syncState.Skills[trackedName]
			if marker.ContentHash != "" && remote.ContentHash != "" && marker.ContentHash != remote.ContentHash {
				needUpdate++
			}
			continue
		}

		if _, exists := localSkills[remote.Name]; !exists {
			needPull++
		}
	}

	for name := range localSkills {
		if !remoteByName[name] {
			needPush++
		}
	}

	if needPush == 0 && needPull == 0 && needUpdate == 0 && upstreamUpdates == 0 && hr.latestCLI == "" {
		if !quiet {
			fmt.Fprintf(os.Stderr, "[airskills] %s\n", green("✓ in sync"))
		}
		return nil
	}

	var parts []string
	if needPush > 0 {
		parts = append(parts, yellow(fmt.Sprintf("↑ %d to push", needPush)))
	}
	if needPull > 0 {
		parts = append(parts, cyan(fmt.Sprintf("↓ %d to pull", needPull)))
	}
	if needUpdate > 0 {
		parts = append(parts, yellow(fmt.Sprintf("~ %d changed", needUpdate)))
	}
	if upstreamUpdates > 0 {
		parts = append(parts, cyan(fmt.Sprintf("⬆ %d upstream", upstreamUpdates)))
	}
	fmt.Fprintf(os.Stderr, "[airskills] %s — run 'airskills sync'\n", strings.Join(parts, ", "))

	if hr.latestCLI != "" {
		fmt.Fprintf(os.Stderr, "[airskills] %s → %s: run 'airskills self-update'\n",
			yellow("update"), hr.latestCLI)
	}

	return nil
}
