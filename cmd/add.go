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
	"github.com/chrismdp/airskills/internal/apitypes"
	"github.com/chrismdp/airskills/telemetry"
	"github.com/spf13/cobra"
)

var addPreview bool

var addSkillFlag string

var addAllFlag bool

var addForce bool

var addCmd = &cobra.Command{
	Use:   "add <username/skill>",
	Short: "Install a shared skill",
	Long: `Install a skill from airskills.ai or directly from GitHub.

  airskills add chrismdp/retro                                          # from airskills.ai
  airskills add chrismdp/retro --force                                  # overwrite local with upstream's current bytes
  airskills add github.com/supabase/agent-skills/supabase               # specific skill from GitHub repo
  airskills add github.com/owner/repo                                   # single-skill GitHub repo
  airskills add github.com/modelcontextprotocol/ext-apps --all          # install all skills in repo
  airskills add github.com/modelcontextprotocol/ext-apps --skill a,b   # install subset

Use --force to take upstream's current bytes when you already have the
skill installed (e.g. after an upstream notification on sync). Any
local copy is backed up to ~/.airskills/undo/<timestamp>/ first.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		input := args[0]

		// Explicit GitHub URLs go through the GitHub import path
		if isGitHubURL(input) {
			return addFromGitHub(input)
		}

		// Strip github.com/ prefix for legacy compat (resolves against airskills API)
		input = strings.TrimPrefix(input, "https://")
		input = strings.TrimPrefix(input, "http://")
		input = strings.TrimPrefix(input, "github.com/")

		parts := strings.SplitN(input, "/", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return fmt.Errorf("expected format: username/skill-name or github.com/owner/repo")
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

		// Resolve the skill silently — we don't show any UI until we know
		// the skill exists, so 404/401 paths produce a clean error with
		// no half-drawn progress bar.
		resolveURL := fmt.Sprintf("%s/api/v1/resolve/%s/%s", cfg.APIURL, username, slug)
		req, err := http.NewRequest("GET", resolveURL, nil)
		if err != nil {
			return err
		}
		if authHeader != "" {
			req.Header.Set("Authorization", authHeader)
		}
		setStandardHeaders(req)

		resp, err := doRequest(http.DefaultClient, req)
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
			Type         string                   `json:"type"`
			ID           string                   `json:"id"`
			Name         string                   `json:"name"`
			Slug         string                   `json:"slug"`
			Content      string                   `json:"content"`
			Version      string                   `json:"version"`
			CurrentOwner *apitypes.OwnerNamespace `json:"current_owner"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			return fmt.Errorf("failed to parse response: %w", err)
		}

		if result.Type == "bundle" {
			fmt.Printf("Bundles are not yet supported for direct install. Visit https://airskills.ai/%s/%s\n", username, slug)
			return nil
		}

		// Skill exists — start the progress UI now.
		lines := []progressLine{{name: result.Slug, status: "downloading", pct: 0.4}}
		if isTTY {
			for _, l := range lines {
				fmt.Printf("  %-20s  %s  %s\n", l.name, renderBar(l.pct), l.status)
			}
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

		// Per the agentskills.io spec, the SKILL.md `name` field must match
		// the parent directory name. The server's slug IS the name field, so
		// the local dir name MUST equal the server slug — no namespace
		// prefixing on disk. Org/user namespace is recorded in the marker
		// (OwnerKind/OwnerSlug), not in the dir name.
		ownerKind := "user"
		ownerSlug := username
		if result.CurrentOwner != nil {
			ownerKind = string(result.CurrentOwner.Kind)
			ownerSlug = result.CurrentOwner.Slug
		}
		dirName := result.Slug

		syncState := loadSyncState()

		// --force path: overwrite any local copy with upstream's current
		// bytes after backing up. Used when the user has been told (by
		// the sync-time notification) that upstream is ahead and they
		// want to adopt upstream's bytes. Replaces the deprecated
		// `airskills incoming incorporate` flow. Source-of-truth spec:
		// platform/doc/changes/cli-kill-incoming-and-fold-into-add-force.md.
		if addForce {
			home, _ := os.UserHomeDir()
			localDirPath := filepath.Join(home, ".claude", "skills", dirName)
			_, hasLocal := os.Stat(localDirPath)
			if hasLocal == nil {
				ts := time.Now().UTC().Format("20060102T150405Z")
				undoPath, backupErr := backupSkillToUndo(dirName, ts)
				if backupErr != nil {
					return fmt.Errorf("%s: backup failed before --force install: %w. No files modified.", dirName, backupErr)
				}

				installed, installErr := installSkillToAgents(dirName, files)
				if installErr != nil {
					return fmt.Errorf("%s: install failed after backup: %w. Local files in ~/.airskills/undo/%s/", dirName, installErr, ts)
				}

				newHash := computeMerkleHash(files)
				existing := syncState.Skills[dirName]
				entry := existing
				if entry == nil {
					entry = &SyncEntry{Tool: "claude-code"}
				}
				entry.Version = result.Version
				entry.ContentHash = newHash
				entry.OwnerKind = ownerKind
				entry.OwnerSlug = ownerSlug
				if entry.Source == nil {
					entry.Source = &skillSource{}
				}
				entry.Source.Owner = username
				entry.Source.Slug = slug
				entry.Source.ID = result.ID
				entry.Source.ContentHash = newHash
				entry.Source.UpstreamSkillID = result.ID
				entry.Source.UpstreamContentHash = newHash
				entry.Source.UpstreamVersion = result.Version
				if entry.SkillID == "" {
					entry.SkillID = result.ID
				}
				syncState.Skills[dirName] = entry
				saveSyncState(syncState)

				fmt.Printf("\n  %s %s/%s overwritten with upstream's current bytes (%d agents)\n",
					green("✓"), ownerSlug, result.Slug, len(installed))
				if undoPath != "" {
					fmt.Printf("  Previous local files backed up to %s/\n", undoPath)
				}
				telemetry.Capture("cli_add", map[string]interface{}{
					"owner":         username,
					"slug":          slug,
					"skill_id":      result.ID,
					"agents":        len(installed),
					"authenticated": authHeader != "",
					"force":         true,
				})
				printAgentNextSteps(os.Stdout, []agentNextStep{
					{Cmd: "airskills status", Why: "confirm local matches upstream"},
					{Cmd: "airskills push", Why: "push the new bytes up to your fork on the server"},
				})
				return nil
			}
			// No local copy yet — --force is a no-op; fall through to
			// normal install.
		}

		// Collision check: if a different skill already lives at this dir
		// name, write the incoming SKILL.md to /tmp and bail. We never
		// silently overwrite — the user (or their agent) decides how to
		// reconcile. Exception: if local bytes already match the remote,
		// silent-link (parity with pull's "linked" path) — no warning,
		// just claim the marker.
		if existing, conflict := detectAddCollision(dirName, result.ID, syncState); conflict {
			if _, silent := detectAddSilentLink(dirName, files); silent {
				syncState.Skills[dirName] = &SyncEntry{
					SkillID:     result.ID,
					Version:     result.Version,
					ContentHash: computeMerkleHash(files),
					Tool:        "claude-code",
					OwnerKind:   ownerKind,
					OwnerSlug:   ownerSlug,
					Source: &skillSource{
						Owner:               username,
						Slug:                slug,
						ID:                  result.ID,
						ContentHash:         computeMerkleHash(files),
						UpstreamSkillID:     result.ID,
						UpstreamContentHash: computeMerkleHash(files),
						UpstreamVersion:     result.Version,
					},
				}
				saveSyncState(syncState)
				fmt.Printf("\n  %s %s/%s %s\n", green("·"), ownerSlug, result.Slug, dim("linked (bytes match server, no download needed)"))
				telemetry.Capture("cli_add", map[string]interface{}{
					"owner":         username,
					"slug":          slug,
					"skill_id":      result.ID,
					"agents":        0,
					"authenticated": authHeader != "",
					"silent_link":   true,
				})
				printAgentNextSteps(os.Stdout, []agentNextStep{
					{Cmd: "airskills status", Why: "confirm the skill is now tracked"},
					{Cmd: "airskills sync", Why: "pull any other remote skills onto this machine"},
				})
				return nil
			}
			tmpPath, writeErr := writeConflictToTmp(dirName, files)
			if writeErr != nil {
				return fmt.Errorf("could not save incoming skill to tmp: %w", writeErr)
			}
			localPath := existing
			fmt.Printf("\n  %s You already have a skill called %q at %s.\n",
				yellow("⚠"), result.Slug, localPath)
			fmt.Printf("  The new version from %s/%s is at:\n    %s\n\n",
				ownerSlug, result.Slug, tmpPath)
			if isTTY {
				fmt.Println("  Ask your agent to read both files and incorporate anything")
				fmt.Println("  worth keeping into your existing skill, or rename one to coexist.")
			} else {
				fmt.Printf("  AGENT INSTRUCTIONS: Read %s and %s.\n", localPath, tmpPath)
				fmt.Println("  Ask the user one question at a time to resolve any ambiguity")
				fmt.Println("  about which behaviours to keep, then patch the local SKILL.md.")
			}
			return nil
		}

		// Install to all detected agents
		lines[0].status = "installing"
		lines[0].pct = 0.9
		renderProgress(lines)

		installed, err := installSkillToAgents(dirName, files)
		if err != nil {
			return err
		}

		lines[0].status = "done"
		lines[0].pct = 1
		lines[0].size = fmt.Sprintf("%d agents", len(installed))
		renderProgress(lines)

		// Register the skill on the server (COW: references parent's archive,
		// no physical copy until the user modifies and pushes).
		// If logged in, create immediately; otherwise track source for next sync.
		home, _ := os.UserHomeDir()
		primaryDir := filepath.Join(home, ".claude", "skills", dirName)
		os.MkdirAll(primaryDir, 0755)
		upstreamHash := computeMerkleHash(files)

		entry := &SyncEntry{
			Version:   result.Version,
			Tool:      "claude-code",
			OwnerKind: ownerKind,
			OwnerSlug: ownerSlug,
			Source: &skillSource{
				Owner:               username,
				Slug:                slug,
				ID:                  result.ID,
				ContentHash:         upstreamHash,
				UpstreamSkillID:     result.ID,
				UpstreamContentHash: upstreamHash,
				UpstreamVersion:     result.Version,
			},
		}

		// If logged in, register the skill on the server now
		if token != nil && time.Now().Unix() < token.ExpiresAt {
			client := newAPIClient(cfg, token)
			skill, createErr := client.createSkill(result.Slug, "", []string{"claude-code"}, result.ID, "")
			if createErr == nil {
				entry.SkillID = skill.Id.String()
				entry.ContentHash = strDeref(skill.ContentHash)
			}
			// If creation fails (e.g. network), fall through — sync will handle it
		}

		syncState.Skills[dirName] = entry
		saveSyncState(syncState)

		fmt.Println()
		for _, line := range installed {
			fmt.Println(line)
		}
		fmt.Printf("\nInstalled %s/%s to %d agents\n", username, slug, len(installed))

		telemetry.Capture("cli_add", map[string]interface{}{
			"owner":         username,
			"slug":          slug,
			"skill_id":      result.ID,
			"agents":        len(installed),
			"authenticated": authHeader != "",
		})

		steps := []agentNextStep{
			{Cmd: "airskills status", Why: "see the new skill alongside the rest"},
			{Cmd: "airskills sync", Why: "pull any other remote skills onto this machine"},
		}
		if authHeader == "" {
			steps = append(steps, agentNextStep{
				Cmd: "airskills login",
				Why: "log in to track this fork and push edits back",
			})
		}
		printAgentNextSteps(os.Stdout, steps)
		return nil
	},
}

// fetchSkillFiles tries the archive first, falls back to SKILL.md content (with progress UI).
func fetchSkillFiles(cfg *config.Config, skillID, content, authHeader string, lines []progressLine) (map[string][]byte, error) {
	lines[0].status = "downloading"
	lines[0].pct = 0.5
	renderProgress(lines)

	files, err := downloadSkillByID(cfg.APIURL, skillID, content, authHeader)
	if err == nil {
		lines[0].status = "extracting"
		lines[0].pct = 0.7
		renderProgress(lines)
	}
	return files, err
}

// downloadSkillByID fetches a skill's files by ID. Tries archive, falls back to SKILL.md content.
func downloadSkillByID(apiURL, skillID, fallbackContent, authHeader string) (map[string][]byte, error) {
	archiveURL := fmt.Sprintf("%s/api/v1/skills/%s/archive", apiURL, skillID)
	archiveReq, _ := http.NewRequest("GET", archiveURL, nil)
	if authHeader != "" {
		archiveReq.Header.Set("Authorization", authHeader)
	}
	setStandardHeaders(archiveReq)

	archiveResp, err := doRequest(http.DefaultClient, archiveReq)
	if err == nil && archiveResp.StatusCode == 200 {
		defer archiveResp.Body.Close()
		files, err := extractTarGzToMap(archiveResp.Body)
		if err == nil && len(files) > 0 {
			return files, nil
		}
	}
	if archiveResp != nil {
		archiveResp.Body.Close()
	}

	if fallbackContent != "" {
		return map[string][]byte{"SKILL.md": []byte(fallbackContent)}, nil
	}
	return nil, fmt.Errorf("no files available for skill %s", skillID)
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
	addCmd.Flags().StringVar(&addSkillFlag, "skill", "", "Install specific skill(s) from a multi-skill GitHub repo (comma-separated names or path/to/name)")
	addCmd.Flags().BoolVar(&addAllFlag, "all", false, "Install all skills found in a GitHub repository")
	addCmd.Flags().BoolVar(&addForce, "force", false, "Take upstream's current bytes, overwriting any local copy (local backed up to ~/.airskills/undo/)")
	rootCmd.AddCommand(addCmd)
}
