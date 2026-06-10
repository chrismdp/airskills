package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
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
	{"openclaw", "OpenClaw", ".agents/skills", ".openclaw/skills"},
	{"hermes", "Hermes Agent", ".agents/skills", ".hermes/skills"},
}

// projectSkillsDir returns the absolute path to $CWD/.agents/skills if it
// already exists. This is the standard repo-local Agent Skills path from
// agentskills.io — when a repo has opted in by creating it, airskills
// mirrors skills into it like any other detected agent dir. Returns "" if
// the directory does not exist; airskills must never create it.
func projectSkillsDir() string {
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}
	p := filepath.Join(cwd, ".agents", "skills")
	if info, err := os.Stat(p); err == nil && info.IsDir() {
		return p
	}
	return ""
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

// installSkillToAgents writes a skill folder to all detected agents.
//
// SKILL.md is rewritten in place to ensure `name:` matches `slug` (the
// dir name). The agentskills.io spec requires this, and the server's
// archive PUT enforces it on push. Without this, a stale archive
// (e.g. an org skill pulled before mig 047 renamed it bare) would
// install with `name: <orgslug>-<slug>` in the frontmatter and any
// subsequent push would 400 with name_slug_mismatch.
func installSkillToAgents(slug string, files map[string][]byte) ([]string, error) {
	if skillMd, ok := files["SKILL.md"]; ok {
		if fixed, changed := fixSkillNameInContent(slug, skillMd); changed {
			files["SKILL.md"] = fixed
		}
	}
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
			if err := os.WriteFile(target, content, fileMode(content)); err != nil {
				continue
			}
		}

		installed = append(installed, fmt.Sprintf("  → %-16s %s", a.Name, skillDir))
	}

	if projectDir := projectSkillsDir(); projectDir != "" && !seen[projectDir] {
		seen[projectDir] = true
		skillDir := filepath.Join(projectDir, slug)
		if err := os.MkdirAll(skillDir, 0755); err == nil {
			for name, content := range files {
				target := filepath.Join(skillDir, name)
				os.MkdirAll(filepath.Dir(target), 0755)
				_ = os.WriteFile(target, content, fileMode(content))
			}
			installed = append(installed, fmt.Sprintf("  → %-16s %s", "Project", skillDir))
		}
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

	if projectDir := projectSkillsDir(); projectDir != "" && !seen[projectDir] {
		seen[projectDir] = true
		entries, err := os.ReadDir(projectDir)
		if err == nil {
			for _, e := range entries {
				if !e.IsDir() {
					continue
				}
				skillMd := filepath.Join(projectDir, e.Name(), "SKILL.md")
				if _, err := os.Stat(skillMd); err == nil {
					if _, exists := skills[e.Name()]; !exists {
						skills[e.Name()] = filepath.Join(projectDir, e.Name())
					}
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

	if projectDir := projectSkillsDir(); projectDir != "" && !seenGlobal[projectDir] {
		seenGlobal[projectDir] = true
		detectedGlobalDirs = append(detectedGlobalDirs, projectDir)
		entries, err := os.ReadDir(projectDir)
		if err == nil {
			for _, e := range entries {
				if !e.IsDir() {
					continue
				}
				skillDir := filepath.Join(projectDir, e.Name())
				if _, err := os.Stat(filepath.Join(skillDir, "SKILL.md")); err != nil {
					continue
				}
				slugToPaths[e.Name()] = append(slugToPaths[e.Name()], skillDir)
			}
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

// mirrorRestoreHint describes a slug for which mirror just refilled a
// previously-empty agent dir. Surfaced as a one-line educational nudge —
// hand-`rm` of an agent dir is not a supported delete signal; users should
// run `airskills rm` instead. See
// platform/doc/changes/cli-mirror-cannot-distinguish-delete-from-never-installed.md.
type mirrorRestoreHint struct {
	slug      string
	target    string // the dir mirror restored content into
	isNonFork bool   // sourced skill the user does not own → use --keep-remote
}

// mirrorWarnedSlugs tracks which conflict slugs have already been reported to
// the user in this process, so `sync` (which runs push then pull back-to-back)
// doesn't print the same warning twice.
var mirrorWarnedSlugs = map[string]bool{}

// printMirrorRestoreHints emits a one-line educational nudge for each slug
// where mirror refilled a previously-empty agent dir this run. Hand-`rm`
// is not a supported delete signal — sync restores it. Tell the user how
// to actually delete the skill across all agents.
//
// The hint fires every time mirror restores, no memoisation. Repeated
// reminders are the price of the simpler model; the user runs `airskills
// rm` once and the noise stops.
func printMirrorRestoreHints(hints []mirrorRestoreHint) {
	for _, h := range hints {
		fmt.Fprintf(os.Stderr, "  %s %s restored to %s\n", yellow("!"), h.slug, h.target)
		if h.isNonFork {
			fmt.Fprintf(os.Stderr, "      To delete this skill across all agents, run: airskills rm %s --keep-remote\n", h.slug)
		} else {
			fmt.Fprintf(os.Stderr, "      To delete this skill across all agents, run: airskills rm %s\n", h.slug)
		}
		fmt.Fprintf(os.Stderr, "      Hand-deleting a single agent dir is silently restored by sync.\n")
	}
}

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
		fmt.Fprintf(os.Stderr, "  %s %s was edited differently in two agent copies — a local fork, leaving it untouched (not pushed):\n", yellow("!"), c.slug)
		for _, p := range c.paths {
			fmt.Fprintf(os.Stderr, "      %s\n", p)
		}
	}
	fmt.Fprintf(os.Stderr, "  Reconcile the copies — edit them to match (the version you want wins), then re-run.\n")
	if isTTY {
		fmt.Fprintf(os.Stderr, "  Easiest from inside an agent (e.g. Claude Code), which can diff and merge them for you.\n")
	} else {
		fmt.Fprintf(os.Stderr, "  If you are an agent, diff the copies and merge them with the user before re-running.\n")
	}
}

// mirrorLocalSkills normalises local skill copies across every detected agent
// directory so that an edit in any folder is propagated to all the others.
//
// For each slug it classifies the copies against their per-copy baselines
// (classifyCopyDivergence), then:
//   - one distinct version across all copies → mirror to every detected dir
//     that's missing or out-of-date
//   - exactly one copy moved vs its baseline → that edit is authoritative and
//     is mirrored to the rest
//   - two or more copies moved to different content → a local fork: reported
//     as a conflict and left untouched (never flattened)
//   - no complete per-copy ledger yet (first run / untracked / a freshly-added
//     agent dir) → reported as a conflict rather than guessed. No file mtime,
//     and never the marker's single hash, is consulted.
//
// After a non-conflict pass it stamps each present copy's baseline to the
// authoritative hash (recordCopyBaselines); identical copies converge and seed
// the ledger on their own, so a divergence only ever needs resolving once
// history exists.
//
// When mirror writes content into a previously-empty target dir (the slug
// was missing from that agent), the slug is also surfaced as a
// mirrorRestoreHint so the caller can educate the user — hand-`rm` is
// not a supported delete signal. The hint fires every time mirror
// restores; we deliberately don't memoise. Repeated reminders are the
// price of the simpler model, and it lines up with "the warning shows
// every sync until the user runs airskills rm."
//
// Returns the list of slugs actually touched, the list of conflicting
// slugs (so callers can print a warning and skip them during push/pull),
// and any fresh restore hints.
func mirrorLocalSkills(syncState *SyncState) ([]mirrorChange, []mirrorConflict, []mirrorRestoreHint) {
	slugToPaths, detectedDirs, err := scanSkillsAllPaths()
	if err != nil || len(detectedDirs) == 0 {
		return nil, nil, nil
	}

	var changes []mirrorChange
	var conflicts []mirrorConflict
	var hints []mirrorRestoreHint

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

		var marker *SyncEntry
		if syncState != nil {
			if e, ok := syncState.Skills[slug]; ok && e != nil {
				marker = e
			}
		}

		// Decide which copy is authoritative from each copy's own recorded
		// per-copy baseline. Never file mtime, never the marker's single hash.
		// Without complete per-copy history, or on independent edits to
		// different content (a local fork), surface it — never flatten one
		// silently.
		authorHash, forkPaths := classifyCopyDivergence(marker, paths, hashByPath, hashGroups)
		if authorHash == "" {
			if len(forkPaths) == 0 {
				forkPaths = paths
			}
			conflicts = append(conflicts, mirrorConflict{slug: slug, paths: forkPaths})
			continue
		}

		authorPath := hashGroups[authorHash][0]
		authorFiles := filesByPath[authorPath]

		change := mirrorChange{slug: slug}
		var restoredInto string
		// Dirs that hold the authoritative content after this pass — used to
		// re-stamp the per-copy ledger so it stays fresh and self-heals.
		var authoritativeDirs []string
		for _, dir := range detectedDirs {
			target := filepath.Join(dir, slug)
			if existingHash, ok := hashByPath[target]; ok && existingHash == authorHash {
				authoritativeDirs = append(authoritativeDirs, target)
				continue
			}
			// "Previously empty target" = no SKILL.md was discovered at
			// this path by scanSkillsAllPaths; hashByPath has no entry.
			// Distinguishes restore (hand-rm or first-time install in a
			// new agent) from edit fan-out, where the target had stale
			// bytes that need overwriting.
			previouslyEmpty := false
			if _, seen := hashByPath[target]; !seen {
				previouslyEmpty = true
			}
			if err := replaceSkillDir(target, authorFiles); err == nil {
				change.written = append(change.written, target)
				authoritativeDirs = append(authoritativeDirs, target)
				if previouslyEmpty && restoredInto == "" {
					restoredInto = target
				}
			}
		}
		changes = append(changes, change)

		// Record where the authoritative content now lives so the next run
		// can tell which copy moved. Only for tracked skills (marker != nil).
		recordCopyBaselines(marker, authoritativeDirs, authorHash)

		if restoredInto != "" {
			hints = append(hints, mirrorRestoreHint{
				slug:      slug,
				target:    restoredInto,
				isNonFork: markerIsNonFork(marker),
			})
		}
	}

	return changes, conflicts, hints
}

// markerIsNonFork reports whether the marker tracks a sourced skill that
// the user does not own (a plain `airskills add` install, no local fork
// created yet). For these, plain `airskills rm` would 403 against the
// upstream owner — the hint must steer to `--keep-remote` instead.
func markerIsNonFork(m *SyncEntry) bool {
	if m == nil || m.Source == nil {
		return false
	}
	if m.SkillID == "" {
		return true
	}
	return m.SkillID == m.Source.ID || m.SkillID == m.Source.UpstreamSkillID
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
