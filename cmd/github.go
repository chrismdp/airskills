package cmd

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/chrismdp/airskills/config"
)

// foundSkill holds metadata for a skill discovered inside a tarball.
type foundSkill struct {
	FullPath string             // full parent dir path in tarball (empty for root-level)
	LeafName string             // last path component of FullPath (empty for root-level)
	Files    map[string][]byte  // relative paths within the skill dir → content
}

// isGitHubURL returns true if the raw input (before prefix stripping) is an
// explicit GitHub URL. We only trigger GitHub-import when the user types
// github.com/... or https://github.com/...
func isGitHubURL(raw string) bool {
	s := strings.TrimPrefix(raw, "https://")
	s = strings.TrimPrefix(s, "http://")
	return strings.HasPrefix(s, "github.com/")
}

// parseGitHubURL extracts owner, repo, and optional skill path from a GitHub URL.
// Returns (owner, repo, skillPath, error).
// Handles:
//   github.com/owner/repo                          → ("owner", "repo", "")
//   github.com/owner/repo/skill-name               → ("owner", "repo", "skill-name")
//   github.com/owner/repo/tree/main/skill-name     → ("owner", "repo", "skill-name")
//   https://github.com/owner/repo.git              → ("owner", "repo", "")
func parseGitHubURL(raw string) (string, string, string, error) {
	s := strings.TrimPrefix(raw, "https://")
	s = strings.TrimPrefix(s, "http://")
	s = strings.TrimPrefix(s, "github.com/")
	s = strings.TrimSuffix(s, ".git")
	s = strings.TrimSuffix(s, "/")

	parts := strings.Split(s, "/")
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return "", "", "", fmt.Errorf("expected github.com/owner/repo format")
	}

	owner, repo := parts[0], parts[1]
	remaining := parts[2:]

	// Strip /tree/<branch>/ or /blob/<branch>/ prefix from remaining path
	if len(remaining) >= 2 && (remaining[0] == "tree" || remaining[0] == "blob") {
		remaining = remaining[2:] // skip "tree" and branch name
	}

	skillPath := strings.Join(remaining, "/")
	return owner, repo, skillPath, nil
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

// scanSkillRoots recursively walks allFiles and returns all discovered skills.
// No collision detection — callers that need it should call findSkillsInFiles.
func scanSkillRoots(allFiles map[string][]byte) []foundSkill {
	// Step 1: find all directories that contain a SKILL.md.
	skillRoots := map[string]struct{}{} // key: full parent path ("" for root-level)
	for path := range allFiles {
		if path == "SKILL.md" {
			skillRoots[""] = struct{}{}
		} else if strings.HasSuffix(path, "/SKILL.md") {
			parent := strings.TrimSuffix(path, "/SKILL.md")
			skillRoots[parent] = struct{}{}
		}
	}

	if len(skillRoots) == 0 {
		return nil
	}

	// Step 2: sort skill roots by length descending for longest-prefix matching.
	sorted := make([]string, 0, len(skillRoots))
	for root := range skillRoots {
		sorted = append(sorted, root)
	}
	sort.Slice(sorted, func(i, j int) bool {
		return len(sorted[i]) > len(sorted[j]) // longest first
	})

	// Step 3: initialise skill entries.
	byRoot := make(map[string]*foundSkill, len(sorted))
	for _, root := range sorted {
		leaf := ""
		if root != "" {
			leaf = filepath.Base(root)
		}
		byRoot[root] = &foundSkill{
			FullPath: root,
			LeafName: leaf,
			Files:    map[string][]byte{},
		}
	}

	// Step 4: assign every file to the deepest (longest-prefix) skill root.
	for path, data := range allFiles {
		assigned := false
		for _, root := range sorted {
			if root == "" {
				continue // handle root-level separately below
			}
			if strings.HasPrefix(path, root+"/") {
				relPath := path[len(root)+1:]
				byRoot[root].Files[relPath] = data
				assigned = true
				break
			}
		}
		// Root-level skill gets files with no directory component.
		if !assigned {
			if _, hasRoot := byRoot[""]; hasRoot {
				if !strings.Contains(path, "/") {
					byRoot[""].Files[path] = data
				}
			}
		}
	}

	// Step 5: return as slice.
	result := make([]foundSkill, 0, len(byRoot))
	for _, s := range byRoot {
		result = append(result, *s)
	}
	return result
}

// findSkillsInFiles wraps scanSkillRoots and returns an error if two discovered
// skills share the same leaf name (which makes selection ambiguous).
func findSkillsInFiles(allFiles map[string][]byte) ([]foundSkill, error) {
	skills := scanSkillRoots(allFiles)
	if err := checkLeafCollisions(skills); err != nil {
		return nil, err
	}
	return skills, nil
}

// checkLeafCollisions returns an error if any two skills share a leaf name.
func checkLeafCollisions(skills []foundSkill) error {
	leafToPaths := map[string][]string{}
	for _, s := range skills {
		leafToPaths[s.LeafName] = append(leafToPaths[s.LeafName], s.FullPath)
	}
	for leafName, paths := range leafToPaths {
		if len(paths) > 1 {
			sort.Strings(paths)
			displayName := leafName
			if displayName == "" {
				displayName = "(root)"
			}
			return fmt.Errorf(
				"skill name conflict: %q found at multiple paths in this repo:\n  %s\nUse --skill <path/to/%s> to pick one",
				displayName,
				strings.Join(paths, "\n  "),
				displayName,
			)
		}
	}
	return nil
}

// resolveSkillSelector finds a single foundSkill matching the selector string.
// Selector can be a leaf name ("foo") or a path suffix ("a/b/foo") for
// disambiguation when there are leaf-name collisions. Returns an error if
// zero or multiple skills match.
func resolveSkillSelector(selector string, skills []foundSkill) (foundSkill, error) {
	// Try exact leaf-name match first.
	var exact []foundSkill
	for _, s := range skills {
		if s.LeafName == selector {
			exact = append(exact, s)
		}
	}
	if len(exact) == 1 {
		return exact[0], nil
	}

	// Try suffix match on FullPath (for disambiguation like --skill a/b/foo).
	var suffixMatches []foundSkill
	for _, s := range skills {
		if s.FullPath == selector || strings.HasSuffix(s.FullPath, "/"+selector) {
			suffixMatches = append(suffixMatches, s)
		}
	}
	if len(suffixMatches) == 1 {
		return suffixMatches[0], nil
	}
	if len(suffixMatches) > 1 {
		paths := make([]string, len(suffixMatches))
		for i, s := range suffixMatches {
			paths[i] = s.FullPath
		}
		return foundSkill{}, fmt.Errorf("ambiguous selector %q matches multiple skills:\n  %s", selector, strings.Join(paths, "\n  "))
	}

	// Build available list for the error message.
	available := make([]string, 0, len(skills))
	for _, s := range skills {
		if s.LeafName != "" {
			available = append(available, s.LeafName)
		}
	}
	sort.Strings(available)
	return foundSkill{}, fmt.Errorf("skill %q not found in repo. Available: %s", selector, strings.Join(available, ", "))
}

// displayLeafNames returns a sorted slice of leaf names for user messages.
func displayLeafNames(skills []foundSkill) []string {
	names := make([]string, 0, len(skills))
	for _, s := range skills {
		if s.LeafName != "" {
			names = append(names, s.LeafName)
		}
	}
	sort.Strings(names)
	return names
}

// installOneGitHubSkill performs the local install + sync registration for a
// single discovered skill from a GitHub repo.
func installOneGitHubSkill(owner, repo, githubURL string, skill foundSkill) error {
	// Determine the slug for local install.
	slug := skill.LeafName
	if slug == "" {
		slug = repo // root-level skill uses repo name as slug
	}

	// Use repo owner as the namespace prefix (GitHub org/user).
	dirName := namespacedSlug(owner, slug)
	syncState := loadSyncState()
	migrateToNamespacedDirs(syncState)

	installed, err := installSkillToAgents(dirName, skill.Files)
	if err != nil {
		return err
	}

	// Compute content hash for the installed files.
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
			GitHubSkill: skill.LeafName,
		},
	}

	// If logged in, register the skill on the server with GitHub provenance.
	cfg, _ := config.Load()
	token, _ := config.LoadToken()
	if cfg != nil && token != nil && token.ExpiresAt > 0 {
		client := newAPIClient(cfg, token)
		if serverSkill, createErr := client.createSkillWithGitHub(slug, githubURL, skill.LeafName); createErr == nil {
			entry.SkillID = serverSkill.ID
			entry.ContentHash = serverSkill.ContentHash
		}
	}

	syncState.Skills[dirName] = entry
	saveSyncState(syncState)

	fmt.Println()
	for _, line := range installed {
		fmt.Println(line)
	}
	fmt.Printf("Installed %s from GitHub to %d agents\n", slug, len(installed))
	fmt.Printf("  %s %s\n", dim("Source:"), githubURL)
	return nil
}

// addFromGitHub handles `airskills add github.com/owner/repo [--skill name] [--all]`.
// Downloads from GitHub, installs locally, registers on airskills.ai with
// GitHub provenance, and stores the GitHub URL in sync state for future
// update checking.
func addFromGitHub(rawURL string) error {
	owner, repo, skillPath, err := parseGitHubURL(rawURL)
	if err != nil {
		return err
	}
	githubURL := fmt.Sprintf("https://github.com/%s/%s", owner, repo)

	fmt.Printf("  %s %s\n", cyan("↓"), fmt.Sprintf("Fetching from %s/%s on GitHub...", owner, repo))

	allFiles, err := downloadGitHubTarball(owner, repo)
	if err != nil {
		return err
	}

	// URL path selector takes priority and is always single-skill.
	if skillPath != "" {
		skills := scanSkillRoots(allFiles)
		if len(skills) == 0 {
			return fmt.Errorf("no skills found in %s/%s (no SKILL.md files)", owner, repo)
		}
		skill, resolveErr := resolveSkillSelector(skillPath, skills)
		if resolveErr != nil {
			return resolveErr
		}
		if addPreview {
			return previewSkill(owner, repo, skill)
		}
		return installOneGitHubSkill(owner, repo, githubURL, skill)
	}

	// When --skill contains a "/" (path-based disambiguation), use the raw scan
	// so the user can resolve a leaf-name collision they saw from a prior error.
	if addSkillFlag != "" && strings.Contains(addSkillFlag, "/") {
		skills := scanSkillRoots(allFiles)
		if len(skills) == 0 {
			return fmt.Errorf("no skills found in %s/%s (no SKILL.md files)", owner, repo)
		}
		selectors := strings.Split(addSkillFlag, ",")
		for _, sel := range selectors {
			sel = strings.TrimSpace(sel)
			skill, resolveErr := resolveSkillSelector(sel, skills)
			if resolveErr != nil {
				return resolveErr
			}
			if addPreview {
				if err := previewSkill(owner, repo, skill); err != nil {
					return err
				}
				continue
			}
			if err := installOneGitHubSkill(owner, repo, githubURL, skill); err != nil {
				return err
			}
		}
		return nil
	}

	// Normal path: collision detection via findSkillsInFiles.
	skills, err := findSkillsInFiles(allFiles)
	if err != nil {
		return err
	}
	if len(skills) == 0 {
		return fmt.Errorf("no skills found in %s/%s (no SKILL.md files)", owner, repo)
	}

	// --all: install every skill.
	if addAllFlag {
		names := displayLeafNames(skills)
		fmt.Printf("  Installing %d skills: %s\n", len(skills), strings.Join(names, ", "))
		for _, skill := range skills {
			if addPreview {
				if err := previewSkill(owner, repo, skill); err != nil {
					return err
				}
				continue
			}
			if err := installOneGitHubSkill(owner, repo, githubURL, skill); err != nil {
				return fmt.Errorf("failed to install %q: %w", skill.LeafName, err)
			}
		}
		return nil
	}

	// --skill with comma-separated names.
	if addSkillFlag != "" {
		selectors := strings.Split(addSkillFlag, ",")
		for _, sel := range selectors {
			sel = strings.TrimSpace(sel)
			skill, resolveErr := resolveSkillSelector(sel, skills)
			if resolveErr != nil {
				return resolveErr
			}
			if addPreview {
				if err := previewSkill(owner, repo, skill); err != nil {
					return err
				}
				continue
			}
			if err := installOneGitHubSkill(owner, repo, githubURL, skill); err != nil {
				return err
			}
		}
		return nil
	}

	// Single skill: auto-select.
	if len(skills) == 1 {
		skill := skills[0]
		if addPreview {
			return previewSkill(owner, repo, skill)
		}
		return installOneGitHubSkill(owner, repo, githubURL, skill)
	}

	// Multiple skills, no selector: list them.
	names := displayLeafNames(skills)
	return fmt.Errorf("multiple skills found in repo. Use --skill to pick one, or --all:\n  %s", strings.Join(names, "\n  "))
}

// previewSkill prints the content of a skill's files without installing.
func previewSkill(owner, repo string, skill foundSkill) error {
	slug := skill.LeafName
	if slug == "" {
		slug = repo
	}
	fmt.Printf("\n--- %s/%s (%s) ---\n", owner, repo, slug)
	// Print SKILL.md first for readability.
	if data, ok := skill.Files["SKILL.md"]; ok {
		fmt.Printf("\n=== SKILL.md ===\n%s\n", string(data))
	}
	for path, data := range skill.Files {
		if path == "SKILL.md" {
			continue
		}
		fmt.Printf("\n=== %s ===\n%s\n", path, string(data))
	}
	fmt.Printf("\nPreview only — run without --preview to install.\n")
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
		owner, repo, _, err := parseGitHubURL(src.GitHubURL)
		if err != nil {
			continue
		}

		allFiles, err := downloadGitHubTarball(owner, repo)
		if err != nil {
			continue
		}

		// Use raw scan — we know which skill we want by leaf name.
		skills := scanSkillRoots(allFiles)
		var selectedFiles map[string][]byte
		for _, s := range skills {
			if s.LeafName == src.GitHubSkill {
				selectedFiles = s.Files
				break
			}
		}
		if selectedFiles == nil {
			continue
		}

		// Check if SKILL.md content changed.
		newContent, hasSkillMd := selectedFiles["SKILL.md"]
		if !hasSkillMd {
			continue
		}
		newHash := sha256Hex(newContent)
		if newHash == src.ContentHash {
			continue // no changes
		}

		// Update local files.
		home, _ := os.UserHomeDir()
		primaryDir := filepath.Join(home, ".claude", "skills", dirName)
		for relPath, data := range selectedFiles {
			fullPath := filepath.Join(primaryDir, relPath)
			os.MkdirAll(filepath.Dir(fullPath), 0755)
			os.WriteFile(fullPath, data, 0644)
		}

		// Mirror to other agents.
		installSkillToAgents(dirName, selectedFiles)

		// Update sync state.
		entry.Source.ContentHash = newHash
		saveSyncState(syncState)

		fmt.Printf("  %s Updated %s from GitHub\n", green("✓"), dirName)
	}
}
