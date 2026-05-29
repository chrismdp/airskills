package cmd

import (
	"fmt"
	"io"
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
	// Ownership query: every skill the caller owns server-side,
	// regardless of which personal skillset it belongs to. Used to
	// label locals that are owned-but-not-in-the-active-skillset, so
	// `airskills skillset use <other>` doesn't make 70 perfectly safe
	// skills look like "to push" (they would have collided on name).
	ownedCh := make(chan []apiSkill, 1)

	go func() {
		skills, _, err := client.listPersonalSkillsInSkillset(rememberedSkillsetSlug())
		skillsCh <- skillsResult{skills, err}
	}()

	go func() {
		// Ownership query: returns every skill the caller owns across
		// every personal skillset (cross-skillset view), so we can
		// distinguish "in another skillset" from "local-only".
		owned, err := client.listSkills("personal")
		if err != nil {
			ownedCh <- nil
			return
		}
		ownedCh <- owned
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
	pendingConflicts := pendingConflictNames()

	sr := <-skillsCh
	if sr.err != nil {
		// Still drain the other channels so their goroutines don't leak
		<-healthCh
		<-suggCh
		<-ownedCh
		return nil
	}
	hr := <-healthCh
	pendingSuggestions := <-suggCh
	ownedAll := <-ownedCh

	// Build the owned-elsewhere set: skills the caller owns server-side
	// minus the ones already in the active skillset's listing. We key
	// by local dir name (via the marker's SkillID) because that's what
	// the classifier loops over. If the ownership query failed
	// (ownedAll == nil) we pass nil through and the classifier behaves
	// as before — these skills will appear as "to push", which is
	// wrong but at least visible.
	ownedElsewhereByName := buildOwnedElsewhereByName(sr.skills, ownedAll, syncState)

	hashLocal := func(p string) string { return computeMerkleHash(readSkillFiles(p)) }
	buckets := classifyForStatus(sr.skills, localSkills, syncState, hashLocal, ownedElsewhereByName)
	toPush, toPull, toUpdate, upstream, untracked, inOtherSkillset := buckets.toPush, buckets.toPull, buckets.toUpdate, buckets.upstream, buckets.untracked, buckets.inOtherSkillset

	needPush := len(toPush)
	needPull := len(toPull)
	needUpdate := len(toUpdate)
	needUntracked := len(untracked)
	upstreamUpdates := len(upstream)
	needInOther := len(inOtherSkillset)
	needPendingConflicts := len(pendingConflicts)

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
			"pending_conflicts":   needPendingConflicts,
		})
	}

	// Compiled-in `version` lags the on-disk binary after an in-process
	// auto-update, so isNewer above falsely flags an upgrade.
	showLatestCLI := hr.latestCLI != "" && !autoUpdateDidFire.Load()

	if needPush == 0 && needPull == 0 && needUpdate == 0 && needUntracked == 0 && upstreamUpdates == 0 && pendingSuggestions == 0 && needInOther == 0 && needPendingConflicts == 0 && !showLatestCLI {
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
		parts = append(parts, yellow(fmt.Sprintf("~ %d on server", needUpdate)))
	}
	if upstreamUpdates > 0 {
		parts = append(parts, cyan(fmt.Sprintf("⬆ %d upstream", upstreamUpdates)))
	}
	if needUntracked > 0 {
		parts = append(parts, yellow(fmt.Sprintf("? %d untracked", needUntracked)))
	}
	if needInOther > 0 {
		parts = append(parts, yellow(fmt.Sprintf("⚠ %d in other skillset", needInOther)))
	}
	if needPendingConflicts > 0 {
		parts = append(parts, yellow(fmt.Sprintf("⚠ %d pending conflict", needPendingConflicts)))
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
		printStatusGroup("on server", toUpdate, yellow)
		printStatusGroup("upstream", upstream, cyan)
		printStatusGroup("untracked", untracked, yellow)
		printPendingConflictStatusGroup(os.Stderr, pendingConflicts)
		if needInOther > 0 {
			active := rememberedSkillsetSlug()
			if active == "" {
				active = "(default)"
			}
			fmt.Fprintf(os.Stderr, "  %s (%d): owned server-side but not in active skillset %q — sync ignores them; switch back with 'airskills skillset use <other>' to re-include\n",
				yellow("in other skillset"), needInOther, active)
			for _, n := range inOtherSkillset {
				fmt.Fprintf(os.Stderr, "    %s\n", n)
			}
		}
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
		if needPendingConflicts > 0 {
			steps = append(steps, agentNextStep{Cmd: "airskills rm <name>", Why: "discard a stale pending conflict copy after reviewing it"})
		}
		printAgentNextSteps(os.Stderr, steps)
	}

	return nil
}

func printPendingConflictStatusGroup(w io.Writer, names []string) {
	if len(names) == 0 {
		return
	}
	fmt.Fprintf(w, "  %s (%d): temp remote copies kept for review; discard with 'airskills rm <name>'\n", yellow("pending conflicts"), len(names))
	for _, n := range names {
		fmt.Fprintf(w, "    %s  discard: airskills rm %s\n", n, n)
	}
}

// buildOwnedElsewhereByName produces the set of local dir names whose
// marker skill_id is owned by the caller server-side but not in the
// current skillset's listing. The lookup is local-name → bool so the
// classifier (which loops over local dir names) can ask "is this name
// in another skillset?" in O(1).
//
// ownedAll comes from the personal-scope ownership query in runStatus.
func buildOwnedElsewhereByName(inSkillset []apiSkill, ownedAll []apiSkill, state *SyncState) map[string]bool {
	if len(ownedAll) == 0 || state == nil {
		return nil
	}
	inSkillsetIDs := map[string]bool{}
	for _, s := range inSkillset {
		inSkillsetIDs[s.Id.String()] = true
	}
	elsewhereIDs := map[string]bool{}
	for _, s := range ownedAll {
		id := s.Id.String()
		if inSkillsetIDs[id] {
			continue
		}
		elsewhereIDs[id] = true
	}
	if len(elsewhereIDs) == 0 {
		return nil
	}
	out := map[string]bool{}
	for name, entry := range state.Skills {
		if entry == nil || entry.SkillID == "" {
			continue
		}
		if elsewhereIDs[entry.SkillID] {
			out[name] = true
		}
	}
	return out
}

// statusBuckets is the cross-state of skills for the status command.
// untracked: a remote-known skill exists locally with no marker — next
// sync would surface a conflict (silent in this command before).
type statusBuckets struct {
	toPush, toPull, toUpdate, upstream, untracked, inOtherSkillset []string
}

// classifyForStatus is pure — no I/O — so it's unit-testable. The status
// command needs six buckets:
//
//   - toPush:           local skill, no remote anywhere; OR tracked skill with local edits
//   - toPull:           remote skill, no local
//   - toUpdate:         tracked, marker hash differs from remote hash
//   - upstream:         remote has an upstream update available (sourced)
//   - untracked:        remote skill, local exists, no marker — needs reconciling
//   - inOtherSkillset:  local skill that the user owns server-side, but
//     the skill is not in the active personal skillset.
//     Without this bucket the classifier dumped them in
//     toPush, which lied — push would have collided on
//     name. ownedElsewhereByName is the cross-reference
//     (skill names the caller owns but that are not in
//     remoteSkills); status fetches this via the
//     personal-scope ownership query in runStatus.
func classifyForStatus(remoteSkills []apiSkill, localSkills map[string]string, syncState *SyncState, hashLocal func(string) string, ownedElsewhereByName map[string]bool) statusBuckets {
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
	accountedLocal := map[string]bool{}
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
			accountedLocal[trackedName] = true
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
			accountedLocal[remote.Name] = true
			b.untracked = append(b.untracked, remote.Name)
		} else {
			b.toPull = append(b.toPull, remote.Name)
		}
	}

	for name := range localSkills {
		if accountedLocal[name] {
			continue
		}
		if syncState != nil {
			if entry := syncState.Skills[name]; entry != nil && entry.Deleted {
				continue
			}
		}
		if remoteByName[name] {
			continue
		}
		if ownedElsewhereByName[name] {
			b.inOtherSkillset = append(b.inOtherSkillset, name)
			continue
		}
		b.toPush = append(b.toPush, name)
	}

	// Detect locally-modified tracked skills. If a tracked skill's local
	// content hash differs from the marker hash, it needs pushing — even
	// though the remote knows about it. The previous implementation only
	// caught new-skills-never-pushed (not-on-remote) and missed local edits
	// to skills that already exist on the server.
	if syncState != nil && hashLocal != nil {
		for name, entry := range syncState.Skills {
			if entry == nil || entry.ContentHash == "" {
				continue
			}
			if _, exists := localSkills[name]; !exists {
				continue
			}
			// Already in toPush (not on remote) — don't duplicate
			if remoteByName[name] || accountedLocal[name] {
				localHash := hashLocal(localSkills[name])
				if localHash != entry.ContentHash {
					b.toPush = append(b.toPush, name)
				}
			}
		}
	}

	sort.Strings(b.toPush)
	sort.Strings(b.toPull)
	sort.Strings(b.toUpdate)
	sort.Strings(b.upstream)
	sort.Strings(b.untracked)
	sort.Strings(b.inOtherSkillset)
	return b
}

// printStatusGroup prints a detail block for one action category, e.g.
//
//	to push (2):
//	  my-skill
//	  other-skill
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
