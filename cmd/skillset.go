package cmd

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/chrismdp/airskills/config"
	"github.com/spf13/cobra"
)

// skillsetFlag holds the value of --skillset on sync/push/pull. Empty means
// "not provided on this invocation".
var skillsetFlag string

// ErrSkillsetSwitchCancelled signals a y/N prompt answered negatively —
// callers exit non-zero without updating the remembered default.
var ErrSkillsetSwitchCancelled = errors.New("cancelled — default skillset unchanged")

// resolveSkillsetFlag decides what skillset slug to send on the upcoming
// API call given the CLI flag value and the remembered default in cfg.
//
// Rules (mirrors doc/changes/cli-skillset-flag.md):
//
//   - flag empty, nothing remembered → return empty (server picks default,
//     caller should remember the resolved slug on successful response).
//   - flag empty, something remembered → return the remembered slug.
//   - flag matches remembered → return the flag, no prompt.
//   - flag differs from remembered → prompt via reader; y stores and returns
//     the new slug, anything else returns ErrSkillsetSwitchCancelled.
//   - flag but nothing remembered → return and remember the flag silently.
//
// Persistence is via cfg.Save(); callers pass the *config.Config they loaded.
func resolveSkillsetFlag(cfg *config.Config, flag string, reader io.Reader, writer io.Writer) (string, error) {
	remembered := cfg.Skillset

	if flag == "" {
		return remembered, nil
	}

	if remembered == "" {
		cfg.Skillset = flag
		if err := cfg.Save(); err != nil {
			return "", fmt.Errorf("save skillset preference: %w", err)
		}
		return flag, nil
	}

	if flag == remembered {
		return flag, nil
	}

	fmt.Fprintf(writer, "Switch default skillset from %q to %q? [y/N] ", remembered, flag)
	bufReader := bufio.NewReader(reader)
	line, err := bufReader.ReadString('\n')
	if err != nil && line == "" {
		// EOF / read error with nothing typed — treat as no.
		return "", ErrSkillsetSwitchCancelled
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	if answer != "y" && answer != "yes" {
		return "", ErrSkillsetSwitchCancelled
	}
	cfg.Skillset = flag
	if err := cfg.Save(); err != nil {
		return "", fmt.Errorf("save skillset preference: %w", err)
	}
	return flag, nil
}

// rememberSkillsetAfterSuccess persists the server-resolved skillset slug
// as the user's default the first time they sync without a flag — so the
// next run has something to switch away from.
func rememberSkillsetAfterSuccess(cfg *config.Config, resolvedSlug string) {
	if cfg.Skillset != "" || resolvedSlug == "" {
		return
	}
	cfg.Skillset = resolvedSlug
	_ = cfg.Save() // best-effort; don't fail the sync on a config write hiccup
}

// stdinReader and stderrWriter exist so the top-level commands can wire
// real file handles in while tests substitute in-memory ones.
func stdinReader() io.Reader  { return os.Stdin }
func stderrWriter() io.Writer { return os.Stderr }

// skillsetSlugPattern matches the server's validation in
// app/api/v1/schemas.ts (lowercase alphanumerics + hyphens, 1-64 chars,
// no leading/trailing/consecutive hyphens). The CLI pre-validates so
// users get a fast, local error message instead of a server 400 on
// bad input.
func validSkillsetSlug(slug string) error {
	if slug == "" {
		return errors.New("slug is required")
	}
	if len(slug) > 64 {
		return errors.New("slug must be 64 characters or fewer")
	}
	if slug[0] == '-' || slug[len(slug)-1] == '-' {
		return errors.New("slug cannot start or end with '-'")
	}
	if strings.Contains(slug, "--") {
		return errors.New("slug cannot contain consecutive '-'")
	}
	for _, r := range slug {
		if !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9') && r != '-' {
			return fmt.Errorf("slug contains invalid character %q (only a-z, 0-9, '-')", r)
		}
	}
	return nil
}

var skillsetParentCmd = &cobra.Command{
	Use:   "skillset",
	Short: "Manage personal skillsets (list / create / delete / use)",
	Long: `Personal skillsets group skills for selective sync.

Every account has a 'default' skillset auto-created on signup. Use
these commands to add more, switch between them (--skillset on sync
remembers the last-used one), and delete the ones you no longer want.`,
}

var skillsetListCmd = &cobra.Command{
	Use:   "list",
	Short: "List personal skillsets (the currently-selected one is marked with *)",
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := newAPIClientAuto()
		if err != nil {
			return err
		}
		skillsets, err := client.listSkillsets()
		if err != nil {
			return err
		}
		cfg, _ := config.Load()
		selected := ""
		if cfg != nil {
			selected = cfg.Skillset
		}
		renderSkillsetList(os.Stdout, skillsets, selected)
		return nil
	},
}

// renderSkillsetList prints one skillset per line, with the
// currently-selected slug marked by a leading '*'. Falls back to
// marking is_default when no slug is remembered locally, so the
// output is never asterisk-free for a user who has never switched.
func renderSkillsetList(w io.Writer, skillsets []apiSkillset, selected string) {
	if len(skillsets) == 0 {
		fmt.Fprintln(w, "No skillsets.")
		return
	}
	for _, s := range skillsets {
		marker := " "
		match := selected != "" && s.Slug == selected
		if selected == "" && s.IsDefault {
			match = true
		}
		if match {
			marker = "*"
		}
		fmt.Fprintf(w, "%s %s (%d skills)\n", marker, s.Slug, s.SkillCount)
	}
}

var (
	skillsetCreateName        string
	skillsetCreateDescription string
)

var skillsetCreateCmd = &cobra.Command{
	Use:   "create <slug>",
	Short: "Create a new personal skillset",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		slug := args[0]
		if err := validSkillsetSlug(slug); err != nil {
			return err
		}
		name := skillsetCreateName
		if name == "" {
			name = slug
		}
		client, err := newAPIClientAuto()
		if err != nil {
			return err
		}
		ss, err := client.createSkillset(slug, name, skillsetCreateDescription)
		if err != nil {
			return err
		}
		fmt.Printf("Created skillset %q (id %s)\n", ss.Slug, ss.ID)
		printAgentNextSteps(os.Stdout, []agentNextStep{
			{Cmd: "airskills skillset use " + ss.Slug, Why: "make this the default for future sync/push/pull"},
			{Cmd: "airskills skillset list", Why: "confirm the new skillset shows up"},
		})
		return nil
	},
}

var skillsetDeleteForce bool

var skillsetDeleteCmd = &cobra.Command{
	Use:   "delete <slug>",
	Short: "Delete a personal skillset (refuses 'default' and the currently-selected one)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		slug := args[0]
		if slug == "default" {
			return errors.New("cannot delete 'default' — it's auto-created and load-bearing; pick a different skillset to switch to and delete this one via the dashboard if you really need to")
		}
		cfg, cfgErr := config.Load()
		if cfgErr != nil {
			return cfgErr
		}
		if cfg.Skillset == slug {
			return fmt.Errorf("cannot delete %q — it's your currently-selected skillset. Run 'airskills skillset use <other-slug>' first", slug)
		}
		if !skillsetDeleteForce {
			fmt.Printf("Delete skillset %q? This cannot be undone. [y/N] ", slug)
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
		if err := client.deletePersonalSkillset(slug); err != nil {
			return err
		}
		fmt.Printf("Deleted skillset %q\n", slug)
		return nil
	},
}

var skillsetUseCmd = &cobra.Command{
	Use:   "use <slug>",
	Short: "Switch the remembered default skillset to <slug>",
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
		skillsets, err := client.listSkillsets()
		if err != nil {
			return err
		}
		found := false
		for _, s := range skillsets {
			if s.Slug == slug {
				found = true
				break
			}
		}
		if !found {
			names := make([]string, 0, len(skillsets))
			for _, s := range skillsets {
				names = append(names, s.Slug)
			}
			if len(names) == 0 {
				return fmt.Errorf("skillset %q not found — you have no personal skillsets yet", slug)
			}
			return fmt.Errorf("skillset %q not found. Your skillsets: %s", slug, strings.Join(names, ", "))
		}
		cfg, cfgErr := config.Load()
		if cfgErr != nil {
			return cfgErr
		}
		cfg.Skillset = slug
		if err := cfg.Save(); err != nil {
			return fmt.Errorf("save skillset preference: %w", err)
		}
		fmt.Printf("Switched default skillset to %q\n", slug)
		printAgentNextSteps(os.Stdout, []agentNextStep{
			{Cmd: "airskills sync", Why: "pull the skills in this skillset onto the machine"},
			{Cmd: "airskills status", Why: "see where things stand under the new skillset"},
		})
		return nil
	},
}

func init() {
	skillsetCreateCmd.Flags().StringVar(&skillsetCreateName, "name", "", "Human-readable name (defaults to slug)")
	skillsetCreateCmd.Flags().StringVar(&skillsetCreateDescription, "description", "", "Optional one-line description")
	skillsetDeleteCmd.Flags().BoolVar(&skillsetDeleteForce, "force", false, "Skip the confirmation prompt")

	skillsetParentCmd.AddCommand(skillsetListCmd)
	skillsetParentCmd.AddCommand(skillsetCreateCmd)
	skillsetParentCmd.AddCommand(skillsetDeleteCmd)
	skillsetParentCmd.AddCommand(skillsetUseCmd)
	rootCmd.AddCommand(skillsetParentCmd)
}
