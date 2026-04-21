package cmd

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/chrismdp/airskills/config"
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
func stdinReader() io.Reader   { return os.Stdin }
func stderrWriter() io.Writer  { return os.Stderr }
