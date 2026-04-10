package cmd

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

// apiBundle represents a bundle from the API.
type apiBundle struct {
	ID           string           `json:"id"`
	Name         string           `json:"name"`
	Slug         string           `json:"slug"`
	Description  string           `json:"description"`
	Visibility   string           `json:"visibility"`
	BundleSkills []apiBundleSkill `json:"bundle_skills"`
}

// apiBundleSkill represents a skill entry within a bundle.
type apiBundleSkill struct {
	SkillID string       `json:"skill_id"`
	Skill   apiBundleRef `json:"skills"`
}

// apiBundleRef is the nested skill info returned inside bundle_skills.
type apiBundleRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Slug string `json:"slug"`
}

// listBundles fetches all bundles for the authenticated user.
func (c *apiClient) listBundles() ([]apiBundle, error) {
	body, err := c.get("/api/v1/bundles")
	if err != nil {
		return nil, err
	}
	var resp struct {
		Bundles []apiBundle `json:"bundles"`
	}
	if err := parseJSON(body, &resp); err != nil {
		return nil, err
	}
	return resp.Bundles, nil
}

// getBundle fetches a single bundle by ID, including its skills.
func (c *apiClient) getBundle(id string) (*apiBundle, error) {
	body, err := c.get(fmt.Sprintf("/api/v1/bundles/%s", id))
	if err != nil {
		return nil, err
	}
	var bundle apiBundle
	if err := parseJSON(body, &bundle); err != nil {
		return nil, err
	}
	return &bundle, nil
}

// findBundleByName finds a bundle by name or slug from the user's bundles.
func (c *apiClient) findBundleByName(name string) (*apiBundle, error) {
	bundles, err := c.listBundles()
	if err != nil {
		return nil, err
	}

	lower := strings.ToLower(name)
	for _, b := range bundles {
		if strings.ToLower(b.Name) == lower || strings.ToLower(b.Slug) == lower {
			return &b, nil
		}
	}
	return nil, fmt.Errorf("bundle %q not found", name)
}

var bundleCmd = &cobra.Command{
	Use:   "bundle",
	Short: "Manage skill bundles",
	Long:  "Create, list, and manage bundles of skills.",
}

var bundleListCmd = &cobra.Command{
	Use:   "list",
	Short: "List your bundles",
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := newAPIClientAuto()
		if err != nil {
			return err
		}

		bundles, err := client.listBundles()
		if err != nil {
			return fmt.Errorf("fetching bundles: %w", err)
		}

		if len(bundles) == 0 {
			fmt.Println("No bundles found. Create one with 'airskills bundle create <name>'.")
			return nil
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "NAME\tVISIBILITY\tSKILLS")
		for _, b := range bundles {
			fmt.Fprintf(w, "%s\t%s\t%d\n", b.Name, b.Visibility, len(b.BundleSkills))
		}
		w.Flush()
		return nil
	},
}

var bundleCreateDescription string
var bundleCreateVisibility string

var bundleCreateCmd = &cobra.Command{
	Use:   "create <name>",
	Short: "Create a new bundle",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]

		client, err := newAPIClientAuto()
		if err != nil {
			return err
		}

		payload := map[string]string{
			"name": name,
		}
		if bundleCreateDescription != "" {
			payload["description"] = bundleCreateDescription
		}
		if bundleCreateVisibility != "" {
			payload["visibility"] = bundleCreateVisibility
		}

		body, err := client.post("/api/v1/bundles", payload)
		if err != nil {
			return fmt.Errorf("creating bundle: %w", err)
		}

		var bundle apiBundle
		if err := parseJSON(body, &bundle); err != nil {
			return err
		}

		logInfo("Created bundle %q (%s)", bundle.Name, bundle.Visibility)
		return nil
	},
}

var bundleShowCmd = &cobra.Command{
	Use:   "show <name>",
	Short: "Show bundle details and skills",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := newAPIClientAuto()
		if err != nil {
			return err
		}

		match, err := client.findBundleByName(args[0])
		if err != nil {
			return err
		}

		// Fetch full details by ID
		bundle, err := client.getBundle(match.ID)
		if err != nil {
			return fmt.Errorf("fetching bundle: %w", err)
		}

		fmt.Printf("Name:        %s\n", bundle.Name)
		fmt.Printf("Slug:        %s\n", bundle.Slug)
		fmt.Printf("Visibility:  %s\n", bundle.Visibility)
		if bundle.Description != "" {
			fmt.Printf("Description: %s\n", bundle.Description)
		}
		fmt.Printf("Skills:      %d\n", len(bundle.BundleSkills))

		if len(bundle.BundleSkills) > 0 {
			fmt.Println()
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "SKILL\tID")
			for _, bs := range bundle.BundleSkills {
				fmt.Fprintf(w, "%s\t%s\n", bs.Skill.Name, bs.SkillID)
			}
			w.Flush()
		}

		return nil
	},
}

var bundleDeleteCmd = &cobra.Command{
	Use:   "delete <name>",
	Short: "Delete a bundle",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := newAPIClientAuto()
		if err != nil {
			return err
		}

		match, err := client.findBundleByName(args[0])
		if err != nil {
			return err
		}

		// Confirm deletion
		fmt.Printf("Delete bundle %q? This cannot be undone. [y/N] ", match.Name)
		reader := bufio.NewReader(os.Stdin)
		answer, _ := reader.ReadString('\n')
		answer = strings.TrimSpace(strings.ToLower(answer))
		if answer != "y" && answer != "yes" {
			fmt.Println("Cancelled.")
			return nil
		}

		if err := client.del(fmt.Sprintf("/api/v1/bundles/%s", match.ID)); err != nil {
			return fmt.Errorf("deleting bundle: %w", err)
		}

		logInfo("Deleted bundle %q", match.Name)
		return nil
	},
}

func init() {
	bundleCreateCmd.Flags().StringVar(&bundleCreateDescription, "description", "", "Bundle description")
	bundleCreateCmd.Flags().StringVar(&bundleCreateVisibility, "visibility", "", "Visibility: private or public")

	bundleCmd.AddCommand(bundleListCmd)
	bundleCmd.AddCommand(bundleCreateCmd)
	bundleCmd.AddCommand(bundleShowCmd)
	bundleCmd.AddCommand(bundleDeleteCmd)
	rootCmd.AddCommand(bundleCmd)
}
