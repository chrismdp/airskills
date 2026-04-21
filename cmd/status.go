package cmd

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/chrismdp/airskills/telemetry"
	"github.com/spf13/cobra"
)

func init() {
	statusCmd.Flags().BoolP("quiet", "q", false, "Only output when updates are available")
	rootCmd.AddCommand(statusCmd)
}

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Check for skill updates",
	Long: `One-line sync status, designed for shell startup:
  eval "$(airskills status)"`,
	RunE: runStatus,
}

func runStatus(cmd *cobra.Command, args []string) error {
	quiet, _ := cmd.Flags().GetBool("quiet")

	client, err := newAPIClientAuto()
	if err != nil {
		return nil
	}

	type skillsResult struct {
		skills []apiSkill
		err    error
	}
	type healthResult struct {
		latestCLI string
	}

	skillsCh := make(chan skillsResult, 1)
	healthCh := make(chan healthResult, 1)
	suggCh := make(chan int, 1)

	go func() {
		skills, err := client.listSkills("personal")
		skillsCh <- skillsResult{skills, err}
	}()

	go func() {
		var latest string
		if body, err := client.get("/api/v1/health"); err == nil {
			var h struct {
				LatestCLI string `json:"latest_cli"`
			}
			if parseJSON(body, &h) == nil && h.LatestCLI != "" && isNewer(h.LatestCLI, version) && version != "dev" {
				latest = h.LatestCLI
			}
		}
		healthCh <- healthResult{latest}
	}()

	go func() {
		n, err := client.countSuggestions("owner", "pending", "")
		if err != nil {
			suggCh <- 0
			return
		}
		suggCh <- n
	}()

	localSkills, _ := scanSkillsFromAgents()
	syncState := loadSyncState()

	sr := <-skillsCh
	if sr.err != nil {
		// Still drain the other channels so their goroutines don't leak
		<-healthCh
		<-suggCh
		return nil
	}
	hr := <-healthCh
	pendingSuggestions := <-suggCh

	skillIdToName := map[string]string{}
	for name, entry := range syncState.Skills {
		if entry.SkillID != "" {
			skillIdToName[entry.SkillID] = name
		}
	}

	var toPush, toPull, toUpdate, upstream []string

	remoteByName := map[string]bool{}
	for _, remote := range sr.skills {
		remoteByName[remote.Name] = true

		if remote.HasUpstreamUpdate() {
			upstream = append(upstream, remote.Name)
		}

		trackedName := ""
		if name, ok := skillIdToName[remote.ID]; ok {
			trackedName = name
		}

		if trackedName != "" {
			if _, exists := localSkills[trackedName]; !exists {
				continue
			}
			marker := syncState.Skills[trackedName]
			if marker.ContentHash != "" && remote.ContentHash != "" && marker.ContentHash != remote.ContentHash {
				toUpdate = append(toUpdate, trackedName)
			}
			continue
		}

		if _, exists := localSkills[remote.Name]; !exists {
			toPull = append(toPull, remote.Name)
		}
	}

	for name := range localSkills {
		if !remoteByName[name] {
			toPush = append(toPush, name)
		}
	}

	sort.Strings(toPush)
	sort.Strings(toPull)
	sort.Strings(toUpdate)
	sort.Strings(upstream)

	needPush := len(toPush)
	needPull := len(toPull)
	needUpdate := len(toUpdate)
	upstreamUpdates := len(upstream)

	// Skip capture on the shell-prompt hot path (quiet mode is used by
	// `eval "$(airskills status)"` in shell init). Capturing there would
	// flood PostHog with one event per shell window and block the prompt
	// on Flush every time.
	if !quiet {
		telemetry.Capture("cli_status", map[string]interface{}{
			"need_push":           needPush,
			"need_pull":           needPull,
			"need_update":         needUpdate,
			"upstream_updates":    upstreamUpdates,
			"pending_suggestions": pendingSuggestions,
		})
	}

	if needPush == 0 && needPull == 0 && needUpdate == 0 && upstreamUpdates == 0 && pendingSuggestions == 0 && hr.latestCLI == "" {
		if !quiet {
			fmt.Fprintf(os.Stderr, "[airskills] %s\n", green("✓ in sync"))
		}
		return nil
	}

	var parts []string
	if needPush > 0 {
		parts = append(parts, yellow(fmt.Sprintf("↑ %d to push", needPush)))
	}
	if needPull > 0 {
		parts = append(parts, cyan(fmt.Sprintf("↓ %d to pull", needPull)))
	}
	if needUpdate > 0 {
		parts = append(parts, yellow(fmt.Sprintf("~ %d changed", needUpdate)))
	}
	if upstreamUpdates > 0 {
		parts = append(parts, cyan(fmt.Sprintf("⬆ %d upstream", upstreamUpdates)))
	}
	if pendingSuggestions > 0 {
		parts = append(parts, cyan(fmt.Sprintf("? %d suggestions", pendingSuggestions)))
	}

	// Pick the most relevant hint for the one-line command: suggestions
	// trumps sync because review is a separate workflow.
	hint := "airskills sync"
	if pendingSuggestions > 0 && needPush == 0 && needPull == 0 && needUpdate == 0 && upstreamUpdates == 0 {
		hint = "airskills review"
	}
	fmt.Fprintf(os.Stderr, "[airskills] %s — run '%s'\n", strings.Join(parts, ", "), hint)

	// Detail groups — show the actual skill names under each action so the
	// user (and any agent driving the CLI) can see exactly what's about to
	// move. Matches git status's "to push / to pull" layout. Skipped when
	// --quiet so the shell-prompt hot path stays one-line.
	if !quiet {
		printStatusGroup("to push", toPush, yellow)
		printStatusGroup("to pull", toPull, cyan)
		printStatusGroup("changed", toUpdate, yellow)
		printStatusGroup("upstream", upstream, cyan)
	}

	if hr.latestCLI != "" {
		fmt.Fprintf(os.Stderr, "[airskills] %s → %s: run 'airskills self-update'\n",
			yellow("update"), hr.latestCLI)
	}

	return nil
}

// printStatusGroup prints a detail block for one action category, e.g.
//
//	  to push (2):
//	    my-skill
//	    other-skill
//
// Silent when names is empty — keeps the output compact for the common
// "nothing in this bucket" case.
func printStatusGroup(label string, names []string, color func(string) string) {
	if len(names) == 0 {
		return
	}
	fmt.Fprintf(os.Stderr, "  %s (%d):\n", color(label), len(names))
	for _, n := range names {
		fmt.Fprintf(os.Stderr, "    %s\n", n)
	}
}
