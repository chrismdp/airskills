package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

var mvCmd = &cobra.Command{
	Use:   "mv <old-name> <new-name>",
	Short: "Rename a skill (does not change ownership — use `transfer` for that)",
	Long: `Renames a skill's name across all detected agents and on the server.

This only changes the name. It does not change the owner, the commit
history, or the skill's identity. Consumers who installed your skill via
airskills add keep receiving updates because the CLI tracks skills by
ID, not by name.

To change ownership of a skill (user ↔ org), use 'airskills transfer'.
That is a deliberate, consumer-visible move; rename is not.`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		oldName, newName := args[0], args[1]
		if oldName == newName {
			return fmt.Errorf("old and new names are identical")
		}

		syncState := loadSyncState()
		entry, tracked := syncState.Skills[oldName]

		// Verify source exists locally — renameLocalSkill will surface
		// validation errors before we touch the server.
		moves, err := renameLocalSkill(oldName, newName)
		if err != nil {
			return err
		}
		for _, m := range moves {
			fmt.Printf("  %s %s → %s\n", green("→"), m.from, m.to)
		}

		// Update server-side name (best-effort if not logged in or untracked)
		if tracked && entry.SkillID != "" {
			client, err := newAPIClientAuto()
			if err != nil {
				fmt.Printf("  %s skipping server rename (not logged in)\n", yellow("!"))
			} else {
				body, status, err := client.put(
					fmt.Sprintf("/api/v1/skills/%s", entry.SkillID),
					map[string]interface{}{"name": newName},
				)
				if err != nil || status >= 400 {
					// Migration 047: a 409 from PUT /skills/{id} carries
					// either the legacy slug_conflict shape (same-namespace
					// collision) or the new conflict_with shape
					// (cross-namespace shadow from an assigned org skill).
					// Parse the latter and surface a clear hint; fall
					// through to the generic error otherwise.
					if status == 409 {
						parsed := parseSkillConflict(httpError(status, body))
						if parsed != nil {
							parsed.Slug = newName
							return parsed
						}
					}
					if err != nil {
						return fmt.Errorf("renaming on server: %w", err)
					}
					return fmt.Errorf("renaming on server: %w", httpError(status, body))
				}
				fmt.Printf("  %s remote skill renamed\n", green("✓"))
			}
		}

		// Move sync state entry
		if tracked {
			delete(syncState.Skills, oldName)
			syncState.Skills[newName] = entry
			if err := saveSyncState(syncState); err != nil {
				return fmt.Errorf("saving sync state: %w", err)
			}
		}

		return nil
	},
}

func init() {
	rootCmd.AddCommand(mvCmd)
}

type localMove struct {
	from string
	to   string
}

// renameLocalSkill renames the named skill directory across every detected
// agent's skills directory. Errors if the source is missing in all agents,
// or if any target already exists. Validates both names against path
// traversal first.
//
// On error after the first successful move, attempts to revert the moves
// already made so the user is not left in a half-renamed state.
func renameLocalSkill(oldName, newName string) ([]localMove, error) {
	if err := validateSkillName(oldName); err != nil {
		return nil, fmt.Errorf("old name: %w", err)
	}
	if err := validateSkillName(newName); err != nil {
		return nil, fmt.Errorf("new name: %w", err)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}

	type pair struct {
		from, to string
	}
	var planned []pair
	seen := map[string]bool{}
	for _, a := range agents {
		globalPath := resolveGlobalDir(home, a.GlobalDir)
		if seen[globalPath] {
			continue
		}
		seen[globalPath] = true

		from := filepath.Join(globalPath, oldName)
		to := filepath.Join(globalPath, newName)

		info, err := os.Stat(from)
		if err != nil || !info.IsDir() {
			continue
		}
		if _, err := os.Stat(to); err == nil {
			return nil, fmt.Errorf("target already exists: %s", to)
		}
		planned = append(planned, pair{from: from, to: to})
	}

	if len(planned) == 0 {
		return nil, fmt.Errorf("no skill named %q found locally", oldName)
	}

	var done []localMove
	for _, p := range planned {
		if err := os.Rename(p.from, p.to); err != nil {
			// Revert any successful moves so the user can retry cleanly.
			for _, d := range done {
				_ = os.Rename(d.to, d.from)
			}
			return nil, fmt.Errorf("renaming %s → %s: %w", p.from, p.to, err)
		}
		// Rewrite SKILL.md `name:` field to match the new directory.
		// Otherwise the next push will fail server-side name_slug_mismatch
		// validation. Best-effort — a missing or unparsable SKILL.md doesn't
		// abort the rename (push will surface it later if it matters).
		skillMd := filepath.Join(p.to, "SKILL.md")
		if content, err := os.ReadFile(skillMd); err == nil {
			if fixed, changed := fixSkillNameInContent(newName, content); changed {
				_ = os.WriteFile(skillMd, fixed, 0o644)
			}
		}
		done = append(done, localMove{from: p.from, to: p.to})
	}

	return done, nil
}
