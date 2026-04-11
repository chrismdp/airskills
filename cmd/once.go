package cmd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/chrismdp/airskills/config"
	"github.com/spf13/cobra"
)

const onceTmpDir = "/tmp/airskills-once"

var onceCmd = &cobra.Command{
	Use:     "once <username/skill>",
	Aliases: []string{"show"},
	Short:   "Download a skill to a temp folder for one-time use",
	Long: `Download a skill from airskills.ai to a temporary folder without
installing it permanently. Prints the SKILL.md path so you can reference
it with @ in your AI agent, plus @ paths for any additional skill files
so your agent can load them one at a time as needed.

Also available as 'airskills show'.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		input := args[0]

		// Strip github.com/ or https://github.com/ prefix
		input = strings.TrimPrefix(input, "https://")
		input = strings.TrimPrefix(input, "http://")
		input = strings.TrimPrefix(input, "github.com/")

		parts := strings.SplitN(input, "/", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return fmt.Errorf("expected format: username/skill-name")
		}
		username, slug := parts[0], parts[1]

		cfg, err := config.Load()
		if err != nil {
			return err
		}

		var authHeader string
		token, _ := config.LoadToken()
		if token != nil && time.Now().Unix() < token.ExpiresAt {
			authHeader = "Bearer " + token.AccessToken
		}

		// Resolve the skill
		lines := []progressLine{{name: slug, status: "resolving", pct: 0.2}}
		if isTTY {
			for _, l := range lines {
				fmt.Printf("  %-20s  %s  %s\n", l.name, renderBar(l.pct), l.status)
			}
		}

		resolveURL := fmt.Sprintf("%s/api/v1/resolve/%s/%s", cfg.APIURL, username, slug)
		req, err := http.NewRequest("GET", resolveURL, nil)
		if err != nil {
			return err
		}
		if authHeader != "" {
			req.Header.Set("Authorization", authHeader)
		}

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return fmt.Errorf("failed to fetch: %w", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode == 404 {
			return fmt.Errorf("skill %s/%s not found (is it public, or shared with you?)", username, slug)
		}
		if resp.StatusCode == 401 {
			return fmt.Errorf("skill %s/%s requires login — run 'airskills login' first", username, slug)
		}
		if resp.StatusCode != 200 {
			return fmt.Errorf("server returned %d", resp.StatusCode)
		}

		var result struct {
			Type    string `json:"type"`
			ID      string `json:"id"`
			Name    string `json:"name"`
			Slug    string `json:"slug"`
			Content string `json:"content"`
			Version string `json:"version"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			return fmt.Errorf("failed to parse response: %w", err)
		}

		if result.Type == "bundle" {
			fmt.Printf("Bundles are not yet supported for once. Visit https://airskills.ai/%s/%s\n", username, slug)
			return nil
		}

		// Download the skill files
		files, err := fetchSkillFiles(cfg, result.ID, result.Content, authHeader, lines)
		if err != nil {
			return err
		}

		// Write files to temp directory
		lines[0].status = "writing"
		lines[0].pct = 0.9
		renderProgress(lines)

		destDir := filepath.Join(onceTmpDir, result.Slug)
		if err := os.MkdirAll(destDir, 0755); err != nil {
			return fmt.Errorf("failed to create temp directory: %w", err)
		}

		for relPath, data := range files {
			fullPath := filepath.Join(destDir, relPath)
			if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
				return fmt.Errorf("failed to create directory for %s: %w", relPath, err)
			}
			if err := os.WriteFile(fullPath, data, 0644); err != nil {
				return fmt.Errorf("failed to write %s: %w", relPath, err)
			}
		}

		lines[0].status = "done"
		lines[0].pct = 1
		renderProgress(lines)

		skillMdPath := filepath.Join(destDir, "SKILL.md")
		fmt.Printf("\nSkill downloaded to %s\n", destDir)
		fmt.Printf("\nStart with: @ %s\n", skillMdPath)

		// List any additional files as follow-on @ references so the AI
		// can load them one at a time after reading SKILL.md.
		extras := make([]string, 0, len(files))
		for relPath := range files {
			if relPath == "SKILL.md" {
				continue
			}
			extras = append(extras, relPath)
		}
		sort.Strings(extras)
		if len(extras) > 0 {
			fmt.Println("\nAdditional files in this skill:")
			for _, rel := range extras {
				fmt.Printf("  @ %s\n", filepath.Join(destDir, rel))
			}
		}

		return nil
	},
}

func init() {
	rootCmd.AddCommand(onceCmd)
}
