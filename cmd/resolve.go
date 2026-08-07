package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

var resolveCmd = &cobra.Command{
	Use:   "resolve <name>",
	Short: "Acknowledge that you've reviewed the original of a sourced skill",
	Long: `Records that you have reviewed your customised copy of a sourced
skill against the original's current version. Stops the modified-pending
prompt on every sync until the original moves again.

What it actually does: writes the upstream skill's current content
hash into your local marker as ResolvedHash. No file changes, no
push, no pull. The CLI just remembers "user has seen the original at
version V."

Three flows that all end in resolve:

  - Took nothing from upstream, kept your customised copy as-is.
  - Took some upstream ideas (manually or with AI), edited locally.
  - Resolved a previous review and the upstream has moved again.

If you instead want to take the whole upstream, replacing your local
copy: 'airskills add <owner>/<slug> --force'. That makes you synced;
resolve is unnecessary. (Not 'pull --force' — pull talks to your own
account's copy and only resolves conflicts there; taking an upstream's
bytes always goes through 'add'.)

For owned skills (your own namespace, no upstream) this is a no-op —
there's no "original" to resolve against.`,
	Args: cobra.ExactArgs(1),
	RunE: runResolve,
}

func init() {
	rootCmd.AddCommand(resolveCmd)
}

// applyResolve writes the upstream content hash into the marker's
// ResolvedHash field. Pure: no I/O, no network. The cobra wrapper does
// the network fetch and persists state. Errors:
//   - unknown <name> in sync state
//   - skill is owned (no Source) — resolve is meaningless
func applyResolve(state *SyncState, name, upstreamHash string) error {
	if state == nil {
		return fmt.Errorf("nil sync state")
	}
	entry := state.Skills[name]
	if entry == nil {
		return fmt.Errorf("%s: not tracked. Try 'airskills list' to see installed skills", name)
	}
	if entry.Source == nil {
		return fmt.Errorf("%s: owned skill — resolve is only meaningful for sourced skills (installed via 'airskills add')", name)
	}
	entry.ResolvedHash = upstreamHash
	return nil
}

func runResolve(cmd *cobra.Command, args []string) error {
	name := args[0]
	state := loadSyncState()

	entry := state.Skills[name]
	if entry == nil {
		return fmt.Errorf("%s: not tracked. Try 'airskills list' to see installed skills", name)
	}
	if entry.Source == nil {
		fmt.Printf("  %s %s is a skill you own — resolve is a no-op.\n", dim("·"), name)
		fmt.Println()
		fmt.Println("    'resolve' acknowledges that you've reviewed an upstream change to a")
		fmt.Println("    skill you SOURCED from someone else (installed via 'airskills add').")
		fmt.Println("    For skills you own, there's no upstream to resolve against — you ARE")
		fmt.Println("    the upstream.")
		fmt.Println()
		fmt.Println("    Looking for something else?")
		fmt.Printf("      airskills push                      publish local changes you've made to %s\n", name)
		fmt.Printf("      airskills review %s              see suggestions others have made\n", name)
		fmt.Println("      airskills review accept <id>        close out a suggestion you've merged")
		return nil
	}

	upstreamHash, err := fetchUpstreamHash(entry)
	if err != nil {
		return fmt.Errorf("fetching upstream content hash: %w", err)
	}
	if upstreamHash == "" {
		return fmt.Errorf("%s: upstream content hash unavailable from server", name)
	}

	if err := applyResolve(state, name, upstreamHash); err != nil {
		return err
	}
	if err := saveSyncState(state); err != nil {
		return fmt.Errorf("saving sync state: %w", err)
	}

	short := upstreamHash
	if len(short) > 12 {
		short = short[:12]
	}
	fmt.Printf("  %s %s reviewed against %s/%s @ %s\n",
		green("✓"), name, entry.Source.Owner, entry.Source.Slug, short)
	fmt.Println("    Future syncs will stay quiet until the original moves again.")
	return nil
}

// fetchUpstreamHash fetches the current upstream skill and returns its
// content hash. Resolves by Source.ID (the original skill's stable id,
// recorded at 'airskills add' time), or — for a GitHub source, which has
// no server-side row — by hashing SKILL.md in the repo tarball.
func fetchUpstreamHash(entry *SyncEntry) (string, error) {
	if entry.Source != nil && entry.Source.GitHubURL != "" {
		return fetchGitHubUpstreamHash(entry.Source)
	}
	if entry.Source == nil || entry.Source.ID == "" {
		return "", fmt.Errorf("marker has no upstream id")
	}
	client, err := newAPIClientAuto()
	if err != nil {
		return "", fmt.Errorf("authentication required: %w", err)
	}
	body, err := client.get(fmt.Sprintf("/api/v1/skills/%s", entry.Source.ID))
	if err != nil {
		return "", err
	}
	var resp struct {
		ContentHash string `json:"content_hash"`
	}
	if err := parseJSON(body, &resp); err != nil {
		return "", err
	}
	return resp.ContentHash, nil
}

// fetchGitHubUpstreamHash reads the current upstream hash for a
// GitHub-sourced skill straight from the repo. No account, no auth: the
// repo IS the upstream, so `resolve` works for a skill that was never on
// airskills.ai.
func fetchGitHubUpstreamHash(src *skillSource) (string, error) {
	owner, repo, _, err := parseGitHubURL(src.GitHubURL)
	if err != nil {
		return "", err
	}
	allFiles, err := downloadGitHubTarballFn(owner, repo)
	if err != nil {
		return "", err
	}
	hash := gitHubUpstreamHash(allFiles, src.GitHubSkill)
	if hash == "" {
		return "", fmt.Errorf("%s/%s no longer contains a skill named %q", owner, repo, src.GitHubSkill)
	}
	return hash, nil
}
