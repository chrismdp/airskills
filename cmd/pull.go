package cmd

import (
	"encoding/json"
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
	Short: "Download remote skills not on this machine, and update changed ones",
	Long:  "Pulls skills from your airskills.ai account that aren't installed locally or have been updated remotely.",
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

	type pullEntry struct {
		skill  apiSkill
		reason string // "new" or "updated"
	}

	var toPull []pullEntry
	for _, remote := range remoteSkills {
		localDir, exists := localSkills[remote.Name]
		if !exists {
			toPull = append(toPull, pullEntry{remote, "new"})
			continue
		}

		// Check if remote has changed since last pull/push
		markerPath := filepath.Join(localDir, ".airskills")
		markerData, err := os.ReadFile(markerPath)
		if err != nil {
			continue // no marker = can't determine if changed, skip
		}
		var marker airskillsMarker
		if json.Unmarshal(markerData, &marker) != nil {
			continue
		}

		// Compare content hash — if different, remote was updated
		if remote.ContentHash != "" && marker.ContentHash != "" && remote.ContentHash != marker.ContentHash {
			toPull = append(toPull, pullEntry{remote, "updated"})
		}
	}

	if len(toPull) == 0 {
		fmt.Println("All remote skills already installed and up to date.")
		return nil
	}

	lines := make([]progressLine, len(toPull))
	for i, p := range toPull {
		lines[i] = progressLine{name: p.skill.Name, status: "waiting", pct: 0}
		fmt.Printf("  %-20s  %s  %s\n", p.skill.Name, renderBar(0), "waiting")
	}

	var pulled, updated, failed int
	for i, p := range toPull {
		lines[i].status = "downloading"
		lines[i].pct = 0.5
		renderProgress(lines)

		files, err := downloadSkillFiles(client, p.skill.ID)
		if err != nil || len(files) == 0 {
			lines[i].status = "failed"
			renderProgress(lines)
			failed++
			continue
		}

		lines[i].status = "installing"
		lines[i].pct = 0.8
		renderProgress(lines)

		destinations, err := installSkillToAgents(p.skill.Name, files)
		if err != nil {
			lines[i].status = "failed"
			renderProgress(lines)
			failed++
			continue
		}

		// Write/update marker with content hash
		home, _ := os.UserHomeDir()
		primaryDir := filepath.Join(home, ".claude", "skills", p.skill.Name)
		os.MkdirAll(primaryDir, 0755)
		marker := airskillsMarker{
			SkillID:     p.skill.ID,
			Version:     p.skill.Version,
			ContentHash: p.skill.ContentHash,
			Tool:        "claude-code",
		}
		writeMarker(filepath.Join(primaryDir, ".airskills"), &marker)

		if p.reason == "updated" {
			lines[i].status = "done"
			lines[i].size = fmt.Sprintf("updated, %d agents", len(destinations))
			updated++
		} else {
			lines[i].status = "done"
			lines[i].size = fmt.Sprintf("%d agents", len(destinations))
			pulled++
		}
		lines[i].pct = 1
		renderProgress(lines)
	}

	fmt.Printf("\n%d pulled, %d updated, %d failed\n", pulled, updated, failed)
	_ = saveLastSync()
	return nil
}
