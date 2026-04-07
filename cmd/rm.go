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

var rmCmd = &cobra.Command{
	Use:   "rm <name>",
	Short: "Remove a skill locally and on the server",
	Long: `Removes a skill from this machine and from your airskills.ai account.

By default deletes both the local directory (across all detected agents)
and the remote skill, then drops the entry from sync state. Use --keep-remote
to delete only locally, or --keep-local to delete only on the server.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		if err := validateSkillName(name); err != nil {
			return err
		}

		syncState := loadSyncState()
		entry, tracked := syncState.Skills[name]

		// Find local copies
		localSkills, _ := scanSkillsFromAgents()
		_, hasLocal := localSkills[name]

		if !tracked && !hasLocal {
			return fmt.Errorf("no skill named %q found locally or in sync state", name)
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
	rootCmd.AddCommand(rmCmd)
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
