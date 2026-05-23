package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/chrismdp/airskills/config"
	"github.com/chrismdp/airskills/internal/apitypes"
	"github.com/spf13/cobra"
)

// skillsetFlag holds the value of --skillset on sync/push/pull. Empty means
// "not provided on this invocation".
var skillsetFlag string

// resolveSkillsetFlag decides what skillset slug to send on the upcoming
// API call given the CLI flag value and the remembered default in cfg.
//
// Rules:
//
//   - flag empty, nothing remembered → return empty (server picks default,
//     caller should remember the resolved slug on successful response).
//   - flag empty, something remembered → return the remembered slug.
//   - flag matches remembered → return the flag, no notice.
//   - flag differs from remembered → silently treat the flag as a
//     `skillset use`: persist the new slug and return it. A short notice
//     is printed when we switched away from a previously remembered slug
//     so users don't lose track of which default they're on.
//   - flag but nothing remembered → return and remember the flag silently.
//
// Persistence is via cfg.Save(); callers pass the *config.Config they loaded.
// The reader argument is retained for ABI stability with older callers but
// is no longer consulted — the prompt was removed because `--skillset` is
// already an explicit user choice and an extra confirmation just blocked
// non-interactive runs (CI, agents).
func resolveSkillsetFlag(cfg *config.Config, flag string, _ io.Reader, writer io.Writer) (string, error) {
	// Migration 047 (2026-05-23) collapsed user-side skillsets to a
	// single implicit 'default'. The --skillset flag and the
	// remembered cfg.Skillset are now no-ops on the user side — the
	// server's /api/v1/skills GET silently coerces any passed slug to
	// the user's default. Warn once if either is set so users can
	// stop passing the flag, but don't fail.
	if flag != "" && flag != "default" {
		fmt.Fprintf(writer, "Note: --skillset is no longer used for user accounts (one default per user). Ignoring %q.\n", flag)
	}
	if cfg.Skillset != "" && cfg.Skillset != "default" {
		// Clear stale config so the warning fires once, not on every command.
		cfg.Skillset = ""
		_ = cfg.Save()
	}
	return "", nil
}

// rememberedSkillsetSlug returns the user's last-used personal skillset
// slug from local config (set by `airskills skillset use` or persisted
// after the first sync), or empty if nothing is remembered. Read-only
// commands that don't take --skillset (status, doctor, list, history,
// update, export) use this so they describe the same skillset the next
// sync/pull would touch — without it, the server resolved an empty
// slug to is_default and silently reported the wrong skillset.
func rememberedSkillsetSlug() string {
	cfg, err := config.Load()
	if err != nil || cfg == nil {
		return ""
	}
	return cfg.Skillset
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
	Short: "List your default skillset (multi-skillset for users removed 2026-05-23)",
	Long: `Personal skillsets were collapsed to a single implicit 'default'
in May 2026 — see the dashboard at airskills.ai/dashboard. Org
skillsets are still a thing and are managed via the dashboard or
the 'airskills org' subcommands.`,
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
func renderSkillsetList(w io.Writer, skillsets []apitypes.SkillsetListItem, selected string) {
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

// `skillset create`, `skillset delete`, `skillset use` (+ set-default /
// auto-absorb) were removed on 2026-05-23 — see migration 047. Every
// user has exactly one implicit 'default' skillset. `skillset list`
// stays as the only user-facing read so cached automation isn't
// surprised by a missing subcommand.

func init() {
	skillsetParentCmd.AddCommand(skillsetListCmd)
	rootCmd.AddCommand(skillsetParentCmd)
}
