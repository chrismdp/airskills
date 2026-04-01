package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(pullCmd)
}

var pullCmd = &cobra.Command{
	Use:   "pull",
	Short: "Download remote skills not on this machine",
	Long:  "Pulls skills from your airskills.ai account that aren't installed locally. Installs to all detected agents.",
	RunE:  runPull,
}

func runPull(cmd *cobra.Command, args []string) error {
	client, err := newAPIClientAuto()
	if err != nil {
		return err
	}

	localSkills, err := scanSkillsFromAgents()
	if err != nil {
		return err
	}

	remoteSkills, err := client.listSkills("")
	if err != nil {
		return fmt.Errorf("fetching skills: %w", err)
	}

	var toPull []apiSkill
	for _, remote := range remoteSkills {
		if _, exists := localSkills[remote.Name]; !exists {
			toPull = append(toPull, remote)
		}
	}

	if len(toPull) == 0 {
		fmt.Println("All remote skills already installed.")
		return nil
	}

	lines := make([]progressLine, len(toPull))
	for i, s := range toPull {
		lines[i] = progressLine{name: s.Name, status: "waiting", pct: 0}
		fmt.Printf("  %-20s  %s  %s\n", s.Name, renderBar(0), "waiting")
	}

	var pulled, failed int
	for i, remote := range toPull {
		lines[i].status = "downloading"
		lines[i].pct = 0.5
		renderProgress(lines)

		content, err := client.getSkillContent(remote.ID)
		if err != nil {
			lines[i].status = "failed"
			renderProgress(lines)
			failed++
			continue
		}

		files := map[string][]byte{"SKILL.md": []byte(content)}

		lines[i].status = "installing"
		lines[i].pct = 0.8
		renderProgress(lines)

		destinations, err := installSkillToAgents(remote.Name, files)
		if err != nil {
			lines[i].status = "failed"
			renderProgress(lines)
			failed++
			continue
		}

		// Write marker
		home, _ := os.UserHomeDir()
		primaryDir := filepath.Join(home, ".claude", "skills", remote.Name)
		os.MkdirAll(primaryDir, 0755)
		marker := airskillsMarker{SkillID: remote.ID, Version: remote.Version, Tool: "claude-code"}
		writeMarker(filepath.Join(primaryDir, ".airskills"), &marker)

		lines[i].status = "done"
		lines[i].pct = 1
		lines[i].size = fmt.Sprintf("%d agents", len(destinations))
		renderProgress(lines)
		pulled++
	}

	fmt.Printf("\n%d pulled, %d failed\n", pulled, failed)
	_ = saveLastSync()
	return nil
}
