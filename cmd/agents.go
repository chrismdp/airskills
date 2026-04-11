package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

type agentDef struct {
	Key        string
	Name       string
	ProjectDir string // relative to project root
	GlobalDir  string // relative to home dir (unix), or absolute pattern
}

// Agent registry — mirrors vercel-labs/skills agent paths
var agents = []agentDef{
	{"claude-code", "Claude Code", ".claude/skills", ".claude/skills"},
	{"cursor", "Cursor", ".agents/skills", ".cursor/skills"},
	{"github-copilot", "GitHub Copilot", ".agents/skills", ".copilot/skills"},
	{"windsurf", "Windsurf", ".windsurf/skills", ".codeium/windsurf/skills"},
	{"codex", "Codex", ".agents/skills", ".codex/skills"},
	{"cline", "Cline", ".agents/skills", ".agents/skills"},
	{"roo", "Roo Code", ".roo/skills", ".roo/skills"},
	{"continue", "Continue", ".continue/skills", ".continue/skills"},
	{"gemini-cli", "Gemini CLI", ".agents/skills", ".gemini/skills"},
	{"augment", "Augment", ".augment/skills", ".augment/skills"},
	{"kiro-cli", "Kiro CLI", ".kiro/skills", ".kiro/skills"},
	{"junie", "Junie", ".junie/skills", ".junie/skills"},
	{"goose", "Goose", ".goose/skills", ".config/goose/skills"},
	{"trae", "Trae", ".trae/skills", ".trae/skills"},
	{"amp", "Amp", ".agents/skills", ".config/agents/skills"},
	{"opencode", "OpenCode", ".agents/skills", ".config/opencode/skills"},
	{"aider", "Aider", ".agents/skills", ".aider/skills"},
	{"amazon-q", "Amazon Q", ".amazonq/skills", ".amazonq/skills"},
	{"pi", "Pi", ".pi/skills", ".pi/agent/skills"},
}

// detectInstalledAgents returns agents whose global skills directory exists
func detectInstalledAgents() []agentDef {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}

	var found []agentDef
	seen := map[string]bool{} // dedupe by global dir

	for _, a := range agents {
		globalPath := resolveGlobalDir(home, a.GlobalDir)
		if seen[globalPath] {
			continue
		}

		// Check if the parent dir exists (e.g. ~/.claude/ for claude-code)
		parent := filepath.Dir(globalPath)
		if _, err := os.Stat(parent); err == nil {
			found = append(found, a)
			seen[globalPath] = true
		}
	}

	return found
}

// installSkillToAgents writes a skill folder to all detected agents
func installSkillToAgents(slug string, files map[string][]byte) ([]string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}

	detected := detectInstalledAgents()
	if len(detected) == 0 {
		// Fallback to Claude Code only
		detected = []agentDef{agents[0]}
	}

	var installed []string
	seen := map[string]bool{}

	for _, a := range detected {
		globalPath := resolveGlobalDir(home, a.GlobalDir)
		if seen[globalPath] {
			continue
		}
		seen[globalPath] = true

		skillDir := filepath.Join(globalPath, slug)
		if err := os.MkdirAll(skillDir, 0755); err != nil {
			continue
		}

		for name, content := range files {
			target := filepath.Join(skillDir, name)
			os.MkdirAll(filepath.Dir(target), 0755)
			if err := os.WriteFile(target, content, 0644); err != nil {
				continue
			}
		}

		installed = append(installed, fmt.Sprintf("  → %-16s %s", a.Name, skillDir))
	}

	return installed, nil
}

// scanSkillsFromAgents finds all local skills across all detected agents
func scanSkillsFromAgents() (map[string]string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}

	// map of slug -> path (first found wins)
	skills := map[string]string{}
	seen := map[string]bool{}

	for _, a := range agents {
		globalPath := resolveGlobalDir(home, a.GlobalDir)
		if seen[globalPath] {
			continue
		}
		seen[globalPath] = true

		entries, err := os.ReadDir(globalPath)
		if err != nil {
			continue
		}

		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			skillMd := filepath.Join(globalPath, e.Name(), "SKILL.md")
			if _, err := os.Stat(skillMd); err == nil {
				if _, exists := skills[e.Name()]; !exists {
					skills[e.Name()] = filepath.Join(globalPath, e.Name())
				}
			}
		}
	}

	return skills, nil
}

func resolveGlobalDir(home, relDir string) string {
	if runtime.GOOS == "windows" {
		// On Windows, use %USERPROFILE% (same as home)
		return filepath.Join(home, relDir)
	}
	return filepath.Join(home, relDir)
}

// scanSkillsAllPaths returns every local skill directory across all detected
// agent dirs, grouped by slug. Unlike scanSkillsFromAgents it does not dedupe
// by slug — multi-copy slugs keep every path — which is what mirrorLocalSkills
// needs to detect edits that live outside the first-found copy.
func scanSkillsAllPaths() (map[string][]string, []string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, nil, err
	}

	slugToPaths := map[string][]string{}
	var detectedGlobalDirs []string
	seenGlobal := map[string]bool{}

	for _, a := range agents {
		globalPath := resolveGlobalDir(home, a.GlobalDir)
		if seenGlobal[globalPath] {
			continue
		}
		parent := filepath.Dir(globalPath)
		if _, err := os.Stat(parent); err != nil {
			continue
		}
		seenGlobal[globalPath] = true
		detectedGlobalDirs = append(detectedGlobalDirs, globalPath)

		entries, err := os.ReadDir(globalPath)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			skillDir := filepath.Join(globalPath, e.Name())
			if _, err := os.Stat(filepath.Join(skillDir, "SKILL.md")); err != nil {
				continue
			}
			slugToPaths[e.Name()] = append(slugToPaths[e.Name()], skillDir)
		}
	}

	return slugToPaths, detectedGlobalDirs, nil
}

// mirrorChange describes a successful mirror for one slug.
type mirrorChange struct {
	slug    string
	written []string // target skill-dir paths that actually changed on disk
}

// mirrorConflict describes a slug whose local copies diverged in a way that
// mirror cannot safely reconcile. Reported to the user; push/pull should skip
// the slug until it's resolved.
type mirrorConflict struct {
	slug  string
	paths []string // every local path for the slug, for the user to diff/merge
}

// mirrorWarnedSlugs tracks which conflict slugs have already been reported to
// the user in this process, so `sync` (which runs push then pull back-to-back)
// doesn't print the same warning twice.
var mirrorWarnedSlugs = map[string]bool{}

// printMirrorConflicts warns the user about divergent local skill copies.
// In a TTY the message nudges the user to re-run inside an agent; in a
// headless context (e.g. an agent already running airskills) it nudges the
// agent to resolve the conflict with the user.
func printMirrorConflicts(conflicts []mirrorConflict) {
	if len(conflicts) == 0 {
		return
	}
	var fresh []mirrorConflict
	for _, c := range conflicts {
		if mirrorWarnedSlugs[c.slug] {
			continue
		}
		mirrorWarnedSlugs[c.slug] = true
		fresh = append(fresh, c)
	}
	if len(fresh) == 0 {
		return
	}
	for _, c := range fresh {
		fmt.Fprintf(os.Stderr, "  %s %s has divergent local copies — skipping\n", yellow("!"), c.slug)
		for _, p := range c.paths {
			fmt.Fprintf(os.Stderr, "      %s\n", p)
		}
	}
	if isTTY {
		fmt.Fprintf(os.Stderr, "  Re-run airskills inside an agent (e.g. Claude Code) to fix.\n")
	} else {
		fmt.Fprintf(os.Stderr, "  If you are an agent, fix this in collaboration with the user.\n")
	}
}

// mirrorLocalSkills normalises local skill copies across every detected agent
// directory so that an edit in any folder is propagated to all the others.
//
// For each slug:
//   - one distinct version across all copies → mirror to every detected dir
//     that's missing or out-of-date
//   - exactly two versions and the sync-state marker matches one of them →
//     the other is treated as the edit and mirrored
//   - anything else (two versions, neither matches the marker; three or more
//     versions) → reported as a conflict and left untouched
//
// Returns the list of slugs actually touched and the list of conflicting
// slugs so callers can print a warning and skip them during push/pull.
func mirrorLocalSkills(syncState *SyncState) ([]mirrorChange, []mirrorConflict) {
	slugToPaths, detectedDirs, err := scanSkillsAllPaths()
	if err != nil || len(detectedDirs) == 0 {
		return nil, nil
	}

	var changes []mirrorChange
	var conflicts []mirrorConflict

	for slug, paths := range slugToPaths {
		filesByPath := make(map[string]map[string][]byte, len(paths))
		hashByPath := make(map[string]string, len(paths))
		hashGroups := map[string][]string{}
		for _, p := range paths {
			files := readSkillFiles(p)
			h := computeMerkleHash(files)
			filesByPath[p] = files
			hashByPath[p] = h
			hashGroups[h] = append(hashGroups[h], p)
		}

		var markerHash string
		if syncState != nil {
			if e, ok := syncState.Skills[slug]; ok && e != nil {
				markerHash = e.ContentHash
			}
		}

		authorHash := pickAuthoritativeHash(paths, hashByPath, hashGroups, markerHash)
		if authorHash == "" {
			conflicts = append(conflicts, mirrorConflict{slug: slug, paths: paths})
			continue
		}

		authorPath := hashGroups[authorHash][0]
		authorFiles := filesByPath[authorPath]

		change := mirrorChange{slug: slug}
		for _, dir := range detectedDirs {
			target := filepath.Join(dir, slug)
			if existingHash, ok := hashByPath[target]; ok && existingHash == authorHash {
				continue
			}
			if err := replaceSkillDir(target, authorFiles); err == nil {
				change.written = append(change.written, target)
			}
		}
		changes = append(changes, change)
	}

	return changes, conflicts
}

// pickAuthoritativeHash chooses which version of a slug's content should
// win when its copies have diverged across agent directories.
//
//   - Single distinct hash → that hash wins.
//   - Marker disambiguates a 2-way split (exactly one group matches the
//     sync-state marker) → the non-marker group is the edit.
//   - Otherwise → newest SKILL.md mtime wins. This handles the case where
//     a prior mirror ran and fanned content out to a secondary agent dir,
//     and the user has since edited the original: the stale mirrored copy
//     has an older mtime, so the intentional edit wins.
//
// Returns "" only when the heuristics all fail (no stat-able paths), in
// which case the caller reports a conflict and skips.
func pickAuthoritativeHash(
	paths []string,
	hashByPath map[string]string,
	hashGroups map[string][]string,
	markerHash string,
) string {
	if len(hashGroups) == 1 {
		for h := range hashGroups {
			return h
		}
	}
	if len(hashGroups) == 2 && markerHash != "" {
		if _, ok := hashGroups[markerHash]; ok {
			for h := range hashGroups {
				if h != markerHash {
					return h
				}
			}
		}
	}
	newestPath := ""
	var newestTime time.Time
	for _, p := range paths {
		info, err := os.Stat(filepath.Join(p, "SKILL.md"))
		if err != nil {
			continue
		}
		if newestPath == "" || info.ModTime().After(newestTime) {
			newestPath = p
			newestTime = info.ModTime()
		}
	}
	if newestPath == "" {
		return ""
	}
	return hashByPath[newestPath]
}

// replaceSkillDir writes files into target, deleting any existing non-marker
// files that aren't in the new set. The .airskills marker (if any) is
// preserved — it's local per-machine state, not part of skill content.
func replaceSkillDir(target string, files map[string][]byte) error {
	if err := os.MkdirAll(target, 0755); err != nil {
		return err
	}

	// Remove stale files (anything currently in target that isn't in files
	// and isn't the .airskills marker).
	_ = filepath.Walk(target, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if info.Name() == ".airskills" {
			return nil
		}
		rel, relErr := filepath.Rel(target, path)
		if relErr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if _, keep := files[rel]; !keep {
			os.Remove(path)
		}
		return nil
	})

	// Write the new set.
	for rel, data := range files {
		dst := filepath.Join(target, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
			return err
		}
		if err := os.WriteFile(dst, data, 0644); err != nil {
			return err
		}
	}

	// Prune empty directories left behind by the deletions above.
	_ = filepath.Walk(target, func(path string, info os.FileInfo, err error) error {
		if err != nil || !info.IsDir() || path == target {
			return nil
		}
		entries, _ := os.ReadDir(path)
		if len(entries) == 0 {
			os.Remove(path)
		}
		return nil
	})

	return nil
}
