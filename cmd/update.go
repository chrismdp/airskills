package cmd

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(updateCmd)
}

var updateCmd = &cobra.Command{
	Use:   "update [name]",
	Short: "Pull latest skill versions and write to tool directories",
	Long: `Fetches the latest versions of all skills (or a named skill) from your
airskills account and writes them to each tool's directory.

Reports which skills changed.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runUpdate,
}

func runUpdate(cmd *cobra.Command, args []string) error {
	client, err := newAPIClientAuto()
	if err != nil {
		return err
	}

	var filterName string
	if len(args) == 1 {
		filterName = args[0]
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}

	skills, err := client.listSkills("")
	if err != nil {
		return fmt.Errorf("fetching skills: %w", err)
	}

	_ = home // used for marker path

	var updated, unchanged int
	for _, skill := range skills {
		if filterName != "" && skill.Name != filterName {
			continue
		}

		// Download archive
		archiveBody, err := client.get(fmt.Sprintf("/api/v1/skills/%s/archive", skill.ID))
		if err != nil {
			fmt.Fprintf(os.Stderr, "  ! %s: %v\n", skill.Name, err)
			continue
		}

		files, err := extractTarGzToMap(bytes.NewReader(archiveBody))
		if err != nil || len(files) == 0 {
			fmt.Fprintf(os.Stderr, "  ! %s: no files in archive\n", skill.Name)
			continue
		}

		destinations, err := installSkillToAgents(skill.Name, files)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  ! %s: %v\n", skill.Name, err)
			unchanged++
			continue
		}

		// Write marker
		primaryDir := filepath.Join(home, ".claude", "skills", skill.Name)
		os.MkdirAll(primaryDir, 0755)
		marker := airskillsMarker{
			SkillID:     skill.ID,
			Version:     skill.Version,
			ContentHash: skill.ContentHash,
			Tool:        "claude-code",
		}
		writeMarker(filepath.Join(primaryDir, ".airskills"), &marker)

		fmt.Printf("  updated: %s (%d agents)\n", skill.Name, len(destinations))
		updated++
	}

	if filterName != "" && updated == 0 && unchanged == 0 {
		return fmt.Errorf("skill %q not found in your account", filterName)
	}

	fmt.Printf("\n%d updated, %d unchanged\n", updated, unchanged)
	_ = saveLastSync()
	return nil
}
