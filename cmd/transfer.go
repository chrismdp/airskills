package cmd

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"

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

A redirect from the old slug is left behind so existing /<old-owner>/<slug>
links keep resolving.`,
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
			to = map[string]string{"kind": "user", "id": profile.ID}
		}

		if !transferYes {
			fmt.Printf("\n  Skill:     %s\n", skill.Name)
			if transferToOrg != "" {
				fmt.Printf("  Move to:   org %s\n", transferToOrg)
			} else {
				fmt.Printf("  Move to:   your personal namespace\n")
			}
			fmt.Printf("\n  Old links keep working via a redirect, but the canonical URL\n")
			fmt.Printf("  changes.\n\n")
			fmt.Print("  Continue? [y/N] ")

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
			fmt.Sprintf("/api/v1/skills/%s/transfer", skill.ID),
			payload,
		)
		if err != nil {
			return fmt.Errorf("transfer failed: %w", err)
		}

		var updated apiSkill
		if jsonErr := json.Unmarshal(respBody, &updated); jsonErr != nil {
			return fmt.Errorf("invalid server response: %w", jsonErr)
		}

		// Update local marker + rename dir on this machine. Other machines
		// learn about the transfer the next time they push (server returns
		// current_owner; CLI re-resolves).
		newSlug := transferToOrg
		newKind := "org"
		if newSlug == "" {
			profile, _ := client.getMe()
			if profile != nil {
				newSlug = profile.Username
			}
			newKind = "user"
		}
		if newSlug != "" {
			if err := updateLocalMarkerForTransfer(skill.ID, newKind, newSlug); err != nil {
				fmt.Fprintf(os.Stderr, "  %s server transferred OK but local marker update failed: %v\n", yellow("!"), err)
			}
		}

		fmt.Printf("\n  %s Transferred.\n", green("✓"))
		telemetry.Capture("cli_transfer", map[string]interface{}{
			"skill_id": skill.ID,
			"to_org":   transferToOrg,
			"to_user":  transferToOrg == "",
		})
		return nil
	},
}

// findSkillByName looks up a skill by name across the user's personal skills
// and any org-owned skills they can see.
func findSkillByName(c *apiClient, name string) (*apiSkill, error) {
	for _, scope := range []string{"personal", "org"} {
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

// lookupCallerOrgID returns the caller's current org ID if its slug matches
// the requested one. Today users belong to a single org via profiles.org_id;
// when memberships land, this should consult that table instead.
func lookupCallerOrgID(c *apiClient, slug string) (string, error) {
	body, err := c.get("/api/v1/org")
	if err != nil {
		return "", fmt.Errorf("looking up your org: %w", err)
	}
	var resp struct {
		Org *struct {
			ID   string `json:"id"`
			Slug string `json:"slug"`
		} `json:"org"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", fmt.Errorf("invalid org response: %w", err)
	}
	if resp.Org == nil {
		return "", fmt.Errorf("you are not a member of any org")
	}
	if resp.Org.Slug != slug {
		return "", fmt.Errorf("you are not a member of %q (your org is %q)", slug, resp.Org.Slug)
	}
	return resp.Org.ID, nil
}

func init() {
	transferCmd.Flags().StringVar(&transferToOrg, "to-org", "", "Target org slug (e.g. cherrypick)")
	transferCmd.Flags().Bool("to-user", false, "Transfer to your personal namespace (org → user)")
	transferCmd.Flags().StringVar(&transferSlug, "slug", "", "Override slug in the target namespace (use on collision)")
	transferCmd.Flags().BoolVarP(&transferYes, "yes", "y", false, "Skip confirmation prompt")
	rootCmd.AddCommand(transferCmd)
}
