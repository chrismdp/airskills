package cmd

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chrismdp/airskills/config"
	"github.com/chrismdp/airskills/telemetry"
	"github.com/spf13/cobra"
)

var pullForceFlag bool
var pullKeepLocalFlag bool
var pullVersionFlag string

func init() {
	pullCmd.Flags().StringVar(&skillsetFlag, "skillset", "", "Personal skillset to pull against; sets the default for future runs (default: your last-used skillset)")
	pullCmd.Flags().BoolVar(&pullForceFlag, "force", false, "Overwrite local with remote for diverged skills (backs up local first)")
	pullCmd.Flags().BoolVar(&pullKeepLocalFlag, "keep-local", false, "Keep your local copy of a conflicting skill and track it against the server version (stops the conflict warning; never overwrites your files)")
	pullCmd.Flags().StringVar(&pullVersionFlag, "version", "", "Restore a specific commit; use with exactly one skill: airskills pull --version <commit-hash> <skill>")
	rootCmd.AddCommand(pullCmd)
}

var pullCmd = &cobra.Command{
	Use:   "pull",
	Short: "Download remote skills not on this machine, and update changed ones",
	Long: `Pulls skills from your airskills.ai account that aren't installed locally or have been updated remotely. If both local and remote changed, saves the remote version for merge.

--force tells the CLI "remote wins, overwrite my local" for diverged skills
(your local copy is backed up first). Use it after you've reviewed the
divergence and decided the remote copy is the truth. The mirror flag is
'push --force', which means "my local wins, take it as-is". Both flags
express the same intent — "I'm about to overwrite the other side, do it
anyway" — pointed in opposite directions.

--keep-local is the opposite resolution: keep your local copy as-is and
track it against the server version, so the conflict stops recurring. Your
files are never touched. Use it when your local copy is the one you want and
you don't need the server's bytes.`,
	Args: validatePullArgs,
	RunE: runPull,
}

func validatePullArgs(cmd *cobra.Command, args []string) error {
	if pullForceFlag || pullKeepLocalFlag {
		return nil
	}
	if pullVersionFlag != "" {
		return cobra.ExactArgs(1)(cmd, args)
	}
	return cobra.NoArgs(cmd, args)
}

type conflictDetail struct {
	name      string
	localDir  string
	remoteDir string
	// kind discriminates "diverged" (tracked, both sides changed) from
	// "untracked" (no marker, server has same name with different bytes).
	// Drives the headline wording in conflictResolutionMessage.
	kind string
	// orgSlug is set when the conflicting server skill is an org skill.
	// Drives the keep-local warning: resolving with --keep-local forks the
	// org skill into your personal namespace rather than updating it, which
	// is wrong if you administer the org. Empty for personal skills.
	orgSlug string
}

type updateDetail struct {
	name       string
	oldVersion string
	newVersion string
	messages   []string
}

type pullEntry struct {
	skill    apiSkill
	reason   string // "new", "updated", "diverged", "auto-resolved", "linked", "untracked-conflict"
	localDir string
	marker   *SyncEntry
	// reAdopt is set when the marker is a stale transfer tombstone
	// (Deleted/MovedTo) whose skill_id is back in the caller's listing —
	// e.g. the skill was re-added to a skillset. The action handler clears
	// the tombstone so the skill is tracked normally again; divergence (if
	// any) is surfaced by the normal rules. See
	// cli-org-skill-wrongly-tombstoned-hides-edits.md.
	reAdopt bool
}

type incomingDetail struct {
	name          string
	upstreamOwner string
	upstreamSlug  string
}

func runPull(cmd *cobra.Command, args []string) error {
	if pullForceFlag && pullKeepLocalFlag {
		return fmt.Errorf("--force and --keep-local are opposite resolutions (take remote vs keep local) — choose one")
	}
	if pullForceFlag {
		return runPullForce(cmd, args)
	}
	if pullKeepLocalFlag {
		return runPullKeepLocal(cmd, args)
	}
	if pullVersionFlag != "" {
		return runPullVersion(cmd, args)
	}

	client, err := newAPIClientAuto()
	loggedIn := err == nil

	syncState := loadSyncState()

	// Resolve partial renames BEFORE mirror. A manual `mv` in one agent
	// dir leaves the same skill living under two names; if mirror runs
	// first, it cross-pollinates both names across every agent and the
	// rename signal is lost. Mirror itself then propagates edits across
	// agent dirs and surfaces a hint when it refills a previously-empty
	// dir (sync restores hand-`rm`d folders by design — see
	// platform/doc/changes/cli-mirror-cannot-distinguish-delete-from-never-installed.md).
	//
	// Pull populates syncActiveConflicts when invoked from `sync` so the
	// push step that follows can skip both mirror (already run) and any
	// upload that would collide with a divergence pull detected below.
	if scanned, scanErr := scanSkillsFromAgents(); scanErr == nil {
		propagatePartialRenames(scanned, syncState)
	}
	mirrorConflictSet := map[string]bool{}
	_, mirrorConflicts, restoreHints := mirrorLocalSkills(syncState)
	printMirrorConflicts(mirrorConflicts)
	printMirrorRestoreHints(restoreHints)
	for _, c := range mirrorConflicts {
		mirrorConflictSet[c.slug] = true
	}
	if syncActiveConflicts != nil {
		for slug := range mirrorConflictSet {
			syncActiveConflicts[slug] = true
		}
	}

	localSkills, err := scanSkillsFromAgents()
	if err != nil {
		return err
	}
	for slug := range mirrorConflictSet {
		delete(localSkills, slug)
	}

	// If not logged in, pull sourced skills (from add) by re-downloading from source
	if !loggedIn {
		return runPullAnon(localSkills, syncState, mirrorConflictSet)
	}

	// A transfer tombstone (Deleted + MovedTo, empty skill_id) is left behind
	// on a machine that transferred a skill with an older CLI, or was offline
	// when another machine did. It carries no skill_id, so decidePullActions
	// can't match it to a tracked skill and treats the still-present local dir
	// exactly like an untracked one: bytes matching the new org copy → relinked
	// silently ("linked"); diverged → surfaced as an "untracked-conflict" via
	// the normal conflict UX. No bespoke repair pass needed.

	// Fetch the caller's skills scoped to their selected personal skillset
	// (and any org skillsets they've been assigned to). Empty slug =>
	// server resolves to their is_default=true skillset.
	cfg, cfgErr := config.Load()
	if cfgErr != nil {
		return cfgErr
	}
	sendSlug, err := resolveSkillsetFlag(cfg, skillsetFlag, stdinReader(), stderrWriter())
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return err
	}
	remoteSkills, resolvedSlug, err := client.listPersonalSkillsInSkillset(sendSlug)
	if err != nil {
		if notFound, ok := err.(*SkillsetNotFoundError); ok {
			fmt.Fprintln(os.Stderr, notFound.Error())
			return err
		}
		return fmt.Errorf("fetching skills: %w", err)
	}
	rememberSkillsetAfterSuccess(cfg, resolvedSlug)

	// Migration 047: filter shadowed skills out of the pull set and
	// emit a warning naming the winning org. Fires on every pull until
	// the user resolves with `airskills mv`.
	shadowMap := client.fetchShadowMap()
	if len(shadowMap) > 0 {
		filtered := remoteSkills[:0]
		for _, s := range remoteSkills {
			info, isShadow := shadowMap[s.Id.String()]
			if !isShadow {
				filtered = append(filtered, s)
				continue
			}
			location := info.OrgSlug
			if location == "" {
				location = "another skill"
			} else {
				location = location + "/" + info.Slug
			}
			fmt.Fprintf(os.Stderr, "  %s %s shadowed by %s\n", yellow("⚠"), info.Slug, location)
			fmt.Fprintf(os.Stderr, "    run `airskills mv %s <new-name>` to keep your version\n", info.Slug)
		}
		remoteSkills = filtered
	}

	movedSourceNotices := collectMovedSourceNotices(client, syncState, remoteSkills)

	owners := newOwnerResolver(client)

	toPull, missingWarnings, divergedSlugs := decidePullActions(remoteSkills, localSkills, syncState)

	// Register pull-detected divergences in the shared sync conflict
	// set BEFORE the action loop. A download failure mid-loop must
	// still leave the slug flagged so the push step that follows in
	// `sync` skips it — otherwise a 3-way diverge would produce two
	// conflict dirs (one here, one from push's 409 path).
	if syncActiveConflicts != nil {
		for _, slug := range divergedSlugs {
			syncActiveConflicts[slug] = true
		}
	}

	// Drop any actions for slugs that have unresolved local divergence —
	// we already warned the user above, and we must not clobber their
	// in-progress copies with a remote install.
	if len(mirrorConflictSet) > 0 {
		filtered := toPull[:0]
		for _, p := range toPull {
			if mirrorConflictSet[p.skill.Name] {
				continue
			}
			filtered = append(filtered, p)
		}
		toPull = filtered
	}

	if len(toPull) == 0 && len(missingWarnings) == 0 {
		fmt.Printf("  %s all up to date\n", green("✓"))
		return nil
	}

	lines := make([]progressLine, len(toPull))
	for i, p := range toPull {
		lines[i] = progressLine{name: p.skill.Name, status: "waiting", pct: 0}
	}
	if verbose && isTTY {
		for _, l := range lines {
			fmt.Printf("  %-20s  %s  %s\n", l.name, renderBar(0), "waiting")
		}
	} else if isTTY && len(lines) > 0 {
		fmt.Printf("  %s %d skills\n", dim("·"), len(lines))
	}

	var pulled, updated, diverged, failed, autoResolved, incoming int
	var pulledNames []string
	var divergedDetails []conflictDetail
	var incomingDetails []incomingDetail
	var updateDetails []updateDetail

	for i, p := range toPull {
		// Auto-resolved: local already matches remote — update marker silently, no download.
		if p.reason == "auto-resolved" {
			if p.marker != nil {
				p.marker.ContentHash = strDeref(p.skill.ContentHash)
				p.marker.Version = p.skill.Version
				markSourceUpstreamIncorporated(p.marker, p.skill)
				// Backfill Source for non-owned markers written by pre-fix
				// CLI versions: the next pull will refresh it. Leave existing
				// Source alone if it's already populated.
				if p.marker.Source == nil {
					p.marker.Source = owners.sourceFor(&p.skill)
				}
				if p.reAdopt {
					ownerKind, ownerSlug := owners.resolve(&p.skill)
					clearTransferTombstone(p.marker, p.skill.Version, ownerKind, ownerSlug)
				}
			}
			autoResolved++
			if verbose {
				fmt.Printf("  %s %s  %s\n", dim("-"), p.skill.Name, dim("auto-resolved (bytes match)"))
			}
			lines[i].status = "done"
			lines[i].pct = 1
			renderProgress(lines)
			continue
		}

		if p.reason == "upstream-advanced" {
			incoming++
			incomingDetails = append(incomingDetails, incomingDetail{
				name:          p.skill.Name,
				upstreamOwner: p.marker.Source.Owner,
				upstreamSlug:  p.marker.Source.Slug,
			})
			lines[i].status = "incoming"
			lines[i].pct = 1
			renderProgress(lines)
			continue
		}

		// Linked: an untracked local dir whose bytes match the server's
		// copy. Write the marker silently — no download, no install. The
		// classifier on the next sync will see this as plain "synced".
		if p.reason == "linked" {
			dirName := filepath.Base(p.localDir)
			ownerKind, ownerSlug := owners.resolve(&p.skill)
			syncState.Skills[dirName] = &SyncEntry{
				SkillID:     p.skill.Id.String(),
				Version:     p.skill.Version,
				ContentHash: strDeref(p.skill.ContentHash),
				Tool:        "claude-code",
				OwnerKind:   ownerKind,
				OwnerSlug:   ownerSlug,
				Source:      owners.sourceFor(&p.skill),
			}
			autoResolved++
			fmt.Printf("  %s %s %s\n", green("·"), p.skill.Name, dim("linked (bytes match server, no download)"))
			lines[i].status = "linked"
			lines[i].pct = 1
			renderProgress(lines)
			continue
		}

		// Diverged / untracked-conflict: never overwrite local. Park the
		// remote copy for review and warn — but idempotently. Only
		// (re)download and re-park when the remote actually changed since
		// the last park; otherwise reuse the existing copy so the warning
		// recurs without piling up duplicates or re-downloading. Sweep
		// stale copies first so old versions don't linger.
		if p.reason == "diverged" || p.reason == "untracked-conflict" {
			// Re-adoption: a tombstoned marker whose skill is back in the
			// listing but whose local copy diverges. Clear the tombstone so
			// the skill is tracked again; the divergence is still surfaced
			// below by the normal conflict UX rather than silently shadowed.
			if p.reAdopt && p.marker != nil {
				ownerKind, ownerSlug := owners.resolve(&p.skill)
				clearTransferTombstone(p.marker, p.skill.Version, ownerKind, ownerSlug)
			}
			remoteHash := strDeref(p.skill.ContentHash)
			parkDir, needWrite := conflictNeedsRepark(p.skill.Name, remoteHash)
			if needWrite {
				_, _ = removePendingConflictDirs(p.skill.Name)
				files, err := downloadSkillFiles(client, p.skill.Id.String())
				if err != nil || len(files) == 0 {
					lines[i].status = "failed"
					renderProgress(lines)
					failed++
					continue
				}
				os.MkdirAll(parkDir, 0755)
				_ = writeFilesToDir(parkDir, files)
			}

			if p.reason == "untracked-conflict" {
				lines[i].status = "UNTRACKED-CONFLICT"
			} else {
				lines[i].status = "DIVERGED"
			}
			lines[i].pct = 1
			renderProgress(lines)
			diverged++
			kind := "tracked"
			if p.reason == "untracked-conflict" {
				kind = "untracked"
			}
			orgSlug := ""
			if ok, slug := owners.resolve(&p.skill); ok == "org" {
				orgSlug = slug
				if orgSlug == "" {
					orgSlug = "org"
				}
			}
			divergedDetails = append(divergedDetails, conflictDetail{
				name:      p.skill.Name,
				localDir:  p.localDir,
				remoteDir: parkDir,
				kind:      kind,
				orgSlug:   orgSlug,
			})
			continue
		}

		lines[i].status = "downloading"
		lines[i].pct = 0.5
		renderProgress(lines)

		if p.reason == "upstream-updated" && p.marker != nil && p.marker.Source != nil && p.skill.Id.String() != p.marker.Source.ID {
			if advanced, err := client.pullUpstream(p.skill.Id.String()); err == nil {
				p.skill = *advanced
			} else {
				lines[i].status = "failed"
				renderProgress(lines)
				failed++
				continue
			}
		}

		files, err := downloadSkillFiles(client, p.skill.Id.String())
		if err != nil || len(files) == 0 {
			lines[i].status = "failed"
			renderProgress(lines)
			failed++
			continue
		}

		lines[i].status = "installing"
		lines[i].pct = 0.8
		renderProgress(lines)

		// Use the existing local dir name when updating a tracked skill so
		// that namespaced dirs (e.g. "chrismdp-my-skill") are preserved
		// rather than silently reinstalling under the bare API name.
		dirName := p.skill.Name
		if p.localDir != "" {
			dirName = filepath.Base(p.localDir)
		}

		destinations, err := installSkillToAgents(dirName, files)
		if err != nil {
			lines[i].status = "failed"
			renderProgress(lines)
			failed++
			continue
		}
		ownerKind, ownerSlug := owners.resolve(&p.skill)
		syncState.Skills[dirName] = &SyncEntry{
			SkillID:     p.skill.Id.String(),
			Version:     p.skill.Version,
			ContentHash: strDeref(p.skill.ContentHash),
			Tool:        "claude-code",
			OwnerKind:   ownerKind,
			OwnerSlug:   ownerSlug,
			Source:      owners.sourceFor(&p.skill),
		}
		if p.reason == "upstream-updated" && p.marker != nil && p.marker.Source != nil {
			syncState.Skills[dirName].Source = p.marker.Source
			markSourceUpstreamIncorporated(syncState.Skills[dirName], p.skill)
		}

		if p.reason == "updated" || p.reason == "upstream-updated" {
			// Collect update info for summary
			oldVersion := ""
			if p.marker != nil {
				oldVersion = p.marker.Version
			}
			detail := updateDetail{
				name:       p.skill.Name,
				oldVersion: oldVersion,
				newVersion: p.skill.Version,
			}
			if p.reason == "upstream-updated" {
				detail.messages = append(detail.messages, "incorporated upstream changes")
			}

			// Fetch commit messages since last known version
			commits, err := client.getVersionHistory(p.skill.Id.String())
			if err == nil {
				for _, c := range commits {
					if msg := strDeref(c.Message); msg != "" {
						detail.messages = append(detail.messages, msg)
					}
				}
			}

			lines[i].status = "done"
			lines[i].size = fmt.Sprintf("%s → %s", oldVersion, p.skill.Version)
			updated++
			updateDetails = append(updateDetails, detail)
		} else {
			lines[i].status = "done"
			lines[i].size = fmt.Sprintf("%d agents", len(destinations))
			pulled++
			pulledNames = append(pulledNames, p.skill.Name)
		}
		lines[i].pct = 1
		renderProgress(lines)
	}

	if pulled > 0 {
		for _, n := range pulledNames {
			fmt.Printf("  %s %s\n", green("+"), n)
		}
	}
	if updated > 0 {
		for _, u := range updateDetails {
			fmt.Printf("  %s %s %s → %s\n", cyan("↓"), u.name, u.oldVersion, u.newVersion)
		}
	}
	fmt.Printf("\n%d pulled, %d updated, %d incoming, %d diverged, %d auto-resolved, %d failed", pulled, updated, incoming, diverged, autoResolved, failed)
	if len(missingWarnings) > 0 {
		fmt.Printf(", %d missing locally", len(missingWarnings))
	}
	fmt.Println()

	if len(missingWarnings) > 0 {
		fmt.Println("\n--- Missing locally ---")
		for _, w := range missingWarnings {
			fmt.Printf("  %s %s\n", yellow("!"), w)
		}
	}
	if len(movedSourceNotices) > 0 {
		fmt.Println("\n--- Transferred upstreams ---")
		printMovedSourceNotices(movedSourceNotices)
	}

	if len(divergedDetails) > 0 {
		var entries []conflictEntry
		for _, d := range divergedDetails {
			var source *skillSource
			if entry, ok := syncState.Skills[d.name]; ok {
				source = entry.Source
			}
			entries = append(entries, conflictEntry{
				name:      d.name,
				localDir:  d.localDir,
				remoteDir: d.remoteDir,
				source:    source,
				kind:      d.kind,
				orgSlug:   d.orgSlug,
			})
		}
		fmt.Print(conflictResolutionMessage(entries, !isTTY))
	}
	if len(incomingDetails) > 0 {
		fmt.Println("\n--- Incoming upstream changes ---")
		for _, d := range incomingDetails {
			source := d.upstreamOwner + "/" + d.upstreamSlug
			if d.upstreamOwner == "" {
				source = d.upstreamSlug
			}
			fmt.Printf("  %s %s: upstream %s advanced — take it with: airskills add %s --force\n",
				yellow("!"), d.name, source, source)
		}
	}

	notifyResolvedSuggestions(client, syncState)

	saveSyncState(syncState)
	_ = saveLastSync()

	// Run broken-ref walker after pull so newly-transferred skills are flagged.
	if brokenIssues, err := walkBrokenRefs(); err == nil && len(brokenIssues) > 0 {
		fmt.Fprintf(os.Stderr, "\n%s %d broken ref(s) found. Run 'airskills doctor' for details.\n",
			yellow("!"), len(brokenIssues))
	}

	telemetry.Capture("cli_pull", map[string]interface{}{
		"pulled":        pulled,
		"updated":       updated,
		"diverged":      diverged,
		"auto_resolved": autoResolved,
		"force":         0,
		"version":       0,
		"failed":        failed,
		"missing":       len(missingWarnings),
		"anonymous":     false,
	})

	// Next-step hints for an agent. Skip when called from `sync` — sync
	// prints its own consolidated block after pull.
	if cmd.Name() != "sync" {
		steps := []agentNextStep{
			{Cmd: "airskills status", Why: "confirm local matches remote"},
		}
		if diverged > 0 {
			steps = []agentNextStep{
				{Cmd: "airskills push --force", Why: "re-push after merging the diverged skills above"},
			}
		}
		printAgentNextSteps(os.Stdout, steps)
	}
	return nil
}

// runPullAnon pulls sourced skills without authentication by re-downloading from the original source.
func runPullAnon(localSkills map[string]string, syncState *SyncState, skipSlugs map[string]bool) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	var pulled int
	var pulledNames []string

	for name, entry := range syncState.Skills {
		if entry.Source == nil {
			continue
		}
		if skipSlugs[name] {
			continue
		}

		// Skip if local content matches what we last synced
		if dir, exists := localSkills[name]; exists && entry.ContentHash != "" {
			localHash := computeMerkleHash(readSkillFiles(dir))
			if localHash == entry.ContentHash {
				continue
			}
		}

		// Resolve the skill from its source
		resolveURL := fmt.Sprintf("%s/api/v1/resolve/%s/%s", cfg.APIURL, entry.Source.Owner, entry.Source.Slug)
		resolveReq, _ := http.NewRequest("GET", resolveURL, nil)
		setStandardHeaders(resolveReq)
		resp, err := http.DefaultClient.Do(resolveReq)
		if err != nil {
			continue
		}
		var result struct {
			ID      string `json:"id"`
			Content string `json:"content"`
			Version string `json:"version"`
		}
		decodeErr := json.NewDecoder(resp.Body).Decode(&result)
		resp.Body.Close()
		if decodeErr != nil || result.ID == "" {
			continue
		}

		// Download files using shared helper
		files, err := downloadSkillByID(cfg.APIURL, result.ID, result.Content, "")
		if err != nil || len(files) == 0 {
			continue
		}

		if _, err := installSkillToAgents(name, files); err != nil {
			continue
		}

		// Update sync state so next pull can skip unchanged
		entry.Version = result.Version
		entry.ContentHash = computeMerkleHash(files)
		pulled++
		pulledNames = append(pulledNames, name)
	}

	if pulled > 0 {
		for _, n := range pulledNames {
			fmt.Printf("  %s %s\n", green("+"), n)
		}
		fmt.Printf("\n%d pulled\n", pulled)
	} else {
		fmt.Printf("  %s all up to date\n", green("✓"))
	}

	saveSyncState(syncState)

	telemetry.Capture("cli_pull", map[string]interface{}{
		"pulled":    pulled,
		"anonymous": true,
	})
	return nil
}

// notifyResolvedSuggestions shows a one-time accept/decline notification for
// each suggestion reviewed since the last time we printed. State is a single
// cutoff timestamp on syncState so the list doesn't grow unbounded.
func notifyResolvedSuggestions(client *apiClient, syncState *SyncState) {
	suggestions, err := client.listSuggestions("suggester", "", "")
	if err != nil {
		return
	}
	cutoff := syncState.LastSuggestionNotifyAt
	var newest string
	var shown bool
	for _, s := range suggestions {
		if string(s.Status) == "pending" || s.ReviewedAt == nil {
			continue
		}
		reviewed := s.ReviewedAt.Format(time.RFC3339)
		if cutoff != "" && reviewed <= cutoff {
			continue
		}
		if !shown {
			fmt.Println()
			fmt.Println("--- Suggestions ---")
			shown = true
		}
		skillName := strDeref(s.OwnerSkillName)
		if skillName == "" {
			skillName = s.OwnerSkillId.String()
		}
		responseMsg := strDeref(s.ResponseMessage)
		switch string(s.Status) {
		case "accepted":
			fmt.Printf("  %s your suggestion for %q was accepted\n", green("✓"), skillName)
		case "declined":
			if responseMsg != "" {
				fmt.Printf("  %s your suggestion for %q was declined: %q\n",
					yellow("✗"), skillName, responseMsg)
			} else {
				fmt.Printf("  %s your suggestion for %q was declined\n", yellow("✗"), skillName)
			}
		}
		if reviewed > newest {
			newest = reviewed
		}
	}
	if newest != "" {
		syncState.LastSuggestionNotifyAt = newest
	}
}

func sourceBaselineHash(source *skillSource) string {
	if source == nil {
		return ""
	}
	if source.UpstreamContentHash != "" {
		return source.UpstreamContentHash
	}
	return source.ContentHash
}

func sourceUpstreamID(source *skillSource) string {
	if source == nil {
		return ""
	}
	if source.UpstreamSkillID != "" {
		return source.UpstreamSkillID
	}
	return source.ID
}

func currentUpstreamHash(marker *SyncEntry, remote apiSkill) string {
	if marker == nil || marker.Source == nil {
		return ""
	}
	if remote.UpstreamContentHash != nil && *remote.UpstreamContentHash != "" {
		return *remote.UpstreamContentHash
	}
	if remote.Id.String() == sourceUpstreamID(marker.Source) {
		return strDeref(remote.ContentHash)
	}
	return ""
}

func upstreamAdvanced(marker *SyncEntry, remote apiSkill) bool {
	baseline := sourceBaselineHash(marker.Source)
	current := currentUpstreamHash(marker, remote)
	return baseline != "" && current != "" && baseline != current
}

func markSourceUpstreamIncorporated(marker *SyncEntry, remote apiSkill) {
	if marker == nil || marker.Source == nil {
		return
	}
	hash := currentUpstreamHash(marker, remote)
	if hash == "" {
		hash = strDeref(remote.ContentHash)
	}
	marker.Source.UpstreamSkillID = sourceUpstreamID(marker.Source)
	if marker.Source.UpstreamSkillID == "" {
		marker.Source.UpstreamSkillID = remote.Id.String()
	}
	marker.Source.UpstreamContentHash = hash
	marker.Source.UpstreamVersion = remote.Version
	marker.Source.ContentHash = hash
}

// decidePullActions inspects remote, local, and sync state to decide which
// remote skills to download. It is the pure decision core of runPull, with
// no network calls. The hashLocal helper reads disk for divergence checks.
//
// Behaviour:
//   - tracked (skill_id matches), name differs: same skill, server-side
//     rename / owner normalisation — handled in place by the rules below,
//     dir kept stable, never tombstoned
//   - tracked + stale transfer tombstone (Deleted/MovedTo) but skill_id back
//     in the listing: re-adopt (clear tombstone), then apply the rules below
//   - tracked + local present + remote unchanged: skip
//   - tracked + local present + only remote changed: "updated"
//   - tracked + local present + both changed: "diverged"
//   - tracked + local missing: warn and skip (treat as intentional removal —
//     user should run 'airskills rm <name>' to delete server-side, or
//     'airskills pull <name>' to restore); re-adopted tombstone reinstalls "new"
//   - untracked + local with same name + bytes match: "linked" (silent claim)
//   - untracked + local with same name + bytes differ: "untracked-conflict"
//   - untracked + no local: "new"
//
// The third return value is the set of slugs classified as `diverged` or
// `untracked-conflict`. Callers in a `sync` run register these in
// syncActiveConflicts BEFORE the action loop so a download failure later
// does not leave the slug eligible for push to upload over.
func decidePullActions(remoteSkills []apiSkill, localSkills map[string]string, syncState *SyncState) ([]pullEntry, []string, []string) {
	var divergedSlugs []string
	skillIdToName := map[string]string{}
	// Upstream skill IDs that are already represented by a local fork.
	// After cli-org-member-suggest-via-shadow-fork.md, push may have
	// forked an org-member skill into the caller's namespace and
	// suggested; the local dir is now tracked to the fork (SkillID),
	// not the upstream (Source.ID). The upstream still shows up in the
	// caller's effective skillset listing — we want to skip it, not
	// re-install it as a duplicate or flag it as "untracked-conflict"
	// against the fork's bytes. The "upstream changed, want to
	// incorporate?" UX is a separate ticket
	// (cli-non-owned-skills-incorporate-upstream-changes.md).
	forkedUpstreamIDs := map[string]bool{}
	for name, entry := range syncState.Skills {
		if entry.SkillID != "" {
			skillIdToName[entry.SkillID] = name
		}
		if entry.Source != nil && entry.SkillID != "" && entry.SkillID != entry.Source.ID {
			forkedUpstreamIDs[entry.Source.ID] = true
		}
	}

	var actions []pullEntry
	var warnings []string

	for _, remote := range remoteSkills {
		trackedName := ""
		if name, ok := skillIdToName[remote.Id.String()]; ok {
			trackedName = name
		}
		if forkedUpstreamIDs[remote.Id.String()] && trackedName == "" {
			continue
		}

		if trackedName != "" {
			marker := syncState.Skills[trackedName]

			// A matching skill_id means this is the SAME skill the caller
			// still receives. Two cases used to be mishandled here:
			//
			//   - remote.Name != trackedName: a server-side rename or
			//     owner-namespace normalisation. This is NOT a transfer
			//     away — the skill is still in the listing. We keep the
			//     existing local dir (per the no-rename-on-ownership-change
			//     rule in marker_resolve.go) and reconcile in place by the
			//     normal rules below, never tombstoning.
			//   - marker is a stale transfer tombstone (Deleted/MovedTo) but
			//     the skill_id is back in the listing (re-added to a
			//     skillset). Re-adopt it: clear the tombstone and apply the
			//     normal rules; divergence is surfaced like any other.
			//
			// Genuine v2 transfers mint a NEW skill_id and soft-delete the
			// old, so the old id never reappears in the listing — a matching
			// id is, by construction, not a move-out.
			// See cli-org-skill-wrongly-tombstoned-hides-edits.md.
			reAdopt := marker != nil && (marker.Deleted || marker.MovedTo != "")

			localDir, exists := localSkills[trackedName]
			if !exists {
				if reAdopt {
					// Skill back in the listing, local dir gone — reinstall
					// fresh under its current name and re-track it.
					actions = append(actions, pullEntry{skill: remote, reason: "new", reAdopt: true})
					continue
				}
				warnings = append(warnings, fmt.Sprintf(
					"%s: tracked but missing locally — run 'airskills rm %s' to delete server-side, or 'airskills pull %s' to restore",
					trackedName, trackedName, trackedName,
				))
				continue
			}

			if marker.Source != nil && upstreamAdvanced(marker, remote) {
				localFiles := readSkillFiles(localDir)
				localHash := computeMerkleHash(localFiles)
				reason := "upstream-advanced"
				if localHash == marker.ContentHash {
					reason = "upstream-updated"
				}
				actions = append(actions, pullEntry{skill: remote, reason: reason, localDir: localDir, marker: marker, reAdopt: reAdopt})
				continue
			}
			remoteHash := strDeref(remote.ContentHash)
			// Synced and not tombstoned → nothing to do. A re-adoption must
			// still clear the tombstone even when bytes already match, so it
			// falls through to the classification below.
			if !reAdopt && (remoteHash == "" || marker.ContentHash == "" || remoteHash == marker.ContentHash) {
				continue
			}

			localFiles := readSkillFiles(localDir)
			localHash := computeMerkleHash(localFiles)

			switch {
			case remoteHash != "" && localHash == remoteHash:
				// Auto-detect: local already matches remote bytes.
				// Marker is stale from manual reconciliation — update silently.
				actions = append(actions, pullEntry{skill: remote, reason: "auto-resolved", localDir: localDir, marker: marker, reAdopt: reAdopt})
			case marker.ContentHash != "" && localHash == marker.ContentHash:
				actions = append(actions, pullEntry{skill: remote, reason: "updated", localDir: localDir, marker: marker, reAdopt: reAdopt})
			default:
				actions = append(actions, pullEntry{skill: remote, reason: "diverged", localDir: localDir, marker: marker, reAdopt: reAdopt})
				divergedSlugs = append(divergedSlugs, remote.Name)
			}
			continue
		}

		if localDir, exists := localSkills[remote.Name]; exists {
			// Untracked local dir whose name matches a server skill. The
			// classifier vocabulary calls this either "linked" (bytes
			// match → silent claim on next sync) or "untracked-conflict"
			// (bytes differ → surface via existing conflict UX).
			localFiles := readSkillFiles(localDir)
			localHash := computeMerkleHash(localFiles)
			remoteHash := strDeref(remote.ContentHash)
			if remoteHash != "" && localHash == remoteHash {
				actions = append(actions, pullEntry{
					skill: remote, reason: "linked", localDir: localDir,
				})
			} else {
				actions = append(actions, pullEntry{
					skill: remote, reason: "untracked-conflict", localDir: localDir,
				})
				divergedSlugs = append(divergedSlugs, remote.Name)
			}
			continue
		}

		actions = append(actions, pullEntry{skill: remote, reason: "new"})
	}

	return actions, warnings, divergedSlugs
}

// runPullForce implements `airskills pull --force [skill...]`.
// Downloads the remote version of diverged skills and overwrites local,
// backing up current local files to ~/.airskills/undo/<ts>/<skill>/<agent>/ first.
func runPullForce(cmd *cobra.Command, args []string) error {
	client, err := newAPIClientAuto()
	if err != nil {
		return fmt.Errorf("pull --force requires authentication: %w", err)
	}

	syncState := loadSyncState()
	// runPullForce deliberately does NOT call propagatePartialRenames —
	// force-pull means "discard my edits, take server's truth"; running
	// rename inference here could silently undo a deliberate user `mv`.
	_, mirrorConflicts, restoreHints := mirrorLocalSkills(syncState)
	printMirrorRestoreHints(restoreHints)
	mirrorConflictSet := map[string]bool{}
	for _, c := range mirrorConflicts {
		mirrorConflictSet[c.slug] = true
	}

	localSkills, err := scanSkillsFromAgents()
	if err != nil {
		return err
	}

	cfg, cfgErr := config.Load()
	if cfgErr != nil {
		return cfgErr
	}
	sendSlug, err := resolveSkillsetFlag(cfg, skillsetFlag, stdinReader(), stderrWriter())
	if err != nil {
		return err
	}
	remoteSkills, resolvedSlug, err := client.listPersonalSkillsInSkillset(sendSlug)
	if err != nil {
		return fmt.Errorf("fetching skills: %w", err)
	}
	rememberSkillsetAfterSuccess(cfg, resolvedSlug)

	toPull, _, _ := decidePullActions(remoteSkills, localSkills, syncState)
	divergedMap := map[string]pullEntry{}
	for _, p := range toPull {
		if p.reason == "diverged" || p.reason == "untracked-conflict" {
			divergedMap[p.skill.Name] = p
		}
	}

	var targets []pullEntry
	if len(args) > 0 {
		for _, name := range args {
			p, ok := divergedMap[name]
			if !ok {
				return fmt.Errorf("%s: not in conflict; nothing to force-pull. Use 'airskills sync' for normal updates.", name)
			}
			targets = append(targets, p)
		}
	} else {
		for _, p := range divergedMap {
			targets = append(targets, p)
		}
		if len(targets) == 0 {
			fmt.Printf("  %s no diverged skills to force-pull\n", dim("·"))
			return nil
		}
	}

	// Block if any target has unresolved mirror conflicts
	for _, p := range targets {
		if mirrorConflictSet[p.skill.Name] {
			return fmt.Errorf("%s: mirror conflict exists. Resolve mirror conflicts first, then retry.", p.skill.Name)
		}
	}

	// Single confirmation prompt
	names := make([]string, len(targets))
	for i, p := range targets {
		names[i] = p.skill.Name
	}
	fmt.Printf("Force-pull will overwrite local files for: %s\n", strings.Join(names, ", "))
	fmt.Print("Previous local files will be backed up to ~/.airskills/undo/. Continue? [y/N] ")
	reader := bufio.NewReader(os.Stdin)
	answer, _ := reader.ReadString('\n')
	if strings.TrimSpace(strings.ToLower(answer)) != "y" {
		fmt.Println("Aborted.")
		return nil
	}

	ts := time.Now().UTC().Format("20060102T150405Z")
	var forcePulled int

	for _, p := range targets {
		skillName := p.skill.Name
		if p.localDir != "" {
			skillName = filepath.Base(p.localDir)
		}

		// Backup all local copies before overwriting
		if _, err := backupSkillToUndo(skillName, ts); err != nil {
			return fmt.Errorf("%s: %w. No files modified. Resolve and retry.", skillName, err)
		}

		// Download remote files
		files, err := downloadSkillFiles(client, p.skill.Id.String())
		if err != nil || len(files) == 0 {
			fmt.Fprintf(os.Stderr, "  %s %s: download failed\n", yellow("!"), skillName)
			continue
		}

		// Overwrite all agent dirs
		if _, err := installSkillToAgents(skillName, files); err != nil {
			fmt.Fprintf(os.Stderr, "  %s %s: install failed: %v\n", yellow("!"), skillName, err)
			continue
		}

		// Update marker, preserving other fields (Source, etc.)
		marker := syncState.Skills[skillName]
		if marker == nil {
			marker = &SyncEntry{Tool: "claude-code"}
		}
		marker.SkillID = p.skill.Id.String()
		marker.ContentHash = strDeref(p.skill.ContentHash)
		marker.Version = p.skill.Version
		syncState.Skills[skillName] = marker
		forcePulled++
		fmt.Printf("  %s %s\n", cyan("↓"), skillName)
	}

	saveSyncState(syncState)

	if forcePulled > 0 {
		fmt.Printf("\n%d pulled with --force. Backups in ~/.airskills/undo/%s/\n", forcePulled, ts)
	}

	telemetry.Capture("cli_pull", map[string]interface{}{
		"pulled":        0,
		"updated":       0,
		"diverged":      0,
		"auto_resolved": 0,
		"force":         forcePulled,
		"version":       0,
		"failed":        0,
		"anonymous":     false,
	})

	return nil
}

// runPullVersion implements `airskills pull --version <commit-hash> <skill>`.
// Pulls a specific historical version of one skill, backing up local first.
func runPullVersion(cmd *cobra.Command, args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("pull --version requires exactly one skill name argument: airskills pull --version <commit-hash> <skill>")
	}
	skillName := args[0]
	commitID := pullVersionFlag

	if !isTTY {
		return fmt.Errorf("pull --version requires confirmation. Run interactively.")
	}

	client, err := newAPIClientAuto()
	if err != nil {
		return fmt.Errorf("pull --version requires authentication: %w", err)
	}

	syncState := loadSyncState()
	marker := syncState.Skills[skillName]
	if marker == nil || marker.SkillID == "" {
		return fmt.Errorf("%s: skill not tracked locally. Run 'airskills pull' first.", skillName)
	}

	// Shorten commit hash for display
	displayCommit := commitID
	if len(commitID) > 8 {
		displayCommit = commitID[:8] + "..."
	}
	fmt.Printf("Pull version %s for skill %s? Previous local files will be backed up. Continue? [y/N] ", displayCommit, skillName)
	reader := bufio.NewReader(os.Stdin)
	answer, _ := reader.ReadString('\n')
	if strings.TrimSpace(strings.ToLower(answer)) != "y" {
		fmt.Println("Aborted.")
		return nil
	}

	ts := time.Now().UTC().Format("20060102T150405Z")

	// Backup
	undoPath, err := backupSkillToUndo(skillName, ts)
	if err != nil {
		return fmt.Errorf("%s: %w. No files modified.", skillName, err)
	}

	// Download the specific commit via archive?commit=
	files, err := client.getVersionContent(marker.SkillID, commitID)
	if err != nil || len(files) == 0 {
		if undoPath != "" {
			os.RemoveAll(undoPath)
		}
		return fmt.Errorf("%s: failed to download version %s: %v", skillName, displayCommit, err)
	}

	// Overwrite all agent dirs
	if _, err := installSkillToAgents(skillName, files); err != nil {
		return fmt.Errorf("%s: install failed: %v", skillName, err)
	}

	// Update marker with the pulled commit's hash (computed from files)
	marker.ContentHash = computeMerkleHash(files)
	marker.Version = commitID
	syncState.Skills[skillName] = marker
	saveSyncState(syncState)

	fmt.Printf("  %s %s (version %s)\n", cyan("↓"), skillName, displayCommit)
	if undoPath != "" {
		fmt.Printf("  Previous local saved to %s/\n  Restore: cp -r %s/%s/ ~/.claude/skills/%s/\n",
			undoPath, undoPath, "claude-code", skillName)
	}

	telemetry.Capture("cli_pull", map[string]interface{}{
		"pulled":        0,
		"updated":       0,
		"diverged":      0,
		"auto_resolved": 0,
		"force":         0,
		"version":       1,
		"failed":        0,
		"anonymous":     false,
	})

	return nil
}

// backupSkillToUndo copies all installed copies of skillName to
// ~/.airskills/undo/<timestamp>/<skillName>/<agentKey>/ before a force operation.
// Returns the backup base path (or "" if the skill wasn't installed anywhere).
// Returns an error if any backup copy fails — no partial backups are left on disk.
func backupSkillToUndo(skillName, timestamp string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("backup to ~/.airskills/undo failed: %w", err)
	}

	detected := detectInstalledAgents()
	if len(detected) == 0 {
		detected = []agentDef{agents[0]}
	}

	undoBase := filepath.Join(home, ".airskills", "undo", timestamp, skillName)
	var backedUp int

	for _, a := range detected {
		globalDir := resolveGlobalDir(home, a.GlobalDir)
		skillDir := filepath.Join(globalDir, skillName)
		if _, err := os.Stat(skillDir); err != nil {
			continue // not installed in this agent
		}

		destDir := filepath.Join(undoBase, a.Key)
		if err := copyDirRecursive(skillDir, destDir); err != nil {
			os.RemoveAll(undoBase)
			return "", fmt.Errorf("backup to ~/.airskills/undo/%s/%s/ failed: %w", timestamp, skillName, err)
		}
		backedUp++
	}

	if backedUp == 0 {
		return "", nil
	}
	return undoBase, nil
}

// copyDirRecursive copies a directory tree from src to dst, preserving file modes.
func copyDirRecursive(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode())
		}
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, info.Mode())
	})
}
