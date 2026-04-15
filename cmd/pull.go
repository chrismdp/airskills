package cmd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"github.com/chrismdp/airskills/config"
	"github.com/chrismdp/airskills/telemetry"
	"github.com/spf13/cobra"
)

func init() {
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
	reason   string // "new", "updated", or "diverged"
	localDir string
	marker   *SyncEntry
}

func runPull(cmd *cobra.Command, args []string) error {
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

	// Fetch owned skills only (scope=personal filters server-side)
	remoteSkills, err := client.listSkills("personal")
	if err != nil {
		return fmt.Errorf("fetching skills: %w", err)
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

	var pulled, updated, diverged, failed int
	var pulledNames []string
	var divergedDetails []conflictDetail
	var updateDetails []updateDetail

	// Unique conflict dir per sync run
	conflictBase, _ := os.MkdirTemp("", "airskills-conflicts-")

	for i, p := range toPull {
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

		if p.reason == "diverged" {
			conflictDir := filepath.Join(conflictBase, p.skill.Name)
			os.MkdirAll(conflictDir, 0755)
			_ = writeFilesToDir(conflictDir, files)

			lines[i].status = "DIVERGED"
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
	fmt.Printf("\n%d pulled, %d updated, %d diverged, %d failed", pulled, updated, diverged, failed)
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
		fmt.Println("\n--- Diverged skills ---")
		fmt.Println("These skills were edited locally AND remotely. The remote version")
		fmt.Println("has been saved so you can merge the changes.")
		var hasSourced bool
		for _, d := range divergedDetails {
			fmt.Printf("\n  %s\n", d.name)
			fmt.Printf("    Local:  %s\n", d.localDir)
			fmt.Printf("    Remote: %s\n", d.remoteDir)
			if entry, ok := syncState.Skills[d.name]; ok && entry.Source != nil {
				hasSourced = true
				fmt.Printf("    Source: %s/%s (owner's version has moved)\n",
					entry.Source.Owner, entry.Source.Slug)
			}
		}
		fmt.Print(pullDivergenceFooter(hasSourced, !isTTY))
	}

	notifyResolvedSuggestions(client, syncState)

	saveSyncState(syncState)
	_ = saveLastSync()

	telemetry.Capture("cli_pull", map[string]interface{}{
		"pulled":    pulled,
		"updated":   updated,
		"diverged":  diverged,
		"failed":    failed,
		"missing":   len(missingWarnings),
		"anonymous": false,
	})
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
//   - untracked + local with same name: skip (don't clobber unrelated dirs)
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

			localFiles := readSkillFiles(localDir)
			localHash := computeMerkleHash(localFiles)

			if localHash == marker.ContentHash {
				actions = append(actions, pullEntry{skill: remote, reason: "updated", localDir: localDir, marker: marker})
			} else {
				actions = append(actions, pullEntry{skill: remote, reason: "diverged", localDir: localDir, marker: marker})
			}
			continue
		}

		if _, exists := localSkills[remote.Name]; exists {
			continue
		}

		actions = append(actions, pullEntry{skill: remote, reason: "new"})
	}

	return actions, warnings
}
