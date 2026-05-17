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

// namespacedSlug returns the local directory name for a sourced skill.
// Skills installed from the marketplace use the "{owner}-{slug}" format so
// that same-named skills from different owners can coexist. Skills without
// an owner (user-created, local) are stored under the bare slug.
func namespacedSlug(owner, slug string) string {
	if owner == "" {
		return slug
	}
	return owner + "-" + slug
}

// contextSlug returns the local directory prefix for a skill based on its
// distribution source:
//   - Org-distributed skills (SkillsetSlug set): use the skillset slug
//   - Personal skills: use the owner's username
//
// This implements the confirmed naming rule: org-distributed skills are
// scoped to the skillset (e.g. "acme-dev-retro"), personal skills to the
// owner (e.g. "chrismdp-retro"). The fork origin is stored in metadata, not
// encoded in the directory name.
func contextSlug(source *skillSource) string {
	if source.SkillsetSlug != "" {
		return source.SkillsetSlug
	}
	return source.Owner
}

// migrateToNamespacedDirs renames skill directories from the old bare-slug
// format to the namespaced "{owner}-{slug}" format for skills that were
// installed via `airskills add` (identified by having a Source in sync state).
// User-created skills (no Source) are left untouched.
//
// This handles the upgrade path for machines that installed skills before
// namespaced directory names were introduced.
func migrateToNamespacedDirs(syncState *SyncState) {
	home, _ := os.UserHomeDir()

	// Collect migrations to perform (iterate over copy to avoid map mutation during range)
	type migration struct {
		oldKey      string
		expectedKey string
		entry       *SyncEntry
	}
	var migrations []migration
	for oldKey, entry := range syncState.Skills {
		if entry == nil || entry.Source == nil {
			continue
		}
		ctx := contextSlug(entry.Source)
		if ctx == "" {
			continue // no context to namespace by — leave bare
		}
		expectedKey := namespacedSlug(ctx, entry.Source.Slug)
		if oldKey == expectedKey {
			continue // already namespaced
		}
		migrations = append(migrations, migration{oldKey, expectedKey, entry})
	}

	for _, m := range migrations {
		for _, a := range agents {
			globalPath := resolveGlobalDir(home, a.GlobalDir)
			oldDir := filepath.Join(globalPath, m.oldKey)
			newDir := filepath.Join(globalPath, m.expectedKey)
			if _, err := os.Stat(oldDir); err != nil {
				continue // not present in this agent dir
			}
			if _, err := os.Stat(newDir); err == nil {
				continue // new location already occupied — don't clobber
			}
			os.Rename(oldDir, newDir)
		}
		syncState.Skills[m.expectedKey] = m.entry
		delete(syncState.Skills, m.oldKey)
	}
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
	slug       string
	target     string // the dir mirror restored content into
	isNonFork  bool   // sourced skill the user does not own → use --keep-remote
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
		var markerHash string
		if syncState != nil {
			if e, ok := syncState.Skills[slug]; ok && e != nil {
				marker = e
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
		var restoredInto string
		for _, dir := range detectedDirs {
			target := filepath.Join(dir, slug)
			if existingHash, ok := hashByPath[target]; ok && existingHash == authorHash {
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
				if previouslyEmpty && restoredInto == "" {
					restoredInto = target
				}
			}
		}
		changes = append(changes, change)

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

// pickAuthoritativeHash chooses which version of a slug's content should
// win when its copies have diverged across agent directories.
//
//   - Single distinct hash → that hash wins.
//   - Marker disambiguates a 2-way split (exactly one group matches the
//     sync-state marker) → newest-touched group wins. This must work
//     regardless of whether the marker is the *pre-edit baseline* (the
//     non-marker group is the user's edit and will have a newer mtime)
//     or the *post-edit confirmation* (the marker group is what was
//     just pushed and has a newer mtime than stale siblings that
//     haven't been mirrored forward yet). The naive "non-marker is the
//     edit" rule inverts in the post-push case and silently overwrites
//     the edit with the stale content — see
//     doc/changes/cli-mirror-overwrites-edit-after-push.md.
//   - Otherwise → newest SKILL.md mtime across all paths wins. Handles
//     the stale-mirror-vs-fresh-edit case where the marker matches
//     neither group.
//
// Returns "" only when no paths are stat-able, in which case the caller
// reports a conflict and skips.
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
		if markerPaths, ok := hashGroups[markerHash]; ok {
			var otherHash string
			for h := range hashGroups {
				if h != markerHash {
					otherHash = h
				}
			}
			markerMtime := newestSkillMtime(markerPaths)
			otherMtime := newestSkillMtime(hashGroups[otherHash])
			if !markerMtime.IsZero() && !otherMtime.IsZero() {
				if markerMtime.After(otherMtime) {
					return markerHash
				}
				return otherHash
			}
			// Mtimes unavailable for one or both groups — fall back to
			// the legacy "non-marker is the edit" rule for the
			// pre-push case. Better than silently dropping the slug.
			return otherHash
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

// newestSkillMtime returns the most recent SKILL.md mtime across the
// given skill directory paths. Zero value if none are stat-able.
func newestSkillMtime(skillPaths []string) time.Time {
	var newest time.Time
	for _, p := range skillPaths {
		info, err := os.Stat(filepath.Join(p, "SKILL.md"))
		if err != nil {
			continue
		}
		if newest.IsZero() || info.ModTime().After(newest) {
			newest = info.ModTime()
		}
	}
	return newest
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
