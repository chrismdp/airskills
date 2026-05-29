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
	Use:   "rm <name>",
	Short: "Remove a skill locally and on the server",
	Long: `Removes a skill from this machine and from your airskills.ai account.

Use this instead of 'rm -rf ~/.claude/skills/<name>'. Hand-deleting an
agent dir is silently restored by the next 'airskills sync' — sync's
mirror fans the skill back out from any sibling agent dir that still
has a copy, by design (it's the same mechanism that makes hand-edits
propagate across agents).

By default deletes both the local directory (across all detected agents)
and the remote skill, then drops the entry from sync state. Use --keep-remote
to delete only locally, or --keep-local to delete only on the server.

Use --pending to discard a parked pending-conflict copy (left in temp by a
collision during 'add' or 'sync') WITHOUT touching the installed skill or the
server. This is the safe way to clear an "N pending conflict" status warning
when a real skill of the same name is installed.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
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
