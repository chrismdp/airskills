package cmd

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

// --- org parent ---

var orgCmd = &cobra.Command{
	Use:   "org",
	Short: "Organisation-scoped commands (members, skillsets)",
	Long: `Commands that operate on orgs you're a member of.

Example:
  airskills org member skillsets alice            # list alice's skillsets
  airskills org member skillsets alice --set a,b  # replace alice's set
  airskills org member skillsets alice --add foo  # add one
  airskills org member skillsets alice --remove foo
`,
}

var orgMemberCmd = &cobra.Command{
	Use:   "member",
	Short: "Member-scoped org commands",
}

// --- airskills org member skillsets <user> ---

var (
	orgMemberSkillsetsSet    string
	orgMemberSkillsetsAdd    string
	orgMemberSkillsetsRemove string
	orgMemberOrgFlag         string
)

var orgMemberSkillsetsCmd = &cobra.Command{
	Use:   "skillsets <user>",
	Short: "List / set / add / remove skillsets assigned to an org member",
	Long: `Inspect or change which org skillsets are assigned to a given member.

Bare invocation lists the current assignments. --set replaces the full set
(empty means 'just the default'). --add and --remove are incremental.

--set / --add / --remove are mutually exclusive.

<user> is a username (the org-members API only exposes usernames today;
email lookup is not supported yet).

The default skillset is always implicitly assigned and cannot be removed.

If you belong to multiple orgs, pass --org <slug> to disambiguate.
`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		userArg := args[0]

		mode := 0
		if orgMemberSkillsetsSet != "" || cmd.Flags().Changed("set") {
			mode++
		}
		if orgMemberSkillsetsAdd != "" {
			mode++
		}
		if orgMemberSkillsetsRemove != "" {
			mode++
		}
		if mode > 1 {
			return fmt.Errorf("--set, --add, --remove are mutually exclusive")
		}

		if strings.EqualFold(orgMemberSkillsetsRemove, "default") {
			return fmt.Errorf("cannot remove 'default' — the default skillset is always implicitly assigned")
		}

		client, err := newAPIClientAuto()
		if err != nil {
			return err
		}

		// Resolve <user> → user_id via the org members list.
		userID, err := resolveOrgMemberID(client, orgMemberOrgFlag, userArg)
		if err != nil {
			return err
		}

		path := fmt.Sprintf(
			"/api/v1/organization/members/%s/skillsets",
			url.PathEscape(userID),
		)
		if orgMemberOrgFlag != "" {
			path += "?org=" + url.QueryEscape(orgMemberOrgFlag)
		}

		// List mode.
		if mode == 0 {
			skillsets, err := getMemberSkillsets(client, path)
			if err != nil {
				return err
			}
			renderAssignedSkillsets(os.Stdout, skillsets)
			return nil
		}

		// Mutation: compute target id set.
		var targetIDs []string

		if cmd.Flags().Changed("set") {
			// --set is the full authoritative list. Empty means "just default".
			targetIDs = []string{}
			if orgMemberSkillsetsSet != "" {
				slugs := splitCSV(orgMemberSkillsetsSet)
				skillsets, err := getMemberSkillsets(client, path)
				if err != nil {
					return err
				}
				ids, err := slugsToIDs(skillsets, slugs)
				if err != nil {
					return err
				}
				targetIDs = ids
			}
		} else {
			// --add / --remove: read, mutate, write.
			skillsets, err := getMemberSkillsets(client, path)
			if err != nil {
				return err
			}
			currentIDs := make([]string, 0, len(skillsets))
			for _, s := range skillsets {
				if s.IsAssigned && !s.IsDefault {
					currentIDs = append(currentIDs, s.ID)
				}
			}

			if orgMemberSkillsetsAdd != "" {
				ids, err := slugsToIDs(skillsets, []string{orgMemberSkillsetsAdd})
				if err != nil {
					return err
				}
				targetIDs = appendUnique(currentIDs, ids[0])
			}
			if orgMemberSkillsetsRemove != "" {
				ids, err := slugsToIDs(skillsets, []string{orgMemberSkillsetsRemove})
				if err != nil {
					return err
				}
				targetIDs = removeID(currentIDs, ids[0])
			}
		}

		out, err := putMemberSkillsets(client, path, targetIDs)
		if err != nil {
			return err
		}
		renderAssignedSkillsets(os.Stdout, out)
		return nil
	},
}

// --- helpers ---

type apiMemberSkillset struct {
	ID         string `json:"id"`
	Slug       string `json:"slug"`
	Name       string `json:"name"`
	IsDefault  bool   `json:"is_default"`
	IsAssigned bool   `json:"is_assigned"`
}

type memberSkillsetsResponse struct {
	Skillsets []apiMemberSkillset `json:"skillsets"`
}

func getMemberSkillsets(c *apiClient, path string) ([]apiMemberSkillset, error) {
	body, err := c.get(path)
	if err != nil {
		return nil, err
	}
	var resp memberSkillsetsResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("parsing member skillsets: %w", err)
	}
	return resp.Skillsets, nil
}

func putMemberSkillsets(c *apiClient, path string, ids []string) ([]apiMemberSkillset, error) {
	payload := map[string][]string{"skillset_ids": ids}
	body, _, err := c.put(path, payload)
	if err != nil {
		return nil, err
	}
	var resp memberSkillsetsResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("parsing PUT response: %w", err)
	}
	return resp.Skillsets, nil
}

// resolveOrgMemberID looks up a username in the org's members list and
// returns the matching user id. `orgSlug` is optional — when empty, the
// server's default-membership logic is used (GET /api/v1/organization).
func resolveOrgMemberID(c *apiClient, orgSlug, username string) (string, error) {
	path := "/api/v1/organization/members"
	if orgSlug != "" {
		path += "?slug=" + url.QueryEscape(orgSlug)
	}
	body, err := c.get(path)
	if err != nil {
		return "", fmt.Errorf("fetching org members: %w", err)
	}
	var resp struct {
		Members []struct {
			ID          string `json:"id"`
			Username    string `json:"username"`
			DisplayName string `json:"display_name"`
		} `json:"members"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", fmt.Errorf("invalid members response: %w", err)
	}
	// Strip a leading @ so both `alice` and `@alice` work.
	needle := strings.TrimPrefix(username, "@")
	for _, m := range resp.Members {
		if strings.EqualFold(m.Username, needle) {
			return m.ID, nil
		}
	}
	return "", fmt.Errorf("user %q is not a member of the target org", username)
}

func renderAssignedSkillsets(w *os.File, skillsets []apiMemberSkillset) {
	if len(skillsets) == 0 {
		fmt.Fprintln(w, "(no skillsets in org)")
		return
	}
	assigned := false
	for _, s := range skillsets {
		if !s.IsAssigned {
			continue
		}
		assigned = true
		suffix := ""
		if s.IsDefault {
			suffix = " (default)"
		}
		fmt.Fprintf(w, "%s%s\n", s.Slug, suffix)
	}
	if !assigned {
		fmt.Fprintln(w, "(no assignments — default is implicit)")
	}
}

func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// slugsToIDs maps slugs to their UUIDs using the caller-visible member
// skillsets response (which lists every skillset in the org). Returns
// an error on the first unknown slug.
func slugsToIDs(skillsets []apiMemberSkillset, slugs []string) ([]string, error) {
	bySlug := make(map[string]string, len(skillsets))
	for _, s := range skillsets {
		bySlug[s.Slug] = s.ID
	}
	ids := make([]string, 0, len(slugs))
	for _, slug := range slugs {
		id, ok := bySlug[slug]
		if !ok {
			return nil, fmt.Errorf("unknown skillset %q — it must be owned by the target org", slug)
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func appendUnique(ids []string, id string) []string {
	for _, existing := range ids {
		if existing == id {
			return ids
		}
	}
	return append(ids, id)
}

func removeID(ids []string, id string) []string {
	out := make([]string, 0, len(ids))
	for _, existing := range ids {
		if existing != id {
			out = append(out, existing)
		}
	}
	return out
}

// --- wiring ---

func init() {
	orgMemberSkillsetsCmd.Flags().StringVar(&orgMemberSkillsetsSet, "set", "", "Replace the member's set with a comma-separated list of skillset slugs (empty = just the default)")
	orgMemberSkillsetsCmd.Flags().StringVar(&orgMemberSkillsetsAdd, "add", "", "Add a single skillset slug to the member's set")
	orgMemberSkillsetsCmd.Flags().StringVar(&orgMemberSkillsetsRemove, "remove", "", "Remove a single skillset slug from the member's set")
	orgMemberSkillsetsCmd.Flags().StringVar(&orgMemberOrgFlag, "org", "", "Org slug to target (required if you belong to multiple orgs)")

	orgMemberCmd.AddCommand(orgMemberSkillsetsCmd)
	orgCmd.AddCommand(orgMemberCmd)
	rootCmd.AddCommand(orgCmd)
}
