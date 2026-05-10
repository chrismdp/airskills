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

	buckets := classifyForStatus(sr.skills, localSkills, syncState)
	toPush, toPull, toUpdate, upstream, untracked := buckets.toPush, buckets.toPull, buckets.toUpdate, buckets.upstream, buckets.untracked

	needPush := len(toPush)
	needPull := len(toPull)
	needUpdate := len(toUpdate)
	needUntracked := len(untracked)
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
			"need_untracked":      needUntracked,
			"upstream_updates":    upstreamUpdates,
			"pending_suggestions": pendingSuggestions,
		})
	}

	// Compiled-in `version` lags the on-disk binary after an in-process
	// auto-update, so isNewer above falsely flags an upgrade.
	showLatestCLI := hr.latestCLI != "" && !autoUpdateDidFire.Load()

	if needPush == 0 && needPull == 0 && needUpdate == 0 && needUntracked == 0 && upstreamUpdates == 0 && pendingSuggestions == 0 && !showLatestCLI {
		if !quiet {
			fmt.Fprintf(os.Stderr, "[airskills] %s\n", green("✓ in sync"))
			printAgentNextSteps(os.Stderr, []agentNextStep{
				{Cmd: "airskills add <owner>/<skill>", Why: "install a public skill"},
				{Cmd: "airskills push", Why: "upload a skill you've created locally"},
			})
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
	if needUntracked > 0 {
		parts = append(parts, yellow(fmt.Sprintf("? %d untracked", needUntracked)))
	}
	if pendingSuggestions > 0 {
		parts = append(parts, cyan(fmt.Sprintf("? %d suggestions", pendingSuggestions)))
	}

	// Pick the most relevant hint for the one-line command: suggestions
	// trumps sync because review is a separate workflow.
	hint := "airskills sync"
	if pendingSuggestions > 0 && needPush == 0 && needPull == 0 && needUpdate == 0 && needUntracked == 0 && upstreamUpdates == 0 {
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
		printStatusGroup("untracked", untracked, yellow)
	}

	if showLatestCLI {
		fmt.Fprintf(os.Stderr, "[airskills] %s → %s: run 'airskills self-update'\n",
			yellow("update"), hr.latestCLI)
	}

	// Next-step hints for an agent reading this output: tuned to the
	// specific mix of work detected above. Suppressed in --quiet mode
	// (shell-prompt hot path) and on TTY by printAgentNextSteps.
	if !quiet {
		var steps []agentNextStep
		if needPull > 0 || needUpdate > 0 || upstreamUpdates > 0 {
			steps = append(steps, agentNextStep{Cmd: "airskills sync", Why: "pull remote changes"})
		}
		if needPush > 0 {
			steps = append(steps, agentNextStep{Cmd: "airskills push", Why: "upload local changes"})
		}
		if needUntracked > 0 {
			steps = append(steps, agentNextStep{Cmd: "airskills sync", Why: "reconcile untracked skills (server has same name, no local marker)"})
		}
		if pendingSuggestions > 0 {
			steps = append(steps, agentNextStep{Cmd: "airskills review", Why: "review incoming suggestions"})
		}
		printAgentNextSteps(os.Stderr, steps)
	}

	return nil
}

// statusBuckets is the cross-state of skills for the status command.
// untracked: a remote-known skill exists locally with no marker — next
// sync would surface a conflict (silent in this command before).
type statusBuckets struct {
	toPush, toPull, toUpdate, upstream, untracked []string
}

// classifyForStatus is pure — no I/O — so it's unit-testable. The status
// command needs five buckets:
//
//   - toPush:    local skill, no remote
//   - toPull:    remote skill, no local
//   - toUpdate:  tracked, marker hash differs from remote hash
//   - upstream:  remote has an upstream update available (sourced)
//   - untracked: remote skill, local exists, no marker — needs reconciling
func classifyForStatus(remoteSkills []apiSkill, localSkills map[string]string, syncState *SyncState) statusBuckets {
	skillIdToName := map[string]string{}
	if syncState != nil {
		for name, entry := range syncState.Skills {
			if entry != nil && entry.SkillID != "" {
				skillIdToName[entry.SkillID] = name
			}
		}
	}

	var b statusBuckets
	remoteByName := map[string]bool{}
	for _, remote := range remoteSkills {
		remoteByName[remote.Name] = true

		if skillHasUpstreamUpdate(remote) {
			b.upstream = append(b.upstream, remote.Name)
		}

		trackedName := skillIdToName[remote.Id.String()]

		if trackedName != "" {
			if _, exists := localSkills[trackedName]; !exists {
				continue
			}
			marker := syncState.Skills[trackedName]
			remoteHash := strDeref(remote.ContentHash)
			if marker != nil && marker.ContentHash != "" && remoteHash != "" && marker.ContentHash != remoteHash {
				b.toUpdate = append(b.toUpdate, trackedName)
			}
			continue
		}

		// No marker. If local dir exists with the same name, the next
		// sync will surface a conflict — flag it now rather than going
		// silent like the previous implementation did.
		if _, exists := localSkills[remote.Name]; exists {
			b.untracked = append(b.untracked, remote.Name)
		} else {
			b.toPull = append(b.toPull, remote.Name)
		}
	}

	for name := range localSkills {
		if !remoteByName[name] {
			b.toPush = append(b.toPush, name)
		}
	}

	sort.Strings(b.toPush)
	sort.Strings(b.toPull)
	sort.Strings(b.toUpdate)
	sort.Strings(b.upstream)
	sort.Strings(b.untracked)
	return b
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
