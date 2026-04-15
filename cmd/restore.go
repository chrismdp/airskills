package cmd

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
)

var restoreNewSlug string

var restoreCmd = &cobra.Command{
	Use:   "restore <skill-name>",
	Short: "Restore a soft-deleted skill",
	Long: `Restores a skill that was deleted with 'airskills rm'.

If the original slug now conflicts with another skill in your namespace,
pass --slug <new-slug> to rename on restore.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		skillName := args[0]

		client, err := newAPIClientAuto()
		if err != nil {
			return err
		}

		// Find the skill in the deleted list
		skills, err := client.listDeletedSkills()
		if err != nil {
			return fmt.Errorf("listing deleted skills: %w", err)
		}

		var target *apiSkill
		for i := range skills {
			if skills[i].Name == skillName || skills[i].Slug == skillName {
				target = &skills[i]
				break
			}
		}
		if target == nil {
			return fmt.Errorf("no deleted skill named %q found", skillName)
		}

		payload := map[string]string{}
		if restoreNewSlug != "" {
			payload["slug"] = restoreNewSlug
		}

		body, err := client.post(fmt.Sprintf("/api/v1/skills/%s/restore", target.ID), payload)
		if err != nil {
			return fmt.Errorf("restoring skill: %w", err)
		}

		var restored apiSkill
		if err := json.Unmarshal(body, &restored); err != nil {
			return fmt.Errorf("parsing response: %w", err)
		}

		fmt.Printf("  %s restored %s (v%s)\n", green("✓"), restored.Name, restored.Version)
		return nil
	},
}

func init() {
	restoreCmd.Flags().StringVar(&restoreNewSlug, "slug", "", "New slug to use if the original is taken")
	rootCmd.AddCommand(restoreCmd)
}
