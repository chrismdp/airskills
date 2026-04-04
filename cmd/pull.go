package cmd

import (
	"fmt"
	"os"
	"path/filepath"

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

func runPull(cmd *cobra.Command, args []string) error {
	client, err := newAPIClientAuto()
	if err != nil {
		return err
	}

	localSkills, err := scanSkillsFromAgents()
	if err != nil {
		return err
	}

	// Fetch owned skills only (scope=personal filters server-side)
	remoteSkills, err := client.listSkills("personal")
	if err != nil {
		return fmt.Errorf("fetching skills: %w", err)
	}

	syncState := loadSyncState()

	// Build reverse map: skill_id → dir name (for matching by ID after renames)
	skillIdToName := map[string]string{}
	for name, entry := range syncState.Skills {
		if entry.SkillID != "" {
			skillIdToName[entry.SkillID] = name
		}
	}

	type pullEntry struct {
		skill    apiSkill
		reason   string // "new", "updated", or "diverged"
		localDir string
		marker   *SyncEntry
	}

	var toPull []pullEntry
	for _, remote := range remoteSkills {
		// First try to match by skill_id (survives renames)
		trackedName := ""
		if name, ok := skillIdToName[remote.ID]; ok {
			trackedName = name
		}

		if trackedName != "" {
			// Tracked skill — check if dir still exists
			localDir, exists := localSkills[trackedName]
			if !exists {
				// Dir deleted locally — re-download from remote
				toPull = append(toPull, pullEntry{skill: remote, reason: "new"})
				continue
			}

			marker := syncState.Skills[trackedName]

			// Remote unchanged since last sync? Skip.
			if remote.ContentHash == "" || marker.ContentHash == "" || remote.ContentHash == marker.ContentHash {
				continue
			}

			// Remote changed. Check if local also changed.
			localFiles := readSkillFiles(localDir)
			localHash := computeMerkleHash(localFiles)

			if localHash == marker.ContentHash {
				toPull = append(toPull, pullEntry{skill: remote, reason: "updated", localDir: localDir, marker: marker})
			} else {
				toPull = append(toPull, pullEntry{skill: remote, reason: "diverged", localDir: localDir, marker: marker})
			}
			continue
		}

		// Not tracked — check if dir exists by name
		if _, exists := localSkills[remote.Name]; exists {
			continue // untracked local skill, don't touch
		}

		// New remote skill — download
		toPull = append(toPull, pullEntry{skill: remote, reason: "new"})
	}

	if len(toPull) == 0 {
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
			// Save remote version to unique conflict dir — don't overwrite local
			conflictDir := filepath.Join(conflictBase, p.skill.Name)
			os.MkdirAll(conflictDir, 0755)
			for name, content := range files {
				target := filepath.Join(conflictDir, name)
				os.MkdirAll(filepath.Dir(target), 0755)
				os.WriteFile(target, content, 0644)
			}

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

		lines[i].status = "installing"
		lines[i].pct = 0.8
		renderProgress(lines)

		destinations, err := installSkillToAgents(p.skill.Name, files)
		if err != nil {
			lines[i].status = "failed"
			renderProgress(lines)
			failed++
			continue
		}

		// Update sync state
		dirName := p.skill.Name
		if p.localDir != "" {
			dirName = filepath.Base(p.localDir)
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
		}
		lines[i].pct = 1
		renderProgress(lines)
	}

	fmt.Printf("\n%d pulled, %d updated, %d diverged, %d failed\n", pulled, updated, diverged, failed)

	if len(updateDetails) > 0 {
		fmt.Println("\n--- Updated skills ---")
		for _, u := range updateDetails {
			fmt.Printf("\n  %s  %s → %s\n", u.name, u.oldVersion, u.newVersion)
			if len(u.messages) > 0 {
				for _, msg := range u.messages {
					fmt.Printf("    • %s\n", msg)
				}
			}
		}
	}

	if len(divergedDetails) > 0 {
		fmt.Println("\n--- Diverged skills ---")
		fmt.Println("These skills were edited locally AND remotely. The remote version")
		fmt.Println("has been saved so you can merge the changes.")
		for _, d := range divergedDetails {
			fmt.Printf("\n  %s\n", d.name)
			fmt.Printf("    Local:  %s\n", d.localDir)
			fmt.Printf("    Remote: %s\n", d.remoteDir)
		}
		fmt.Println("\nMerge the files, then run 'airskills push --force' to resolve.")
	}

	saveSyncState(syncState)
	_ = saveLastSync()
	return nil
}
