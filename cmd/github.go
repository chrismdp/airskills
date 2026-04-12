package cmd

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/chrismdp/airskills/config"
)

// isGitHubURL returns true if the raw input (before prefix stripping) is an
// explicit GitHub URL. We only trigger GitHub-import when the user types
// github.com/... or https://github.com/...
func isGitHubURL(raw string) bool {
	s := strings.TrimPrefix(raw, "https://")
	s = strings.TrimPrefix(s, "http://")
	return strings.HasPrefix(s, "github.com/")
}

// parseGitHubURL extracts owner and repo from a GitHub URL.
// Returns (owner, repo, error).
// Handles: github.com/owner/repo, https://github.com/owner/repo,
//          github.com/owner/repo.git, github.com/owner/repo/tree/main/...
func parseGitHubURL(raw string) (string, string, error) {
	s := strings.TrimPrefix(raw, "https://")
	s = strings.TrimPrefix(s, "http://")
	s = strings.TrimPrefix(s, "github.com/")
	s = strings.TrimSuffix(s, ".git")

	// Strip any /tree/main/... or /blob/main/... suffix
	for _, marker := range []string{"/tree/", "/blob/"} {
		if idx := strings.Index(s, marker); idx > 0 {
			s = s[:idx]
		}
	}

	parts := strings.SplitN(s, "/", 3)
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("expected github.com/owner/repo format")
	}
	return parts[0], parts[1], nil
}

// downloadGitHubTarball fetches the default-branch tarball from GitHub's API
// and returns the extracted files as a map. Public repos need no auth.
func downloadGitHubTarball(owner, repo string) (map[string][]byte, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/tarball", owner, repo)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to download from GitHub: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 404 {
		return nil, fmt.Errorf("repository %s/%s not found on GitHub", owner, repo)
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("GitHub returned %d", resp.StatusCode)
	}

	return extractTarGzToMap(resp.Body)
}

// findSkillsInFiles scans a flat file map (from tarball extraction) and
// returns a map of skill-name -> files for each skill found (directories
// containing a SKILL.md).
func findSkillsInFiles(allFiles map[string][]byte) map[string]map[string][]byte {
	skills := map[string]map[string][]byte{}

	for path, data := range allFiles {
		// Path format after tarball extraction: "skill-name/SKILL.md" or
		// "skill-name/references/foo.md" etc.
		parts := strings.SplitN(path, "/", 2)
		if len(parts) < 2 {
			// Root-level file — check if it's SKILL.md (single-skill repo)
			if path == "SKILL.md" {
				if skills[""] == nil {
					skills[""] = map[string][]byte{}
				}
				skills[""][path] = data
			}
			continue
		}
		dir, relPath := parts[0], parts[1]

		// Check if this directory has a SKILL.md
		skillMdKey := dir + "/SKILL.md"
		if _, hasSKILL := allFiles[skillMdKey]; hasSKILL {
			if skills[dir] == nil {
				skills[dir] = map[string][]byte{}
			}
			skills[dir][relPath] = data
		}
	}

	// Handle single-skill repos where SKILL.md is at the root
	if rootSkill, ok := skills[""]; ok && len(skills) == 1 {
		// Gather all root-level files into the root skill
		for path, data := range allFiles {
			if !strings.Contains(path, "/") {
				rootSkill[path] = data
			}
		}
	}

	return skills
}

// addFromGitHub handles `airskills add github.com/owner/repo [--skill name]`.
// Downloads from GitHub, installs locally, registers on airskills.ai with
// GitHub provenance, and stores the GitHub URL in sync state for future
// update checking.
func addFromGitHub(rawURL, skillFlag string) error {
	owner, repo, err := parseGitHubURL(rawURL)
	if err != nil {
		return err
	}
	githubURL := fmt.Sprintf("https://github.com/%s/%s", owner, repo)

	fmt.Printf("  %s %s\n", cyan("↓"), fmt.Sprintf("Fetching from %s/%s on GitHub...", owner, repo))

	allFiles, err := downloadGitHubTarball(owner, repo)
	if err != nil {
		return err
	}

	skills := findSkillsInFiles(allFiles)
	if len(skills) == 0 {
		return fmt.Errorf("no skills found in %s/%s (no SKILL.md files)", owner, repo)
	}

	// Select the right skill
	var selectedName string
	var selectedFiles map[string][]byte

	if skillFlag != "" {
		// User specified which skill
		sf, ok := skills[skillFlag]
		if !ok {
			available := make([]string, 0, len(skills))
			for name := range skills {
				if name != "" {
					available = append(available, name)
				}
			}
			return fmt.Errorf("skill %q not found in repo. Available: %s", skillFlag, strings.Join(available, ", "))
		}
		selectedName = skillFlag
		selectedFiles = sf
	} else if len(skills) == 1 {
		// Single skill in repo
		for name, files := range skills {
			selectedName = name
			selectedFiles = files
		}
	} else {
		// Multiple skills, no flag — list them
		available := make([]string, 0, len(skills))
		for name := range skills {
			if name != "" {
				available = append(available, name)
			}
		}
		return fmt.Errorf("multiple skills found in repo. Use --skill to pick one: %s", strings.Join(available, ", "))
	}

	// Determine the slug for local install
	slug := selectedName
	if slug == "" {
		slug = repo // root-level skill uses repo name as slug
	}

	// Preview mode
	if addPreview {
		fmt.Printf("\n--- %s/%s (%s) ---\n", owner, repo, slug)
		for path, data := range selectedFiles {
			fmt.Printf("\n=== %s ===\n%s\n", path, string(data))
		}
		fmt.Printf("\nPreview only — run without --preview to install.\n")
		return nil
	}

	// Use repo owner as the namespace prefix (GitHub org/user)
	dirName := namespacedSlug(owner, slug)
	syncState := loadSyncState()
	migrateToNamespacedDirs(syncState)

	// Install to all detected agents
	installed, err := installSkillToAgents(dirName, selectedFiles)
	if err != nil {
		return err
	}

	// Compute content hash for the installed files
	home, _ := os.UserHomeDir()
	primaryDir := filepath.Join(home, ".claude", "skills", dirName)
	originalContent, _ := os.ReadFile(filepath.Join(primaryDir, "SKILL.md"))

	entry := &SyncEntry{
		Version: "github",
		Tool:    "claude-code",
		Source: &skillSource{
			Owner:       owner,
			Slug:        slug,
			ContentHash: sha256Hex(originalContent),
			GitHubURL:   githubURL,
			GitHubSkill: selectedName,
		},
	}

	// If logged in, register the skill on the server with GitHub provenance
	cfg, _ := config.Load()
	token, _ := config.LoadToken()
	if cfg != nil && token != nil && token.ExpiresAt > 0 {
		client := newAPIClient(cfg, token)
		skill, createErr := client.createSkillWithGitHub(slug, githubURL, selectedName)
		if createErr == nil {
			entry.SkillID = skill.ID
			entry.ContentHash = skill.ContentHash
		}
	}

	syncState.Skills[dirName] = entry
	saveSyncState(syncState)

	fmt.Println()
	for _, line := range installed {
		fmt.Println(line)
	}
	fmt.Printf("\nInstalled %s from GitHub to %d agents\n", slug, len(installed))
	fmt.Printf("  %s %s\n", dim("Source:"), githubURL)

	return nil
}

// syncGitHubSkills checks GitHub for updates to any GitHub-sourced skills
// and pushes changes to the airskills platform.
func syncGitHubSkills() {
	syncState := loadSyncState()

	for dirName, entry := range syncState.Skills {
		if entry == nil || entry.Source == nil || entry.Source.GitHubURL == "" {
			continue
		}

		src := entry.Source
		owner, repo, err := parseGitHubURL(src.GitHubURL)
		if err != nil {
			continue
		}

		allFiles, err := downloadGitHubTarball(owner, repo)
		if err != nil {
			continue
		}

		skills := findSkillsInFiles(allFiles)
		selectedFiles, ok := skills[src.GitHubSkill]
		if !ok {
			continue
		}

		// Check if SKILL.md content changed
		newContent, hasSkillMd := selectedFiles["SKILL.md"]
		if !hasSkillMd {
			continue
		}
		newHash := sha256Hex(newContent)
		if newHash == src.ContentHash {
			continue // no changes
		}

		// Update local files
		home, _ := os.UserHomeDir()
		primaryDir := filepath.Join(home, ".claude", "skills", dirName)
		for relPath, data := range selectedFiles {
			fullPath := filepath.Join(primaryDir, relPath)
			os.MkdirAll(filepath.Dir(fullPath), 0755)
			os.WriteFile(fullPath, data, 0644)
		}

		// Mirror to other agents
		installSkillToAgents(dirName, selectedFiles)

		// Update sync state
		entry.Source.ContentHash = newHash
		saveSyncState(syncState)

		fmt.Printf("  %s Updated %s from GitHub\n", green("✓"), dirName)
	}
}
