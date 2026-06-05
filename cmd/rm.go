package cmd

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

var rmForce bool
var rmKeepRemote bool
var rmKeepLocal bool
var rmPending bool

var rmCmd = &cobra.Command{
	Use:   "rm <name> | <name>/<path>",
	Short: "Remove a skill, or a single file within a skill, locally and on the server",
	Long: `Removes a skill from this machine and from your airskills.ai account.

Use this instead of 'rm -rf ~/.claude/skills/<name>'. Hand-deleting an
agent dir is silently restored by the next 'airskills sync' — sync's
mirror fans the skill back out from any sibling agent dir that still
has a copy, by design (it's the same mechanism that makes hand-edits
propagate across agents).

By default deletes both the local directory (across all detected agents)
and the remote skill, then drops the entry from sync state. Use --keep-remote
to delete only locally, or --keep-local to delete only on the server.

FILE-LEVEL REMOVAL: pass a path inside a skill — 'airskills rm triage/scripts/foo.sh'
— to remove one file rather than the whole skill. Hand-deleting a file then
pushing does NOT work: the mirror resurrects it from any sibling agent dir
that still has a copy. This command deletes the file from EVERY detected agent
copy first (so the mirror has nothing to copy back), then pushes the skill so
the server and the marker drop it too. Use --keep-remote to remove the file
only locally without pushing. SKILL.md cannot be removed this way — to delete
the whole skill, run 'airskills rm <skill>'.

Use --pending to discard a parked pending-conflict copy (left in temp by a
collision during 'add' or 'sync') WITHOUT touching the installed skill or the
server. This is the safe way to clear an "N pending conflict" status warning
when a real skill of the same name is installed.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		// Path form ('<skill>/<file>') removes a single file within a skill
		// rather than the whole skill. Branch before validateSkillName, which
		// rejects the '/' a path form requires.
		if strings.Contains(args[0], "/") {
			return runRemoveSkillFile(args[0])
		}

		name := args[0]
		if err := validateSkillName(name); err != nil {
			return err
		}

		// --pending discards ONLY the parked conflict copy in temp, never
		// the installed skill or the server copy. This is the path status
		// recommends, so it must be safe regardless of whether a real skill
		// of the same name is installed (the common case — the conflict
		// arose precisely because one is).
		if rmPending {
			return discardPendingConflict(name, rmForce)
		}

		syncState := loadSyncState()
		entry, tracked := syncState.Skills[name]

		// Find local copies
		localSkills, _ := scanSkillsFromAgents()
		_, hasLocal := localSkills[name]

		if !tracked && !hasLocal {
			if len(pendingConflictDirs(name)) == 0 {
				return fmt.Errorf("no skill named %q found locally, in sync state, or in pending conflicts", name)
			}
			return discardPendingConflict(name, rmForce)
		}

		// A real skill is being deleted, but a parked conflict copy of the
		// same name also exists. Make sure the user knows this command
		// targets the installed skill, not that copy — and point at the
		// safe path. Printed even under --force so it can't be missed.
		if len(pendingConflictDirs(name)) > 0 {
			fmt.Printf("  %s a pending conflict copy for %q also exists in temp. This command deletes the\n", yellow("⚠"), name)
			fmt.Printf("    installed skill, NOT that copy. To discard only the parked copy, run: airskills rm %s --pending\n", name)
		}

		// Confirmation
		if !rmForce {
			parts := []string{}
			if hasLocal && !rmKeepLocal {
				parts = append(parts, "local files")
			}
			if tracked && entry.SkillID != "" && !rmKeepRemote {
				parts = append(parts, "remote skill")
			}
			if len(parts) == 0 {
				return fmt.Errorf("nothing to do (skill is not local and has no remote ID)")
			}
			fmt.Printf("Delete %s for skill %q? [y/N] ", strings.Join(parts, " and "), name)
			reader := bufio.NewReader(os.Stdin)
			answer, _ := reader.ReadString('\n')
			if strings.TrimSpace(strings.ToLower(answer)) != "y" {
				fmt.Println("Aborted.")
				return nil
			}
		}

		// Delete on server first — if it fails, leave local intact so the user
		// can retry without ending up in a half-deleted state.
		if tracked && entry.SkillID != "" && !rmKeepRemote {
			client, err := newAPIClientAuto()
			if err != nil {
				return fmt.Errorf("server delete requires login: %w", err)
			}
			if err := client.del(fmt.Sprintf("/api/v1/skills/%s", entry.SkillID)); err != nil {
				return fmt.Errorf("deleting remote skill: %w", err)
			}
			fmt.Printf("  %s remote skill deleted\n", green("✓"))
		}

		// Delete locally
		if !rmKeepLocal {
			removed, err := removeLocalSkill(name)
			if err != nil {
				return fmt.Errorf("removing local files: %w", err)
			}
			for _, p := range removed {
				fmt.Printf("  %s removed %s\n", green("-"), p)
			}
		}

		// Drop sync state entry
		delete(syncState.Skills, name)
		if err := saveSyncState(syncState); err != nil {
			return fmt.Errorf("saving sync state: %w", err)
		}

		return nil
	},
}

func init() {
	rmCmd.Flags().BoolVarP(&rmForce, "force", "f", false, "Skip confirmation")
	rmCmd.Flags().BoolVar(&rmKeepRemote, "keep-remote", false, "Only delete locally; leave remote skill")
	rmCmd.Flags().BoolVar(&rmKeepLocal, "keep-local", false, "Only delete remote; leave local files")
	rmCmd.Flags().BoolVar(&rmPending, "pending", false, "Discard only the parked pending-conflict copy in temp; never touch the installed skill or server")
	rootCmd.AddCommand(rmCmd)
}

// runRemoveSkillFile handles the path form of `airskills rm` —
// `<skill>/<relpath>` — which removes a single file from a skill rather
// than the whole skill. It deletes the file from every detected agent copy
// (defeating the sync mirror, which would otherwise resurrect it from a
// sibling dir) and then pushes the skill so the server and marker drop it
// too. --keep-remote stops before the push for a local-only removal.
func runRemoveSkillFile(arg string) error {
	parts := strings.SplitN(arg, "/", 2)
	name, relPath := parts[0], parts[1]

	if err := validateSkillName(name); err != nil {
		return err
	}
	if err := validateSkillFileRelPath(relPath); err != nil {
		return err
	}
	if rmKeepLocal {
		return fmt.Errorf("--keep-local is not supported for file-level removal; a file is removed locally and the change pushed (use --keep-remote to skip the push)")
	}
	if rmPending {
		return fmt.Errorf("--pending applies to whole-skill conflict copies, not single files")
	}

	// The skill must be installed locally — file removal operates on the
	// working tree, then pushes it.
	localSkills, _ := scanSkillsFromAgents()
	if _, ok := localSkills[name]; !ok {
		return fmt.Errorf("no skill named %q found locally", name)
	}

	if !rmForce {
		scope := "all detected agent copies, then push the change"
		if rmKeepRemote {
			scope = "all detected agent copies (local only; not pushed)"
		}
		fmt.Printf("Remove %s from skill %q in %s? [y/N] ", relPath, name, scope)
		reader := bufio.NewReader(os.Stdin)
		answer, _ := reader.ReadString('\n')
		if strings.TrimSpace(strings.ToLower(answer)) != "y" {
			fmt.Println("Aborted.")
			return nil
		}
	}

	removed, err := removeLocalSkillFile(name, relPath)
	if err != nil {
		return err
	}
	for _, p := range removed {
		fmt.Printf("  %s removed %s\n", green("-"), p)
	}

	if rmKeepRemote {
		fmt.Printf("  %s removed locally only. Run 'airskills push %s' to drop it on the server too.\n", yellow("!"), name)
		return nil
	}

	// Push the now-smaller skill so the server and marker hash drop the file.
	// The mirror inside push sees every agent copy already missing the file,
	// so it finds a single consistent version and won't restore anything.
	fmt.Printf("  %s pushing %s to drop the file on the server...\n", dim("·"), name)
	// The user has already decided to delete; stop the push's deletion
	// resolver from spotting the now-missing file and offering to restore it.
	suppressDeletionPrompt = true
	defer func() { suppressDeletionPrompt = false }()
	if err := pushCmd.RunE(pushCmd, []string{name}); err != nil {
		return fmt.Errorf("file removed locally, but push failed: %w\n  Run 'airskills push %s' to finish dropping it on the server.", err, name)
	}
	return nil
}

// discardPendingConflict removes only the parked conflict copies for name
// (the dirs under /tmp/airskills-conflicts*). It never touches the
// installed skill or the server, so it is safe to recommend even when a
// real same-named skill exists.
func discardPendingConflict(name string, force bool) error {
	if len(pendingConflictDirs(name)) == 0 {
		return fmt.Errorf("no pending conflict copy named %q found in %s",
			name, filepath.Join(os.TempDir(), "airskills-conflicts*"))
	}
	if !force {
		fmt.Printf("Discard pending conflict copy for %q? Removes only the parked copy in temp; your installed skill is untouched. [y/N] ", name)
		reader := bufio.NewReader(os.Stdin)
		answer, _ := reader.ReadString('\n')
		if strings.TrimSpace(strings.ToLower(answer)) != "y" {
			fmt.Println("Aborted.")
			return nil
		}
	}
	removed, err := removePendingConflictDirs(name)
	if err != nil {
		return fmt.Errorf("discarding pending conflict: %w", err)
	}
	for _, p := range removed {
		fmt.Printf("  %s discarded pending conflict %s\n", green("-"), p)
	}
	return nil
}

func removePendingConflictDirs(name string) ([]string, error) {
	if err := validateSkillName(name); err != nil {
		return nil, err
	}
	dirs := pendingConflictDirs(name)
	removed := make([]string, 0, len(dirs))
	for _, dir := range dirs {
		if err := os.RemoveAll(dir); err != nil {
			return removed, fmt.Errorf("removing %s: %w", dir, err)
		}
		removed = append(removed, dir)
	}
	return removed, nil
}

// validateSkillFileRelPath checks a path-form argument to `airskills rm`
// (the part after `<skill>/`). It must be a safe relative path inside the
// skill directory — never absolute, never escaping via "..", never empty,
// and never the SKILL.md manifest (removing that silently breaks the skill;
// use `airskills rm <skill>` to delete the whole thing).
func validateSkillFileRelPath(relPath string) error {
	if relPath == "" {
		return fmt.Errorf("file path within the skill is required")
	}
	if filepath.IsAbs(relPath) {
		return fmt.Errorf("file path %q must be relative to the skill directory", relPath)
	}
	clean := filepath.ToSlash(filepath.Clean(relPath))
	if clean == ".." || strings.HasPrefix(clean, "../") || strings.Contains(clean, "/../") {
		return fmt.Errorf("file path %q must not escape the skill directory", relPath)
	}
	if clean == "." {
		return fmt.Errorf("file path %q is not a file within the skill", relPath)
	}
	if clean == "SKILL.md" {
		return fmt.Errorf("refusing to remove the skill manifest SKILL.md — to delete the whole skill run: airskills rm <skill>")
	}
	return nil
}

// removeLocalSkillFile deletes a single file (relPath) from the named skill
// in every detected agent directory, then prunes any parent directories the
// deletion left empty (up to, but never including, the skill root).
//
// Deleting from EVERY agent copy is the whole point: the sync mirror fans a
// skill back out from any sibling agent dir that still has a copy, so a
// hand-`rm` of one file in one dir is silently resurrected. Removing the
// file everywhere first gives the mirror nothing to copy back, and the
// follow-on push drops it from the server and updates the marker hash.
//
// Returns the absolute paths actually removed. Errors if the file existed in
// no agent copy (so the caller can tell the user nothing happened) or if the
// path is unsafe.
func removeLocalSkillFile(name, relPath string) ([]string, error) {
	if err := validateSkillName(name); err != nil {
		return nil, err
	}
	if err := validateSkillFileRelPath(relPath); err != nil {
		return nil, err
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}

	clean := filepath.FromSlash(filepath.Clean(relPath))

	var removed []string
	seen := map[string]bool{}

	for _, a := range agents {
		globalPath := resolveGlobalDir(home, a.GlobalDir)
		if seen[globalPath] {
			continue
		}
		seen[globalPath] = true

		skillDir := filepath.Join(globalPath, name)
		target := filepath.Join(skillDir, clean)
		info, statErr := os.Stat(target)
		if statErr != nil || info.IsDir() {
			continue
		}
		if err := os.Remove(target); err != nil {
			return removed, fmt.Errorf("removing %s: %w", target, err)
		}
		removed = append(removed, target)
		pruneEmptyParents(skillDir, filepath.Dir(target))
	}

	if len(removed) == 0 {
		return nil, fmt.Errorf("no file %q found in any installed copy of skill %q", relPath, name)
	}
	return removed, nil
}

// pruneEmptyParents removes now-empty directories from `dir` upward, stopping
// before `root` (the skill directory, which is never removed). Best-effort:
// stops at the first non-empty dir or any error.
func pruneEmptyParents(root, dir string) {
	for {
		if dir == root || !strings.HasPrefix(dir, root+string(os.PathSeparator)) {
			return
		}
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) > 0 {
			return
		}
		if err := os.Remove(dir); err != nil {
			return
		}
		dir = filepath.Dir(dir)
	}
}

// validateSkillName rejects empty strings, path separators, and traversal
// fragments. Skill names are directory names, never paths.
func validateSkillName(name string) error {
	if name == "" {
		return fmt.Errorf("skill name is required")
	}
	if name == "." || name == ".." {
		return fmt.Errorf("invalid skill name %q", name)
	}
	if strings.ContainsAny(name, "/\\") {
		return fmt.Errorf("skill name %q must not contain path separators", name)
	}
	if strings.Contains(name, "..") {
		return fmt.Errorf("skill name %q must not contain '..'", name)
	}
	return nil
}

// removeLocalSkill deletes the named skill directory from every detected
// agent's skills directory. Returns the absolute paths that were removed.
//
// It is safe to call when the skill is missing — returns an empty list.
// Path-traversal-style names are rejected up front.
func removeLocalSkill(name string) ([]string, error) {
	if err := validateSkillName(name); err != nil {
		return nil, err
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}

	var removed []string
	seen := map[string]bool{}

	for _, a := range agents {
		globalPath := resolveGlobalDir(home, a.GlobalDir)
		if seen[globalPath] {
			continue
		}
		seen[globalPath] = true

		skillDir := filepath.Join(globalPath, name)
		info, err := os.Stat(skillDir)
		if err != nil || !info.IsDir() {
			continue
		}
		if err := os.RemoveAll(skillDir); err != nil {
			return removed, fmt.Errorf("removing %s: %w", skillDir, err)
		}
		removed = append(removed, skillDir)
	}

	return removed, nil
}
