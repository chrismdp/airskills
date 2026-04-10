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

var publishCmd = &cobra.Command{
	Use:   "publish <skill-name>",
	Short: "Make a skill publicly visible on the internet",
	Long:  "Publishes a private skill so anyone can install it with airskills add.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		skillName := args[0]

		client, err := newAPIClientAuto()
		if err != nil {
			return err
		}

		// Find the skill by name
		skills, err := client.listSkills("personal")
		if err != nil {
			return fmt.Errorf("fetching skills: %w", err)
		}

		var skill *apiSkill
		for i := range skills {
			if skills[i].Name == skillName {
				skill = &skills[i]
				break
			}
		}
		if skill == nil {
			return fmt.Errorf("skill %q not found in your account", skillName)
		}

		if skill.Visibility == "public" {
			fmt.Printf("%s is already public.\n", skillName)
			return nil
		}

		// Show what's about to happen and ask for confirmation
		fmt.Printf("\n  Skill:  %s\n", skill.Name)
		if skill.Description != "" {
			fmt.Printf("  About:  %s\n", skill.Description)
		}
		fmt.Println()
		fmt.Printf("  %s This will make %q publicly visible on the internet.\n", yellow("⚠"), skillName)
		fmt.Printf("  Anyone will be able to find and install it.\n\n")
		fmt.Print("  Are you sure? [y/N] ")

		reader := bufio.NewReader(os.Stdin)
		answer, _ := reader.ReadString('\n')
		if strings.TrimSpace(strings.ToLower(answer)) != "y" {
			fmt.Println("  Aborted.")
			return nil
		}

		// Make it public
		body, statusCode, err := client.put(
			fmt.Sprintf("/api/v1/skills/%s", skill.ID),
			map[string]interface{}{
				"visibility":     "public",
				"confirm_public": true,
			},
		)
		if err != nil {
			return fmt.Errorf("failed to publish: %w", err)
		}
		if statusCode >= 400 {
			return fmt.Errorf("API error (%d): %s", statusCode, string(body))
		}

		// Check for private dependencies warning
		var resp struct {
			PrivateDependencies []struct {
				ID   string `json:"id"`
				Slug string `json:"slug"`
				Name string `json:"name"`
			} `json:"private_dependencies"`
		}
		json.Unmarshal(body, &resp)

		// Get the user's profile for the public URL
		profile, _ := client.getMe()

		fmt.Printf("\n  %s %s is now public.\n", green("✓"), skillName)
		if profile != nil {
			fmt.Printf("  Install: airskills add %s/%s\n", profile.Username, skillName)
		}

		if len(resp.PrivateDependencies) > 0 {
			fmt.Printf("\n  %s This skill depends on private skills:\n", yellow("!"))
			for _, dep := range resp.PrivateDependencies {
				fmt.Printf("    • %s\n", dep.Name)
			}
			fmt.Println("  Users who install it won't be able to resolve these dependencies.")
			fmt.Println("  Run airskills publish <dep-name> for each, or re-publish with --include-deps.")
		}

		telemetry.Capture("cli_publish", map[string]interface{}{
			"skill":    skillName,
			"skill_id": skill.ID,
		})

		return nil
	},
}

func init() {
	rootCmd.AddCommand(publishCmd)
}
