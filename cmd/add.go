package cmd

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chrismdp/airskills/config"
	"github.com/spf13/cobra"
)

var addPreview bool

var addCmd = &cobra.Command{
	Use:   "add <username/skill>",
	Short: "Install a shared skill",
	Long:  "Install a skill from airskills.ai to all detected AI agents.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		input := args[0]

		// Strip github.com/ or https://github.com/ prefix
		input = strings.TrimPrefix(input, "https://")
		input = strings.TrimPrefix(input, "http://")
		input = strings.TrimPrefix(input, "github.com/")

		parts := strings.SplitN(input, "/", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return fmt.Errorf("expected format: username/skill-name or github.com/username/skill-name")
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
		for _, l := range lines {
			fmt.Printf("  %-20s  %s  %s\n", l.name, renderBar(l.pct), l.status)
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
			fmt.Printf("Bundles are not yet supported for direct install. Visit https://airskills.ai/%s/%s\n", username, slug)
			return nil
		}

		// Collect the files to install
		files, err := fetchSkillFiles(cfg, result.ID, result.Content, authHeader, lines)
		if err != nil {
			return err
		}

		// Preview mode: show files and exit
		if addPreview {
			fmt.Printf("\n--- %s/%s ---\n", username, slug)
			for path, data := range files {
				fmt.Printf("\n=== %s ===\n%s\n", path, string(data))
			}
			fmt.Printf("\nPreview only — run without --preview to install.\n")
			return nil
		}

		// Install to all detected agents
		lines[0].status = "installing"
		lines[0].pct = 0.9
		renderProgress(lines)

		installed, err := installSkillToAgents(result.Slug, files)
		if err != nil {
			return err
		}

		lines[0].status = "done"
		lines[0].pct = 1
		lines[0].size = fmt.Sprintf("%d agents", len(installed))
		renderProgress(lines)

		// Track source in sync state — never set skill_id.
		// The user's own skill_id is set later when they push (creates their copy
		// with forked_from linking back to the original).
		home, _ := os.UserHomeDir()
		primaryDir := filepath.Join(home, ".claude", "skills", result.Slug)
		os.MkdirAll(primaryDir, 0755)
		// Hash the original content so push can detect modifications
		originalContent, _ := os.ReadFile(filepath.Join(primaryDir, "SKILL.md"))
		syncState := loadSyncState()
		syncState.Skills[result.Slug] = &SyncEntry{
			Version: result.Version,
			Tool:    "claude-code",
			Source: &skillSource{
				Owner:       username,
				Slug:        slug,
				ID:          result.ID,
				ContentHash: sha256Hex(originalContent),
			},
		}
		saveSyncState(syncState)

		fmt.Println()
		for _, line := range installed {
			fmt.Println(line)
		}
		fmt.Printf("\nInstalled %s/%s to %d agents\n", username, slug, len(installed))
		return nil
	},
}

// fetchSkillFiles tries the archive first, falls back to SKILL.md content
func fetchSkillFiles(cfg *config.Config, skillID, content, authHeader string, lines []progressLine) (map[string][]byte, error) {
	lines[0].status = "downloading"
	lines[0].pct = 0.5
	renderProgress(lines)

	// Try archive
	archiveURL := fmt.Sprintf("%s/api/v1/skills/%s/archive", cfg.APIURL, skillID)
	archiveReq, _ := http.NewRequest("GET", archiveURL, nil)
	if authHeader != "" {
		archiveReq.Header.Set("Authorization", authHeader)
	}

	archiveResp, err := http.DefaultClient.Do(archiveReq)
	if err == nil && archiveResp.StatusCode == 200 {
		defer archiveResp.Body.Close()

		lines[0].status = "extracting"
		lines[0].pct = 0.7
		renderProgress(lines)

		files, err := extractTarGzToMap(archiveResp.Body)
		if err == nil && len(files) > 0 {
			return files, nil
		}
	}
	if archiveResp != nil {
		archiveResp.Body.Close()
	}

	// Fallback: just SKILL.md
	return map[string][]byte{
		"SKILL.md": []byte(content),
	}, nil
}

// extractTarGzToMap reads a tar.gz into a map of relative-path -> content
func extractTarGzToMap(r io.Reader) (map[string][]byte, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return nil, err
	}
	defer gz.Close()

	files := map[string][]byte{}
	tr := tar.NewReader(gz)

	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}

		// Strip top-level directory
		parts := strings.SplitN(header.Name, "/", 2)
		if len(parts) < 2 || parts[1] == "" {
			continue
		}
		relPath := parts[1]

		if filepath.Base(relPath) == ".airskills" {
			continue
		}

		if header.Typeflag == tar.TypeReg {
			data, err := io.ReadAll(tr)
			if err != nil {
				return nil, err
			}
			files[relPath] = data
		}
	}

	return files, nil
}

func countFiles(dir string) int {
	count := 0
	filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && info.Name() != ".airskills" {
			count++
		}
		return nil
	})
	return count
}

func init() {
	addCmd.Flags().BoolVar(&addPreview, "preview", false, "Show skill content without installing")
	rootCmd.AddCommand(addCmd)
}
