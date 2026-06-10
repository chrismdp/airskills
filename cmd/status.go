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

	// Surface auth/config failures — a silent nil here made a logged-out
	// machine indistinguishable from a healthy idle one.
	client, err := newAPIClientAuto()
	if err != nil {
		cmd.SilenceUsage = true
		return err
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

	// Local forks (agent copies edited differently) — sync/push will refuse
	// these until reconciled, so they get their own bucket below instead of
	// being counted as "to push" (which implies a sync would clear them).
	// Read-only detection: status must never mirror/write.
	localForks := detectLocalForks(syncState)
	forkNames := map[string]bool{}
	for _, c := range localForks {
		forkNames[c.slug] = true
	}

	sr := <-skillsCh
	if sr.err != nil {
		// Still drain the other channels so their goroutines don't leak
		<-healthCh
		<-suggCh
		<-ownedCh
		cmd.SilenceUsage = true
		return fmt.Errorf("could not fetch your skills from the server: %w", sr.err)
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
	rows := classifySkills(sr.skills, localSkills, syncState, hashLocal)
	buckets := projectStatusBuckets(rows, ownedElsewhereByName)
	toPush, toPull, toUpdate, upstream, untracked, inOtherSkillset := buckets.toPush, buckets.toPull, buckets.toUpdate, buckets.upstream, buckets.untracked, buckets.inOtherSkillset
	tombstoned := buckets.tombstoned

	// A forked skill classifies as dirty/untracked from its first-found copy,
	// but no push, pull, or update can act on it until the copies agree —
	// report it only in the local-fork bucket.
	toPush = dropNames(toPush, forkNames)
	toUpdate = dropNames(toUpdate, forkNames)
	untracked = dropNames(untracked, forkNames)

	// A pull conflict now parks its remote copy to the stable conflict path
	// AND surfaces live in the "untracked" bucket (or "on server"/toUpdate
	// for a tracked divergence). Don't double-report: drop pending-conflict
	// names already shown there, so the same skill isn't listed twice.
	pendingConflicts = dropPendingConflictDuplicates(pendingConflicts, untracked, toUpdate)

	needPush := len(toPush)
	needPull := len(toPull)
	needUpdate := len(toUpdate)
	needUntracked := len(untracked)
	upstreamUpdates := len(upstream)
	needInOther := len(inOtherSkillset)
	needPendingConflicts := len(pendingConflicts)
	needTombstoned := len(tombstoned)
	needForks := len(localForks)

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
			"tombstoned":          needTombstoned,
			"local_forks":         needForks,
		})
	}

	// Compiled-in `version` lags the on-disk binary after an in-process
	// auto-update, so isNewer above falsely flags an upgrade.
	showLatestCLI := hr.latestCLI != "" && !autoUpdateDidFire.Load()

	if needPush == 0 && needPull == 0 && needUpdate == 0 && needUntracked == 0 && upstreamUpdates == 0 && pendingSuggestions == 0 && needInOther == 0 && needPendingConflicts == 0 && needTombstoned == 0 && needForks == 0 && !showLatestCLI {
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
	if needTombstoned > 0 {
		parts = append(parts, yellow(fmt.Sprintf("⚠ %d marked transferred", needTombstoned)))
	}
	if needForks > 0 {
		parts = append(parts, yellow(fmt.Sprintf("⚠ %d local fork", needForks)))
	}

	// Pick the headline command for the work that actually dominates.
	// `sync` is correct ONLY when there's push/pull/update/untracked/upstream
	// work — it's a no-op for both pending conflicts and suggestions, so
	// pointing at it there sends the user into a status→sync→status loop
	// (the warning never clears because sync never touches it). Order:
	// real sync work → sync; else suggestions → review; else a pending
	// conflict → the safe --pending discard (full resolution menu prints
	// in the detail block below).
	hasSyncWork := needPush > 0 || needPull > 0 || needUpdate > 0 || needUntracked > 0 || upstreamUpdates > 0 || needTombstoned > 0
	hint := "airskills sync"
	switch {
	case hasSyncWork:
		hint = "airskills sync"
	case pendingSuggestions > 0:
		hint = "airskills review"
	case needPendingConflicts > 0:
		hint = "airskills rm <name> --pending"
	case needForks > 0:
		// No command resolves a fork — the copies must be reconciled by
		// hand (or an agent). Point at the detail block, not at sync,
		// which would refuse and loop the user back here.
		hint = ""
	}
	if hint != "" {
		fmt.Fprintf(os.Stderr, "[airskills] %s — run '%s'\n", strings.Join(parts, ", "), hint)
	} else {
		fmt.Fprintf(os.Stderr, "[airskills] %s — reconcile the copies below\n", strings.Join(parts, ", "))
	}

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
		printLocalForkStatusGroup(os.Stderr, localForks)
		printPendingConflictStatusGroup(os.Stderr, pendingConflicts)
		if needTombstoned > 0 {
			fmt.Fprintf(os.Stderr, "  %s (%d): present locally but marked transferred away. If it's back in your skillset, 'airskills sync' re-adopts it (divergence handled normally); otherwise 'airskills rm <name>' discards the local copy:\n",
				yellow("marked transferred"), needTombstoned)
			for _, n := range tombstoned {
				fmt.Fprintf(os.Stderr, "    %s\n", n)
			}
		}
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
			steps = append(steps, agentNextStep{Cmd: "airskills pull --keep-local <name>", Why: "keep your local copy of a same-named server skill and stop the conflict (or 'pull --force <name>' to take the server's; sync alone won't clear it)"})
		}
		if pendingSuggestions > 0 {
			steps = append(steps, agentNextStep{Cmd: "airskills review", Why: "review incoming suggestions"})
		}
		if needPendingConflicts > 0 {
			steps = append(steps, agentNextStep{Cmd: "airskills rm <name> --pending", Why: "discard a parked pending-conflict copy (safe: never deletes the installed skill); sync does not clear these"})
		}
		if needTombstoned > 0 {
			steps = append(steps, agentNextStep{Cmd: "airskills sync", Why: "re-adopt a skill marked transferred but back in your skillset (divergence handled normally)"})
		}
		if needForks > 0 {
			steps = append(steps, agentNextStep{Cmd: "airskills sync", Why: "after diffing and merging each local fork's copies to match — sync/push refuse forks until the copies agree"})
		}
		printAgentNextSteps(os.Stderr, steps)
	}

	return nil
}

// dropNames filters out of names every entry present in the exclude set.
func dropNames(names []string, exclude map[string]bool) []string {
	if len(exclude) == 0 {
		return names
	}
	out := names[:0]
	for _, n := range names {
		if !exclude[n] {
			out = append(out, n)
		}
	}
	return out
}

// printLocalForkStatusGroup details each forked skill with the same diagnosis
// and guidance push/sync give (printMirrorConflicts), so status stops
// implying a plain sync will clear it.
func printLocalForkStatusGroup(w io.Writer, forks []mirrorConflict) {
	if len(forks) == 0 {
		return
	}
	fmt.Fprintf(w, "  %s (%d): edited differently in two agent copies — sync and push leave these untouched until the copies match:\n", yellow("local forks"), len(forks))
	for _, c := range forks {
		fmt.Fprintf(w, "    %s\n", c.slug)
		for _, p := range c.paths {
			fmt.Fprintf(w, "      %s\n", p)
		}
	}
	fmt.Fprintf(w, "  Reconcile the copies — edit them to match (the version you want wins), then re-run 'airskills sync'.\n")
}

// dropPendingConflictDuplicates removes from pending any name that already
// appears in one of the shownElsewhere lists (e.g. the untracked or toUpdate
// buckets), so a single conflicting skill isn't reported as both a live
// classification and a parked "pending conflict" copy.
func dropPendingConflictDuplicates(pending []string, shownElsewhere ...[]string) []string {
	if len(pending) == 0 {
		return pending
	}
	seen := map[string]bool{}
	for _, list := range shownElsewhere {
		for _, n := range list {
			seen[n] = true
		}
	}
	out := pending[:0]
	for _, n := range pending {
		if !seen[n] {
			out = append(out, n)
		}
	}
	return out
}

func printPendingConflictStatusGroup(w io.Writer, names []string) {
	if len(names) == 0 {
		return
	}
	fmt.Fprintf(w, "  %s (%d): an incoming skill was parked because a local skill of the same name exists. 'airskills sync' will NOT clear these — resolve each:\n", yellow("pending conflicts"), len(names))
	for _, n := range names {
		fmt.Fprintf(w, "    %s\n", n)
		if dirs := pendingConflictDirs(n); len(dirs) > 0 {
			fmt.Fprintf(w, "      incoming copy: %s\n", dirs[0])
		}
		fmt.Fprintf(w, "      • take incoming, overwrite local:  airskills pull --force %s   (or: airskills add <owner>/%s --force)\n", n, n)
		fmt.Fprintf(w, "      • keep both:                        airskills add <owner>/%s --as <new-name>\n", n)
		fmt.Fprintf(w, "      • merge wanted bits into local (or keep local as-is), then discard the copy:  airskills rm %s --pending\n", n)
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
	// tombstoned: local dir present but its marker is a transfer tombstone
	// (Deleted/MovedTo). Previously skipped silently, which let a wrongly
	// tombstoned skill hide its local edits from status/push/list while diff
	// still saw them. Surface it so it's never invisible. See
	// cli-org-skill-wrongly-tombstoned-hides-edits.md.
	tombstoned []string
}

// projectStatusBuckets groups the unified classifier's rows into the status
// command's display buckets. It is the projection the spec calls for: one
// classifier (classifySkills / decideState), and every command — list,
// doctor, status — reads from it, so they can no longer disagree. Pure: the
// rows already carry every fact (presence + the divergence booleans).
//
//   - toPull:          available (remote, never installed here)
//   - toPush:          tracked with local edits; a brand-new untracked local;
//                      or an orphaned tracked skill the server dropped
//   - toUpdate:        tracked, another of my machines pushed (RemoteMoved)
//   - upstream:        a fork whose parent moved past upstream_base (forks only)
//   - untracked:       a same-named server skill collides with an unmarked
//                      local (adoptable or conflict)
//   - inOtherSkillset: orthogonal skillset membership — an owned skill not in
//                      the active skillset, surfaced via ownedElsewhereByName
//                      so it isn't mislabelled "to push"
//   - tombstoned:      displaced (marker is a transfer tombstone)
//
// A tracked-but-locally-deleted skill (available WITH a marker) is
// deliberately silent — you removed it on purpose; status does not nag.
func projectStatusBuckets(rows []SkillStateInfo, ownedElsewhereByName map[string]bool) statusBuckets {
	var b statusBuckets
	for _, r := range rows {
		switch r.State {
		case StateAvailable:
			// Never-installed remote → pull. A tracked skill whose local dir
			// was deleted also reads as available, but carries a marker —
			// stay silent on those, matching prior behaviour.
			if r.Marker == nil {
				b.toPull = append(b.toPull, r.Name)
			}
		case StateTracked:
			if r.LocalDirty {
				b.toPush = append(b.toPush, r.Name)
			}
			if r.RemoteMoved {
				b.toUpdate = append(b.toUpdate, r.Name)
			}
			if r.UpstreamMoved {
				b.upstream = append(b.upstream, r.Name)
			}
		case StateAdoptable, StateConflict:
			b.untracked = append(b.untracked, r.Name)
		case StateUntracked:
			b.toPush = append(b.toPush, r.Name)
		case StateOrphaned:
			// Marker but no remote: a never-pushed new skill, or one the
			// server dropped. Unless the caller owns it in another skillset
			// (then it's not really gone — it's just out of the active set).
			if ownedElsewhereByName[r.Name] {
				b.inOtherSkillset = append(b.inOtherSkillset, r.Name)
			} else {
				b.toPush = append(b.toPush, r.Name)
			}
		case StateDisplaced:
			b.tombstoned = append(b.tombstoned, r.Name)
		}
	}

	sort.Strings(b.toPush)
	sort.Strings(b.toPull)
	sort.Strings(b.toUpdate)
	sort.Strings(b.upstream)
	sort.Strings(b.untracked)
	sort.Strings(b.inOtherSkillset)
	sort.Strings(b.tombstoned)
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
