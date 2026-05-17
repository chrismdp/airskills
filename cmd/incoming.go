package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/chrismdp/airskills/config"
	"github.com/chrismdp/airskills/telemetry"
	"github.com/spf13/cobra"
)

var incomingCmd = &cobra.Command{
	Use:   "incoming [skill]",
	Short: "Review upstream changes for skills you do not own",
	Long: `Review upstream changes for skills you installed from another owner.

With no arguments, lists non-owned skills whose upstream has changed since
you last incorporated it. With a skill name, downloads the upstream copy to a
temporary directory so an agent can compare it with your local version.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runIncomingListOrReview,
}

var incomingReviewCmd = &cobra.Command{
	Use:   "review <skill>",
	Short: "Download upstream files to a temp directory for comparison",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runIncomingReview(args[0])
	},
}

var incomingIncorporateCmd = &cobra.Command{
	Use:   "incorporate <skill>",
	Short: "Overwrite local files with the current upstream version",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runIncomingIncorporate(args[0])
	},
}

var incomingDeferCmd = &cobra.Command{
	Use:   "defer <skill>",
	Short: "Mark current upstream as seen without changing local files",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runIncomingDefer(args[0])
	},
}

func init() {
	incomingCmd.AddCommand(incomingReviewCmd)
	incomingCmd.AddCommand(incomingIncorporateCmd)
	incomingCmd.AddCommand(incomingDeferCmd)
	rootCmd.AddCommand(incomingCmd)
}

type incomingCandidate struct {
	entry pullEntry
}

func loadIncomingCandidates() (*apiClient, *SyncState, []incomingCandidate, error) {
	client, err := newAPIClientAuto()
	if err != nil {
		return nil, nil, nil, err
	}
	state := loadSyncState()
	// Resolve partial renames before mirror so a hand-`mv` on one agent
	// dir is propagated rather than restored by mirror's fan-out (the
	// push path already does this; mirror in pull-side contexts needs
	// the same guard).
	if scanned, scanErr := scanSkillsFromAgents(); scanErr == nil {
		propagatePartialRenames(scanned, state)
	}
	_, mirrorConflicts, restoreHints := mirrorLocalSkills(state)
	printMirrorRestoreHints(restoreHints)
	skip := map[string]bool{}
	for _, c := range mirrorConflicts {
		skip[c.slug] = true
	}
	localSkills, err := scanSkillsFromAgents()
	if err != nil {
		return nil, nil, nil, err
	}
	for slug := range skip {
		delete(localSkills, slug)
	}
	cfg, cfgErr := config.Load()
	if cfgErr != nil {
		return nil, nil, nil, cfgErr
	}
	sendSlug, err := resolveSkillsetFlag(cfg, skillsetFlag, stdinReader(), stderrWriter())
	if err != nil {
		return nil, nil, nil, err
	}
	remote, _, err := client.listPersonalSkillsInSkillset(sendSlug)
	if err != nil {
		return nil, nil, nil, err
	}
	actions, _, _ := decidePullActions(remote, localSkills, state)
	var candidates []incomingCandidate
	for _, a := range actions {
		if a.reason == "upstream-advanced" || a.reason == "upstream-updated" {
			candidates = append(candidates, incomingCandidate{entry: a})
		}
	}
	return client, state, candidates, nil
}

func findIncomingSkill(name string) (*apiClient, *SyncState, incomingCandidate, error) {
	client, state, candidates, err := loadIncomingCandidates()
	if err != nil {
		return nil, nil, incomingCandidate{}, err
	}
	for _, c := range candidates {
		if c.entry.skill.Name == name || filepath.Base(c.entry.localDir) == name {
			return client, state, c, nil
		}
	}
	return nil, nil, incomingCandidate{}, fmt.Errorf("%s: no incoming upstream changes", name)
}

func runIncomingListOrReview(cmd *cobra.Command, args []string) error {
	if len(args) == 1 {
		return runIncomingReview(args[0])
	}
	_, _, candidates, err := loadIncomingCandidates()
	if err != nil {
		return err
	}
	fmt.Println("=== Incoming upstream changes ===")
	fmt.Println()
	if len(candidates) == 0 {
		fmt.Printf("  %s no incoming upstream changes\n", green("✓"))
		return nil
	}
	for _, c := range candidates {
		source := c.entry.marker.Source.Owner + "/" + c.entry.marker.Source.Slug
		if c.entry.marker.Source.Owner == "" {
			source = c.entry.marker.Source.Slug
		}
		state := "edited locally"
		if c.entry.reason == "upstream-updated" {
			state = "clean; can incorporate"
		}
		fmt.Printf("  %s %s from %s (%s)\n", yellow("!"), c.entry.skill.Name, source, state)
		fmt.Printf("     Review:      airskills incoming review %s\n", c.entry.skill.Name)
		fmt.Printf("     Incorporate: airskills incoming incorporate %s\n", c.entry.skill.Name)
		fmt.Printf("     Defer:       airskills incoming defer %s\n", c.entry.skill.Name)
	}
	telemetry.Capture("cli_incoming_list", map[string]interface{}{"count": len(candidates)})
	return nil
}

func runIncomingReview(name string) error {
	client, _, candidate, err := findIncomingSkill(name)
	if err != nil {
		return err
	}
	files, err := downloadSkillFiles(client, sourceUpstreamID(candidate.entry.marker.Source))
	if err != nil {
		return fmt.Errorf("downloading upstream files: %w", err)
	}
	tmpDir, err := os.MkdirTemp("", "airskills-incoming-"+name+"-")
	if err != nil {
		return err
	}
	if err := writeFilesToDir(tmpDir, files); err != nil {
		return err
	}
	fmt.Printf("Downloaded upstream to:\n  %s\n\n", tmpDir)
	fmt.Printf("Local version:\n  %s\n\n", candidate.entry.localDir)
	fmt.Printf("Compare the files, then run 'airskills incoming incorporate %s' or 'airskills incoming defer %s'.\n", name, name)
	telemetry.Capture("cli_incoming_review", map[string]interface{}{"skill": name})
	return nil
}

func runIncomingIncorporate(name string) error {
	client, state, candidate, err := findIncomingSkill(name)
	if err != nil {
		return err
	}
	p := candidate.entry
	if p.marker == nil || p.marker.Source == nil {
		return fmt.Errorf("%s: missing source marker", name)
	}
	if p.skill.Id.String() != sourceUpstreamID(p.marker.Source) {
		advanced, err := client.pullUpstream(p.skill.Id.String())
		if err != nil {
			return fmt.Errorf("advancing fork from upstream: %w", err)
		}
		p.skill = *advanced
	}
	files, err := downloadSkillFiles(client, p.skill.Id.String())
	if err != nil {
		return fmt.Errorf("downloading upstream files: %w", err)
	}
	ts := time.Now().UTC().Format("20060102-150405")
	if _, err := backupSkillToUndo(filepath.Base(p.localDir), ts); err != nil {
		return err
	}
	dirName := filepath.Base(p.localDir)
	if _, err := installSkillToAgents(dirName, files); err != nil {
		return err
	}
	p.marker.ContentHash = computeMerkleHash(files)
	p.marker.Version = p.skill.Version
	markSourceUpstreamIncorporated(p.marker, p.skill)
	state.Skills[dirName] = p.marker
	if err := saveSyncState(state); err != nil {
		return err
	}
	fmt.Printf("%s %s incorporated upstream changes. Previous local files are in ~/.airskills/undo/%s/%s/\n",
		green("✓"), dirName, ts, dirName)
	telemetry.Capture("cli_incoming_incorporate", map[string]interface{}{"skill": name})
	return nil
}

func runIncomingDefer(name string) error {
	_, state, candidate, err := findIncomingSkill(name)
	if err != nil {
		return err
	}
	dirName := filepath.Base(candidate.entry.localDir)
	markSourceUpstreamIncorporated(candidate.entry.marker, candidate.entry.skill)
	state.Skills[dirName] = candidate.entry.marker
	if err := saveSyncState(state); err != nil {
		return err
	}
	fmt.Printf("%s %s deferred upstream changes\n", green("✓"), dirName)
	telemetry.Capture("cli_incoming_defer", map[string]interface{}{"skill": name})
	return nil
}
