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
var pullVersionFlag string

func init() {
	pullCmd.Flags().StringVar(&skillsetFlag, "skillset", "", "Personal skillset to pull against (default: your last-used skillset)")
	pullCmd.Flags().BoolVar(&pullForceFlag, "force", false, "Overwrite local with remote for diverged skills (backs up local first)")
	pullCmd.Flags().StringVar(&pullVersionFlag, "version", "", "Pull a specific commit version of a skill: pull --version <commit-hash> <skill>")
	rootCmd.AddCommand(pullCmd)
}

var pullCmd = &cobra.Command{
	Use:   "pull",
	Short: "Download remote skills not on this machine, and update changed ones",
	Long:  "Pulls skills from your airskills.ai account that aren't installed locally or have been updated remotely. If both local and remote changed, saves the remote version for merge.",
	RunE:  runPull,
}

type conflictDetail struct {
	name      string
	localDir  string
	remoteDir string
}

type updateDetail struct {
	name       string
	oldVersion string
	newVersion string
	messages   []string
}

type pullEntry struct {
	skill    apiSkill
	reason   string // "new", "updated", "diverged", "transferred", "auto-resolved", "linked", "untracked-conflict"
	localDir string
	marker   *SyncEntry
}

func runPull(cmd *cobra.Command, args []string) error {
	if pullForceFlag {
		return runPullForce(cmd, args)
	}
	if pullVersionFlag != "" {
		return runPullVersion(cmd, args)
	}

	client, err := newAPIClientAuto()
	loggedIn := err == nil

	syncState := loadSyncState()

	// Propagate local edits across every detected agent dir before scanning,
	// so pull's divergence check works regardless of which copy was edited.
	// Slugs whose copies can't be reconciled are skipped to avoid clobbering
	// the user's in-progress work.
	_, mirrorConflicts := mirrorLocalSkills(syncState)
	printMirrorConflicts(mirrorConflicts)
	mirrorConflictSet := map[string]bool{}
	for _, c := range mirrorConflicts {
		mirrorConflictSet[c.slug] = true
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
	if resolvedSlug != "" {
		fmt.Printf("  %s %s\n", dim("Skillset:"), resolvedSlug)
	}

	// Resolve upstream updates on forked skills before deciding actions.
	for i := range remoteSkills {
		if remoteSkills[i].HasUpstreamUpdate() {
			if updated, err := client.pullUpstream(remoteSkills[i].ID); err == nil {
				remoteSkills[i].ContentHash = updated.ContentHash
				remoteSkills[i].Version = updated.Version
			}
		}
	}

	toPull, missingWarnings := decidePullActions(remoteSkills, localSkills, syncState)

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

	var pulled, updated, diverged, failed, autoResolved int
	var pulledNames []string
	var divergedDetails []conflictDetail
	var updateDetails []updateDetail

	// Unique conflict dir per sync run
	conflictBase, _ := os.MkdirTemp("", "airskills-conflicts-")

	for i, p := range toPull {
		// Auto-resolved: local already matches remote — update marker silently, no download.
		if p.reason == "auto-resolved" {
			if p.marker != nil {
				p.marker.ContentHash = p.skill.ContentHash
				p.marker.Version = p.skill.Version
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

		// Linked: an untracked local dir whose bytes match the server's
		// copy. Write the marker silently — no download, no install. The
		// classifier on the next sync will see this as plain "synced".
		if p.reason == "linked" {
			dirName := filepath.Base(p.localDir)
			syncState.Skills[dirName] = &SyncEntry{
				SkillID:     p.skill.ID,
				Version:     p.skill.Version,
				ContentHash: p.skill.ContentHash,
				Tool:        "claude-code",
			}
			autoResolved++
			fmt.Printf("  %s %s %s\n", green("·"), p.skill.Name, dim("linked (bytes match server, no download)"))
			lines[i].status = "linked"
			lines[i].pct = 1
			renderProgress(lines)
			continue
		}

		lines[i].status = "downloading"
		lines[i].pct = 0.5
		renderProgress(lines)

		files, err := downloadSkillFiles(client, p.skill.ID)
		if err != nil || len(files) == 0 {
			lines[i].status = "failed"
			renderProgress(lines)
			failed++
			continue
		}

		if p.reason == "diverged" || p.reason == "untracked-conflict" {
			conflictDir := filepath.Join(conflictBase, p.skill.Name)
			os.MkdirAll(conflictDir, 0755)
			_ = writeFilesToDir(conflictDir, files)

			if p.reason == "untracked-conflict" {
				lines[i].status = "UNTRACKED-CONFLICT"
			} else {
				lines[i].status = "DIVERGED"
			}
			lines[i].pct = 1
			renderProgress(lines)
			diverged++
			divergedDetails = append(divergedDetails, conflictDetail{
				name:      p.skill.Name,
				localDir:  p.localDir,
				remoteDir: conflictDir,
			})
			continue
		}

		if p.reason == "transferred" {
			// Server-side transfer: old slug → new slug.
			// Install new dir, then reconcile the old one.
			newDirName := p.skill.Name
			lines[i].status = "transferred"
			lines[i].pct = 0.8
			renderProgress(lines)

			if _, err := installSkillToAgents(newDirName, files); err != nil {
				lines[i].status = "failed"
				renderProgress(lines)
				failed++
				continue
			}

			// Add marker for the new dir
			syncState.Skills[newDirName] = &SyncEntry{
				SkillID:     p.skill.ID,
				Version:     p.skill.Version,
				ContentHash: p.skill.ContentHash,
				Tool:        "claude-code",
			}

			// Reconcile old dir
			oldDirName := filepath.Base(p.localDir)
			if p.marker != nil && p.localDir != "" {
				localFiles := readSkillFiles(p.localDir)
				localHash := computeMerkleHash(localFiles)
				if localHash == p.marker.ContentHash {
					// No local edits — delete old dir across all agents
					_ = removeSkillDirAcrossAgents(p.localDir)
					delete(syncState.Skills, oldDirName)
				} else {
					// Local edits exist — mark deleted and warn
					p.marker.Deleted = true
					p.marker.MovedTo = newDirName
					syncState.Skills[oldDirName] = p.marker
					fmt.Fprintf(os.Stderr,
						"\n  %s %s: local edits not pushed — skill transferred to %s. Merge manually into %s/ then rm -rf %s/\n",
						yellow("!"), oldDirName, newDirName, newDirName, oldDirName)
				}
			}

			lines[i].status = "done"
			lines[i].size = fmt.Sprintf("%s → %s", oldDirName, newDirName)
			lines[i].pct = 1
			renderProgress(lines)
			updated++
			updateDetails = append(updateDetails, updateDetail{
				name:       p.skill.Name,
				oldVersion: p.marker.Version,
				newVersion: p.skill.Version,
				messages:   []string{"transferred"},
			})
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
		syncState.Skills[dirName] = &SyncEntry{
			SkillID:     p.skill.ID,
			Version:     p.skill.Version,
			ContentHash: p.skill.ContentHash,
			Tool:        "claude-code",
		}

		if p.reason == "updated" {
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

			// Fetch commit messages since last known version
			commits, err := client.getVersionHistory(p.skill.ID)
			if err == nil {
				for _, c := range commits {
					if c.Message != "" {
						detail.messages = append(detail.messages, c.Message)
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
	fmt.Printf("\n%d pulled, %d updated, %d diverged, %d auto-resolved, %d failed", pulled, updated, diverged, autoResolved, failed)
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
			})
		}
		fmt.Print(conflictResolutionMessage(entries, !isTTY))
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
		setAnonHeader(resolveReq)
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
		if s.Status == "pending" || s.ReviewedAt == nil {
			continue
		}
		if cutoff != "" && *s.ReviewedAt <= cutoff {
			continue
		}
		if !shown {
			fmt.Println()
			fmt.Println("--- Suggestions ---")
			shown = true
		}
		skillName := s.OwnerSkillName
		if skillName == "" {
			skillName = s.OwnerSkillID
		}
		switch s.Status {
		case "accepted":
			fmt.Printf("  %s your suggestion for %q was accepted\n", green("✓"), skillName)
		case "declined":
			if s.ResponseMessage != "" {
				fmt.Printf("  %s your suggestion for %q was declined: %q\n",
					yellow("✗"), skillName, s.ResponseMessage)
			} else {
				fmt.Printf("  %s your suggestion for %q was declined\n", yellow("✗"), skillName)
			}
		}
		if *s.ReviewedAt > newest {
			newest = *s.ReviewedAt
		}
	}
	if newest != "" {
		syncState.LastSuggestionNotifyAt = newest
	}
}

// decidePullActions inspects remote, local, and sync state to decide which
// remote skills to download. It is the pure decision core of runPull, with
// no network calls. The hashLocal helper reads disk for divergence checks.
//
// Behaviour:
//   - tracked + slug changed (server-side transfer): "transferred"
//   - tracked + local present + remote unchanged: skip
//   - tracked + local present + only remote changed: "updated"
//   - tracked + local present + both changed: "diverged"
//   - tracked + local missing: warn and skip (treat as intentional removal —
//     user should run 'airskills rm <name>' to delete server-side, or
//     'airskills pull <name>' to restore)
//   - untracked + local with same name + bytes match: "linked" (silent claim)
//   - untracked + local with same name + bytes differ: "untracked-conflict"
//   - untracked + no local: "new"
func decidePullActions(remoteSkills []apiSkill, localSkills map[string]string, syncState *SyncState) ([]pullEntry, []string) {
	skillIdToName := map[string]string{}
	for name, entry := range syncState.Skills {
		if entry.SkillID != "" {
			skillIdToName[entry.SkillID] = name
		}
	}

	var actions []pullEntry
	var warnings []string

	for _, remote := range remoteSkills {
		trackedName := ""
		if name, ok := skillIdToName[remote.ID]; ok {
			trackedName = name
		}

		if trackedName != "" {
			// Server-side transfer: slug changed from what we last tracked
			if remote.Name != trackedName {
				localDir := localSkills[trackedName]
				actions = append(actions, pullEntry{
					skill:    remote,
					reason:   "transferred",
					localDir: localDir,
					marker:   syncState.Skills[trackedName],
				})
				continue
			}

			localDir, exists := localSkills[trackedName]
			if !exists {
				warnings = append(warnings, fmt.Sprintf(
					"%s: tracked but missing locally — run 'airskills rm %s' to delete server-side, or 'airskills pull %s' to restore",
					trackedName, trackedName, trackedName,
				))
				continue
			}

			marker := syncState.Skills[trackedName]
			if remote.ContentHash == "" || marker.ContentHash == "" || remote.ContentHash == marker.ContentHash {
				continue
			}

			// Skip skills mid-transfer — handled by the transfer flow, not here.
			if marker.Deleted || marker.MovedTo != "" {
				continue
			}

			localFiles := readSkillFiles(localDir)
			localHash := computeMerkleHash(localFiles)

			switch {
			case localHash == remote.ContentHash:
				// Auto-detect: local already matches remote bytes.
				// Marker is stale from manual reconciliation — update silently.
				actions = append(actions, pullEntry{skill: remote, reason: "auto-resolved", localDir: localDir, marker: marker})
			case localHash == marker.ContentHash:
				actions = append(actions, pullEntry{skill: remote, reason: "updated", localDir: localDir, marker: marker})
			default:
				actions = append(actions, pullEntry{skill: remote, reason: "diverged", localDir: localDir, marker: marker})
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
			if remote.ContentHash != "" && localHash == remote.ContentHash {
				actions = append(actions, pullEntry{
					skill: remote, reason: "linked", localDir: localDir,
				})
			} else {
				actions = append(actions, pullEntry{
					skill: remote, reason: "untracked-conflict", localDir: localDir,
				})
			}
			continue
		}

		actions = append(actions, pullEntry{skill: remote, reason: "new"})
	}

	return actions, warnings
}

// runPullForce implements `airskills pull --force [skill...]`.
// Downloads the remote version of diverged skills and overwrites local,
// backing up current local files to ~/.airskills/undo/<ts>/<skill>/<agent>/ first.
func runPullForce(cmd *cobra.Command, args []string) error {
	if !isTTY {
		return fmt.Errorf("pull --force requires confirmation. Run interactively or use 'airskills sync' after manual reconciliation.")
	}

	client, err := newAPIClientAuto()
	if err != nil {
		return fmt.Errorf("pull --force requires authentication: %w", err)
	}

	syncState := loadSyncState()
	_, mirrorConflicts := mirrorLocalSkills(syncState)
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

	toPull, _ := decidePullActions(remoteSkills, localSkills, syncState)
	divergedMap := map[string]pullEntry{}
	for _, p := range toPull {
		if p.reason == "diverged" {
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
		files, err := downloadSkillFiles(client, p.skill.ID)
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
		marker.SkillID = p.skill.ID
		marker.ContentHash = p.skill.ContentHash
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
