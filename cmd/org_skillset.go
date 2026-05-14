package cmd

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/chrismdp/airskills/internal/apitypes"
	"github.com/spf13/cobra"
)

type apiOrgSkillset = apitypes.SkillsetListItem

var (
	orgSkillsetOrgFlag     string
	orgSkillsetCreateName  string
	orgSkillsetDescription string
	orgSkillsetDeleteForce bool
)

var orgSkillsetCmd = &cobra.Command{
	Use:   "skillset",
	Short: "Manage org skillsets",
	Long: `Manage skillsets owned by an organization.

If you belong to exactly one org, --org can be omitted. If you belong to
multiple orgs, pass --org <slug> to choose the target.`,
}

var orgSkillsetListCmd = &cobra.Command{
	Use:   "list",
	Short: "List org skillsets",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := newAPIClientAuto()
		if err != nil {
			return err
		}
		orgSlug, err := resolveOrgSlug(client, orgSkillsetOrgFlag)
		if err != nil {
			return err
		}
		skillsets, err := client.listOrgSkillsets(orgSlug)
		if err != nil {
			return err
		}
		renderOrgSkillsetList(os.Stdout, skillsets)
		return nil
	},
}

var orgSkillsetCreateCmd = &cobra.Command{
	Use:   "create <slug>",
	Short: "Create an org skillset",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		slug := args[0]
		if err := validSkillsetSlug(slug); err != nil {
			return err
		}
		client, err := newAPIClientAuto()
		if err != nil {
			return err
		}
		orgSlug, err := resolveOrgSlug(client, orgSkillsetOrgFlag)
		if err != nil {
			return err
		}
		name := orgSkillsetCreateName
		if name == "" {
			name = slug
		}
		ss, err := client.createOrgSkillset(orgSlug, slug, name, orgSkillsetDescription)
		if err != nil {
			return err
		}
		fmt.Printf("Created org skillset %q in %s (id %s)\n", ss.Slug, orgSlug, ss.Id)
		printAgentNextSteps(os.Stdout, []agentNextStep{
			{Cmd: fmt.Sprintf("airskills org skillset add-skill %s <skill> --org %s", ss.Slug, orgSlug), Why: "populate it"},
			{Cmd: fmt.Sprintf("airskills org member skillsets <user> --add %s --org %s", ss.Slug, orgSlug), Why: "assign it to a member"},
		})
		return nil
	},
}

var orgSkillsetDeleteCmd = &cobra.Command{
	Use:   "delete <slug>",
	Short: "Delete an org skillset",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		slug := args[0]
		if !orgSkillsetDeleteForce {
			fmt.Printf("Delete org skillset %q? This cannot be undone. [y/N] ", slug)
			reader := bufio.NewReader(os.Stdin)
			line, _ := reader.ReadString('\n')
			answer := strings.ToLower(strings.TrimSpace(line))
			if answer != "y" && answer != "yes" {
				return errors.New("cancelled")
			}
		}
		client, err := newAPIClientAuto()
		if err != nil {
			return err
		}
		orgSlug, err := resolveOrgSlug(client, orgSkillsetOrgFlag)
		if err != nil {
			return err
		}
		if err := client.deleteOrgSkillset(orgSlug, slug); err != nil {
			return err
		}
		fmt.Printf("Deleted org skillset %q from %s\n", slug, orgSlug)
		return nil
	},
}

var orgSkillsetAddSkillCmd = &cobra.Command{
	Use:   "add-skill <skillset> <skill>",
	Short: "Add an org-owned skill to an org skillset",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		skillsetSlug := args[0]
		if err := validSkillsetSlug(skillsetSlug); err != nil {
			return err
		}
		client, err := newAPIClientAuto()
		if err != nil {
			return err
		}
		orgSlug, err := resolveOrgSlug(client, orgSkillsetOrgFlag)
		if err != nil {
			return err
		}
		skillOwner, skillSlug := normalizeOrgSkillRef(orgSlug, args[1])
		if skillOwner != orgSlug {
			return fmt.Errorf("org skillsets can only contain skills owned by %s; transfer it first with `airskills transfer %s --to-org %s`", orgSlug, args[1], orgSlug)
		}
		if err := client.addSkillToOrgSkillset(orgSlug, skillsetSlug, skillOwner, skillSlug); err != nil {
			return err
		}
		fmt.Printf("Added %s/%s to org skillset %q\n", skillOwner, skillSlug, skillsetSlug)
		return nil
	},
}

var orgSkillsetRemoveSkillCmd = &cobra.Command{
	Use:     "rm-skill <skillset> <skill>",
	Aliases: []string{"remove-skill"},
	Short:   "Remove a skill from an org skillset",
	Args:    cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		skillsetSlug := args[0]
		if err := validSkillsetSlug(skillsetSlug); err != nil {
			return err
		}
		client, err := newAPIClientAuto()
		if err != nil {
			return err
		}
		orgSlug, err := resolveOrgSlug(client, orgSkillsetOrgFlag)
		if err != nil {
			return err
		}
		skillOwner, skillSlug := normalizeOrgSkillRef(orgSlug, args[1])
		if skillOwner != orgSlug {
			return fmt.Errorf("org skillsets can only contain skills owned by %s", orgSlug)
		}
		if err := client.removeSkillFromOrgSkillset(orgSlug, skillsetSlug, skillOwner, skillSlug); err != nil {
			return err
		}
		fmt.Printf("Removed %s/%s from org skillset %q\n", skillOwner, skillSlug, skillsetSlug)
		return nil
	},
}

func normalizeOrgSkillRef(orgSlug, input string) (string, string) {
	if owner, slug, ok := strings.Cut(input, "/"); ok {
		return owner, slug
	}
	prefix := orgSlug + "-"
	if strings.HasPrefix(input, prefix) {
		return orgSlug, strings.TrimPrefix(input, prefix)
	}
	return orgSlug, input
}

func resolveOrgSlug(c *apiClient, flag string) (string, error) {
	if flag != "" {
		return flag, nil
	}
	orgs, err := listCallerOrgs(c)
	if err != nil {
		return "", err
	}
	if len(orgs) == 0 {
		return "", errors.New("you do not belong to any organizations")
	}
	if len(orgs) > 1 {
		slugs := make([]string, 0, len(orgs))
		for _, org := range orgs {
			slugs = append(slugs, org.Slug)
		}
		sort.Strings(slugs)
		return "", fmt.Errorf("multiple organizations found (%s); pass --org <slug>", strings.Join(slugs, ", "))
	}
	return orgs[0].Slug, nil
}

func renderOrgSkillsetList(w io.Writer, skillsets []apiOrgSkillset) {
	if len(skillsets) == 0 {
		fmt.Fprintln(w, "(no skillsets in org)")
		return
	}
	sorted := make([]apiOrgSkillset, len(skillsets))
	copy(sorted, skillsets)
	sort.SliceStable(sorted, func(i, j int) bool {
		return sorted[i].Slug < sorted[j].Slug
	})
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "SLUG\tSKILLS\tNAME")
	for _, s := range sorted {
		name := s.Name
		if s.IsDefault {
			name += " (default)"
		}
		fmt.Fprintf(tw, "%s\t%d\t%s\n", s.Slug, s.SkillCount, name)
	}
	tw.Flush()
}

func init() {
	for _, cmd := range []*cobra.Command{
		orgSkillsetListCmd,
		orgSkillsetCreateCmd,
		orgSkillsetDeleteCmd,
		orgSkillsetAddSkillCmd,
		orgSkillsetRemoveSkillCmd,
	} {
		cmd.Flags().StringVar(&orgSkillsetOrgFlag, "org", "", "Org slug to target (required if you belong to multiple orgs)")
	}
	orgSkillsetCreateCmd.Flags().StringVar(&orgSkillsetCreateName, "name", "", "Human-readable name (defaults to slug)")
	orgSkillsetCreateCmd.Flags().StringVar(&orgSkillsetDescription, "description", "", "Optional one-line description")
	orgSkillsetDeleteCmd.Flags().BoolVar(&orgSkillsetDeleteForce, "force", false, "Skip the confirmation prompt")

	orgSkillsetCmd.AddCommand(orgSkillsetListCmd)
	orgSkillsetCmd.AddCommand(orgSkillsetCreateCmd)
	orgSkillsetCmd.AddCommand(orgSkillsetDeleteCmd)
	orgSkillsetCmd.AddCommand(orgSkillsetAddSkillCmd)
	orgSkillsetCmd.AddCommand(orgSkillsetRemoveSkillCmd)
}
