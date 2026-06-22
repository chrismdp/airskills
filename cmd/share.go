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
		username, slug, err := parseShareRef(args[0], shareWith)
		if err != nil {
			return err
		}

		client, err := newAPIClientAuto()
		if err != nil {
			return err
		}

		return shareSkill(client, username, slug, shareWith)
	},
}

// parseShareRef validates the share inputs and splits the username/skill
// argument. Kept separate from the network path so the input contract is
// unit-testable without an API client. Empty owner or slug (e.g. "alice/" or
// "/foo") is rejected here rather than passed on to produce a confusing
// "skill not found" from a malformed resolve URL.
func parseShareRef(ref, email string) (username, slug string, err error) {
	if email == "" {
		return "", "", fmt.Errorf("--with is required (email address)")
	}

	parts := strings.SplitN(ref, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("expected format: username/skill-name")
	}
	return parts[0], parts[1], nil
}

// shareSkill resolves username/slug to an id, then POSTs the share to the
// skill or bundle endpoint depending on the resolved type.
func shareSkill(client *apiClient, username, slug, email string) error {
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

	// Share it — bundles live on a separate endpoint
	endpoint := fmt.Sprintf("/api/v1/skills/%s/share", resolved.ID)
	if resolved.Type == "bundle" {
		endpoint = fmt.Sprintf("/api/v1/bundles/%s/share", resolved.ID)
	}

	_, err = client.post(endpoint, map[string]string{
		"email": email,
	})
	if err != nil {
		return fmt.Errorf("failed to share: %w", err)
	}

	logInfo("Shared %s/%s with %s", username, slug, email)
	return nil
}

func init() {
	shareCmd.Flags().StringVar(&shareWith, "with", "", "Email address to share with")
	rootCmd.AddCommand(shareCmd)
}
