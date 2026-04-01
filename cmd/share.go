package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

var shareWith string

var shareCmd = &cobra.Command{
	Use:   "share <username/skill>",
	Short: "Share a skill with someone",
	Long:  "Share a skill with another user by email. They'll be notified and can install it with airskills add.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if shareWith == "" {
			return fmt.Errorf("--with is required (email address)")
		}

		parts := strings.SplitN(args[0], "/", 2)
		if len(parts) != 2 {
			return fmt.Errorf("expected format: username/skill-name")
		}
		username, slug := parts[0], parts[1]

		client, err := newAPIClientAuto()
		if err != nil {
			return err
		}

		// Resolve the skill to get its ID
		body, err := client.get(fmt.Sprintf("/api/v1/resolve/%s/%s", username, slug))
		if err != nil {
			return fmt.Errorf("skill not found: %w", err)
		}

		var resolved struct {
			Type string `json:"type"`
			ID   string `json:"id"`
		}
		if err := parseJSON(body, &resolved); err != nil {
			return err
		}

		// Share it
		endpoint := fmt.Sprintf("/api/v1/skills/%s/share", resolved.ID)
		if resolved.Type == "skillset" {
			endpoint = fmt.Sprintf("/api/v1/skillsets/%s/share", resolved.ID)
		}

		_, err = client.post(endpoint, map[string]string{
			"email": shareWith,
		})
		if err != nil {
			return fmt.Errorf("failed to share: %w", err)
		}

		logInfo("Shared %s/%s with %s", username, slug, shareWith)
		return nil
	},
}

func init() {
	shareCmd.Flags().StringVar(&shareWith, "with", "", "Email address to share with")
	rootCmd.AddCommand(shareCmd)
}
