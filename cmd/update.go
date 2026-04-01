package cmd

import (
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

	type toolDirEntry struct {
		tool string
		dir  string
	}
	toolDirs := []toolDirEntry{
		{"claude-code", filepath.Join(home, ".claude", "skills")},
		{"cursor", filepath.Join(home, ".cursor", "rules")},
		{"copilot", filepath.Join(home, ".github", "instructions")},
	}

	var updated, unchanged int
	for _, skill := range skills {
		if filterName != "" && skill.Name != filterName {
			continue
		}

		content, err := client.getSkillContent(skill.ID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  ! %s: %v\n", skill.Name, err)
			continue
		}

		tools := skill.ToolFormats
		if len(tools) == 0 {
			tools = []string{"claude-code"}
		}
		toolSet := make(map[string]bool)
		for _, t := range tools {
			toolSet[t] = true
		}

		skillChanged := false
		for _, td := range toolDirs {
			if !toolSet[td.tool] {
				continue
			}

			skillDir := filepath.Join(td.dir, skill.Name)
			skillPath := filepath.Join(skillDir, "SKILL.md")

			existing, err := os.ReadFile(skillPath)
			if err == nil && string(existing) == content {
				continue
			}

			if err := os.MkdirAll(skillDir, 0o755); err != nil {
				fmt.Fprintf(os.Stderr, "  ! %s -> %s: %v\n", skill.Name, td.tool, err)
				continue
			}

			if err := os.WriteFile(skillPath, []byte(content), 0o644); err != nil {
				fmt.Fprintf(os.Stderr, "  ! %s -> %s: %v\n", skill.Name, td.tool, err)
				continue
			}

			marker := fmt.Sprintf(`{"skill_id":"%s","version":"%s","tool":"%s"}`,
				skill.ID, skill.Version, td.tool)
			_ = os.WriteFile(filepath.Join(skillDir, ".airskills"), []byte(marker), 0o644)

			_ = client.recordInstallation(skill.ID, td.tool, skill.Version)

			fmt.Printf("  updated: %s -> %s\n", skill.Name, skillPath)
			skillChanged = true
		}

		if skillChanged {
			updated++
		} else {
			unchanged++
		}
	}

	if filterName != "" && updated == 0 && unchanged == 0 {
		return fmt.Errorf("skill %q not found in your account", filterName)
	}

	fmt.Printf("\n%d updated, %d unchanged\n", updated, unchanged)
	_ = saveLastSync()
	return nil
}
