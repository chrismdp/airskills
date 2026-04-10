package cmd

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

func init() {
	exportCmd.Flags().StringP("output", "o", "", "Output path (default: ./<name>.zip or ./<name>/)")
	exportCmd.Flags().StringP("format", "f", "zip", "Export format: zip (ChatGPT/Cowork/Claude.ai) or dir (Claude Code plugin)")
	exportCmd.Flags().Bool("all", false, "Export all skills from your account")
	rootCmd.AddCommand(exportCmd)
}

var exportCmd = &cobra.Command{
	Use:   "export [name]",
	Short: "Export skills as zip (for ChatGPT/Cowork) or directory (for Claude Code)",
	Long: `Exports skills from your airskills account into portable formats.

Formats:
  zip   A zip file containing all skill files — drag into Claude.ai's Upload Skill
        dialog, ChatGPT's Skills page, or Cowork. (default)
  dir   A directory with skill files — the Claude Code plugin structure.
        Copy into ~/.claude/skills/ or a project's .claude/skills/.

Examples:
  airskills export code-review                    # → code-review.zip
  airskills export code-review -f dir             # → code-review/SKILL.md
  airskills export code-review -o ~/Downloads/    # → ~/Downloads/code-review.zip
  airskills export --all                          # → exports all skills as zips`,
	RunE: runExport,
}

func runExport(cmd *cobra.Command, args []string) error {
	exportAll, _ := cmd.Flags().GetBool("all")
	if !exportAll && len(args) == 0 {
		return fmt.Errorf("specify a skill name or use --all")
	}

	client, err := newAPIClientAuto()
	if err != nil {
		return err
	}

	format, _ := cmd.Flags().GetString("format")
	output, _ := cmd.Flags().GetString("output")

	skills, err := client.listSkills("")
	if err != nil {
		return fmt.Errorf("fetching skills: %w", err)
	}

	if exportAll {
		if len(skills) == 0 {
			fmt.Println("No skills in your account.")
			return nil
		}

		dir := output
		if dir == "" {
			dir = "."
		}

		var exported int
		for _, skill := range skills {
			files, err := downloadSkillFiles(client, skill.ID)
			if err != nil {
				fmt.Fprintf(os.Stderr, "  ! %s: %v\n", skill.Name, err)
				continue
			}
			if err := exportSkill(skill, files, format, dir, ""); err != nil {
				fmt.Fprintf(os.Stderr, "  ! %s: %v\n", skill.Name, err)
				continue
			}
			exported++
		}
		fmt.Printf("\nExported %d skill(s)\n", exported)
		return nil
	}

	// Single skill export
	name := args[0]
	var target *apiSkill
	for _, s := range skills {
		if s.Name == name {
			target = &s
			break
		}
	}
	if target == nil {
		return fmt.Errorf("skill %q not found in your account", name)
	}

	files, err := downloadSkillFiles(client, target.ID)
	if err != nil {
		return fmt.Errorf("downloading skill files: %w", err)
	}

	dir := "."
	outFile := output
	if output != "" {
		info, err := os.Stat(output)
		if err == nil && info.IsDir() {
			dir = output
			outFile = ""
		}
	}

	return exportSkill(*target, files, format, dir, outFile)
}

func downloadSkillFiles(client *apiClient, skillID string) (map[string][]byte, error) {
	archiveBody, err := client.get(fmt.Sprintf("/api/v1/skills/%s/archive", skillID))
	if err != nil {
		return nil, err
	}
	return extractTarGzToMap(bytes.NewReader(archiveBody))
}

func exportSkill(skill apiSkill, files map[string][]byte, format, dir, outFile string) error {
	switch format {
	case "zip":
		return exportZip(skill, files, dir, outFile)
	case "dir":
		return exportDir(skill, files, dir, outFile)
	default:
		return fmt.Errorf("unknown format %q — use 'zip' or 'dir'", format)
	}
}

func exportZip(skill apiSkill, files map[string][]byte, dir, outFile string) error {
	if outFile == "" {
		outFile = filepath.Join(dir, skill.Name+".zip")
	}

	f, err := os.Create(outFile)
	if err != nil {
		return fmt.Errorf("creating zip: %w", err)
	}
	defer f.Close()

	w := zip.NewWriter(f)

	// Add all skill files
	for path, content := range files {
		fw, err := w.Create(path)
		if err != nil {
			return err
		}
		if _, err := fw.Write(content); err != nil {
			return err
		}
	}

	// Add metadata.json
	meta := map[string]interface{}{
		"name":        skill.Name,
		"description": skill.Description,
		"version":     skill.Version,
		"source":      "airskills",
	}
	if len(skill.ToolFormats) > 0 {
		meta["tool_formats"] = skill.ToolFormats
	}
	metaJSON, _ := json.MarshalIndent(meta, "", "  ")
	mw, err := w.Create("metadata.json")
	if err != nil {
		return err
	}
	if _, err := mw.Write(metaJSON); err != nil {
		return err
	}

	if err := w.Close(); err != nil {
		return err
	}

	fmt.Printf("  exported: %s → %s (%d files)\n", skill.Name, outFile, len(files))
	return nil
}

func exportDir(skill apiSkill, files map[string][]byte, dir, outFile string) error {
	skillDir := outFile
	if skillDir == "" {
		skillDir = filepath.Join(dir, skill.Name)
	}

	if err := writeFilesToDir(skillDir, files); err != nil {
		return err
	}

	fmt.Printf("  exported: %s → %s/ (%d files)\n", skill.Name, skillDir, len(files))
	return nil
}
