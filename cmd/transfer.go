package cmd

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/chrismdp/airskills/telemetry"
	"github.com/spf13/cobra"
)

var transferToOrg string
var transferSlug string
var transferYes bool

var transferCmd = &cobra.Command{
	Use:   "transfer <skill-name>",
	Short: "Transfer a skill between user and org ownership",
	Long: `Move a skill from your personal namespace to an org you're a member of,
or from an org back to your personal namespace (org admins only).

Examples:
  airskills transfer deploy-check --to-org cherrypick
  airskills transfer deploy-check --to-user

Transfer is a deliberate, consumer-visible move — soft-delete + create.
The old <old-owner>/<slug> is archived; requests to it return 410 Gone
(no redirect). Consumers with access see the new location and can choose
whether to follow it; consumers without access see "upstream archived"
and keep their local copy.

On --to-org the skill becomes an org asset delivered through skillsets, so
your local copy is removed (backed up to ~/.airskills/undo) — re-add it to a
skillset you're assigned to in order to use it locally again. On --to-user the
skill becomes your personal skill and the local copy stays linked.

To restore the old slug, run 'airskills restore <old-slug>'.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		skillName := args[0]

		toUser, _ := cmd.Flags().GetBool("to-user")
		if transferToOrg == "" && !toUser {
			return fmt.Errorf("specify either --to-org <slug> or --to-user")
		}
		if transferToOrg != "" && toUser {
			return fmt.Errorf("--to-org and --to-user are mutually exclusive")
		}

		client, err := newAPIClientAuto()
		if err != nil {
			return err
		}

		skill, err := findSkillByName(client, skillName)
		if err != nil {
			return err
		}
		if skill == nil {
			return fmt.Errorf("skill %q not found in your account or any org you belong to", skillName)
		}

		// Resolve the target ID.
		var to map[string]string
		if transferToOrg != "" {
			orgID, err := lookupCallerOrgID(client, transferToOrg)
			if err != nil {
				return err
			}
			to = map[string]string{"kind": "org", "id": orgID}
		} else {
			profile, err := client.getMe()
			if err != nil {
				return fmt.Errorf("fetching profile: %w", err)
			}
			to = map[string]string{"kind": "user", "id": profile.Id.String()}
		}

		if !transferYes {
			fmt.Printf("\n  Skill:     %s\n", skill.Name)
			if transferToOrg != "" {
				fmt.Printf("  Move to:   org %s\n", transferToOrg)
			} else {
				fmt.Printf("  Move to:   your personal namespace\n")
			}
			fmt.Printf("\n  This is a permanent move: the old URL returns 410 Gone.\n")
			fmt.Printf("  Consumers with access will be shown the new location and\n")
			fmt.Printf("  can choose whether to follow it; consumers without access\n")
			fmt.Printf("  see \"upstream archived\" and keep their local copy.\n")
			if transferToOrg != "" {
				fmt.Printf("  Your local copy will be removed (backed up to ~/.airskills/undo);\n")
				fmt.Printf("  re-add it to a skillset you're on to use it locally again.\n")
			}
			fmt.Print("\n  Continue? [y/N] ")

			reader := bufio.NewReader(os.Stdin)
			answer, _ := reader.ReadString('\n')
			if strings.TrimSpace(strings.ToLower(answer)) != "y" {
				fmt.Println("  Aborted.")
				return nil
			}
		}

		payload := map[string]interface{}{"to": to}
		if transferSlug != "" {
			payload["slug"] = transferSlug
		}

		respBody, err := client.post(
			fmt.Sprintf("/api/v1/skills/%s/transfer", skill.Id),
			payload,
		)
		if err != nil {
			return fmt.Errorf("transfer failed: %w", err)
		}

		var updated apiSkill
		if jsonErr := json.Unmarshal(respBody, &updated); jsonErr != nil {
			return fmt.Errorf("invalid server response: %w", jsonErr)
		}

		// Reconcile the local copy. The two directions differ:
		//
		//   --to-org: the skill is now an org asset, delivered to members
		//   through skillsets. A local copy linked to the org skill would be
		//   orphaned — sync only re-pulls org skills that are in a skillset
		//   assigned to the member. So remove the local copy (backed up to
		//   ~/.airskills/undo) and tell the user how to get it back.
		//
		//   --to-user: the skill is now the caller's personal skill. Keep the
		//   local copy and repoint its marker to personal ownership, so
		//   subsequent push/pull hit the new row.
		if transferToOrg != "" {
			undoPath := backupAndRemoveLocalSkill(skillName)
			fmt.Printf("\n  %s Transferred to org %s.\n", green("✓"), transferToOrg)
			if undoPath != "" {
				fmt.Printf("  Removed the local copy — backed up to %s/\n", undoPath)
			}
			fmt.Printf("\n  It's now an org skill, delivered through skillsets. To use it locally\n")
			fmt.Printf("  again, add it to a skillset and assign that skillset to yourself:\n")
			fmt.Printf("    airskills org skillset add-skill <skillset> %s --org %s\n", updated.Name, transferToOrg)
			fmt.Printf("    airskills org member skillsets <you> --add <skillset> --org %s\n", transferToOrg)
			fmt.Printf("    airskills sync\n")
		} else {
			username := ""
			if profile, _ := client.getMe(); profile != nil {
				username = profile.Username
			}
			if username != "" && updated.Id.String() != "" {
				if err := updateLocalMarkerForTransfer(
					skillName,
					skill.Id.String(),
					updated.Id.String(),
					"user",
					username,
					updated.Name,
					updated.Version,
					strDeref(updated.ContentHash),
				); err != nil {
					fmt.Fprintf(os.Stderr, "  %s server transferred OK but local marker update failed: %v\n", yellow("!"), err)
				}
			}
			fmt.Printf("\n  %s Transferred to your personal namespace. Your local copy stays linked.\n", green("✓"))
		}
		telemetry.Capture("cli_transfer", map[string]interface{}{
			"skill_id": skill.Id.String(),
			"to_org":   transferToOrg,
			"to_user":  transferToOrg == "",
		})
		return nil
	},
}

// backupAndRemoveLocalSkill is the local side of a --to-org transfer: the skill
// no longer belongs to the caller personally, so its local copy must go. We
// back it up to ~/.airskills/undo (never delete outright), remove the dir from
// every agent, and drop its marker so it's no longer tracked as a local skill.
// Returns the undo path (empty if the skill wasn't present locally, or backup
// found nothing to copy). If the backup fails we leave everything in place.
func backupAndRemoveLocalSkill(skillName string) string {
	localSkills, _ := scanSkillsFromAgents()
	localDir, present := localSkills[skillName]

	undoPath := ""
	if present {
		ts := time.Now().UTC().Format("20060102T150405Z")
		p, err := backupSkillToUndo(skillName, ts)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  %s local backup failed, leaving local copy in place: %v\n", yellow("!"), err)
			return ""
		}
		undoPath = p
		_ = removeSkillDirAcrossAgents(localDir)
	}

	state := loadSyncState()
	if _, ok := state.Skills[skillName]; ok {
		delete(state.Skills, skillName)
		_ = saveSyncState(state)
	}
	return undoPath
}

// findSkillByName looks up a skill by name across the user's personal skills
// and any org-owned skills they can see.
func findSkillByName(c *apiClient, name string) (*apiSkill, error) {
	for _, scope := range []string{"personal", "org"} {
		// Ownership query: transfer source lookup must search skills the
		// caller can move from their personal namespace or org ownership.
		skills, err := c.listSkills(scope)
		if err != nil {
			return nil, fmt.Errorf("fetching %s skills: %w", scope, err)
		}
		for i := range skills {
			if skills[i].Name == name {
				return &skills[i], nil
			}
		}
	}
	return nil, nil
}

// lookupCallerOrgID returns the org ID for the given slug if the caller is a
// member of that org. Uses /api/v1/organizations (multi-org endpoint) so users
// who belong to multiple orgs can target any of them.
func lookupCallerOrgID(c *apiClient, slug string) (string, error) {
	orgs, err := listCallerOrgs(c)
	if err != nil {
		return "", err
	}
	for _, org := range orgs {
		if org.Slug == slug {
			return org.ID, nil
		}
	}
	return "", fmt.Errorf("you are not a member of %q", slug)
}

func init() {
	transferCmd.Flags().StringVar(&transferToOrg, "to-org", "", "Target org slug (e.g. cherrypick)")
	transferCmd.Flags().Bool("to-user", false, "Transfer to your personal namespace (org → user)")
	transferCmd.Flags().StringVar(&transferSlug, "slug", "", "Override slug in the target namespace (use on collision)")
	transferCmd.Flags().BoolVarP(&transferYes, "yes", "y", false, "Skip confirmation prompt")
	rootCmd.AddCommand(transferCmd)
}
