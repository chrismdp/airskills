package cmd

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/chrismdp/airskills/telemetry"
	"github.com/spf13/cobra"
)

type skillSource struct {
	Owner        string `json:"owner"`                   // username of the original author
	Slug         string `json:"slug"`                    // original skill slug
	ID           string `json:"id"`                      // original skill ID from the server
	ContentHash  string `json:"content_hash,omitempty"`  // sha256 of original content at add time
	SkillsetSlug string `json:"skillset_slug,omitempty"` // org skillset slug (non-empty for org-distributed skills)
	GitHubURL    string `json:"github_url,omitempty"`    // GitHub repo URL (for skills imported from GitHub)
	GitHubSkill  string `json:"github_skill,omitempty"`  // skill subdirectory within the GitHub repo (for multi-skill repos)
}

type conflictInfo struct {
	name       string
	localPath  string
	remotePath string
}

type validationInfo struct {
	name string
	err  error
}

// pendingSuggestionPrompt is collected inside the concurrent push goroutines
// and drained sequentially after wg.Wait so we can prompt the user without
// racing multiple goroutines on stdin.
type pendingSuggestionPrompt struct {
	name             string
	suggesterSkillID string
	source           *skillSource
}

var pushForce bool

var pushCmd = &cobra.Command{
	Use:   "push",
	Short: "Push local skill changes to airskills.ai",
	Long:  "Scans local skills, detects changes, and pushes updates (including all files) to the server.",
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := newAPIClientAuto()
		if err != nil {
			return err
		}

		// Confirm force push
		if pushForce {
			fmt.Print("Force push will overwrite remote versions. Continue? [y/N] ")
			reader := bufio.NewReader(os.Stdin)
			answer, _ := reader.ReadString('\n')
			if strings.TrimSpace(strings.ToLower(answer)) != "y" {
				fmt.Println("Aborted.")
				return nil
			}
		}

		var conflictMessages []conflictInfo
		var validationMessages []validationInfo

		syncState := loadSyncState()

		// Propagate any local edit across every detected agent dir before we
		// scan, so push sees a consistent view. Slugs whose copies can't be
		// safely reconciled are reported and skipped below.
		_, mirrorConflicts := mirrorLocalSkills(syncState)
		printMirrorConflicts(mirrorConflicts)
		mirrorConflictSet := map[string]bool{}
		for _, c := range mirrorConflicts {
			mirrorConflictSet[c.slug] = true
		}

		// Scan all detected agent directories for skills
		localSkills, err := scanSkillsFromAgents()
		if err != nil || len(localSkills) == 0 {
			fmt.Println("No skills found in any agent directory.")
			return nil
		}

		// Collect skills to push
		type skillEntry struct {
			name   string
			dir    string
			marker *SyncEntry
		}

		var skills []skillEntry
		for name, dir := range localSkills {
			if mirrorConflictSet[name] {
				continue
			}
			se := skillEntry{name: name, dir: dir}
			if m, ok := syncState.Skills[name]; ok {
				se.marker = m
			}
			skills = append(skills, se)
		}

		// Detect renames: entries in sync state whose directory no longer exists
		localDirSet := map[string]bool{}
		for _, s := range skills {
			localDirSet[s.name] = true
		}
		orphanHashToName := map[string]string{} // content_hash → old dir name
		for name, entry := range syncState.Skills {
			if !localDirSet[name] && entry.ContentHash != "" {
				orphanHashToName[entry.ContentHash] = name
			}
		}

		if len(skills) == 0 {
			fmt.Println("No skills to push.")
			return nil
		}

		// Fetch owned skills only (scope=personal filters server-side)
		remoteSkills, _ := client.listSkills("personal")
		remoteByName := map[string]*apiSkill{}
		ownedSkillIDs := map[string]bool{}
		for i := range remoteSkills {
			remoteByName[remoteSkills[i].Name] = &remoteSkills[i]
			ownedSkillIDs[remoteSkills[i].ID] = true
		}

		// Filter out skills whose sync state SkillID belongs to another user.
		// This happens when skills are installed via "add" from another user and
		// the sync state somehow ends up with the original owner's skill ID.
		var filtered []skillEntry
		var skipped int
		for _, s := range skills {
			if s.marker != nil && s.marker.SkillID != "" && !ownedSkillIDs[s.marker.SkillID] {
				// SkillID doesn't belong to us. If it has a Source (was added from
				// another user), clear the SkillID so it gets created as a fork.
				if s.marker.Source != nil {
					s.marker.SkillID = ""
				} else {
					// Unknown ownership — skip to avoid "not your skill" errors
					skipped++
					continue
				}
			}
			filtered = append(filtered, s)
		}
		skills = filtered

		// Print initial progress lines
		lines := make([]progressLine, len(skills))
		for i, s := range skills {
			lines[i] = progressLine{name: s.name, status: "waiting", pct: 0}
		}
		if verbose && isTTY {
			for _, l := range lines {
				fmt.Printf("  %-20s  %s  %s\n", l.name, renderBar(0), "waiting")
			}
		} else if isTTY {
			fmt.Printf("  %s %d skills\n", dim("·"), len(skills))
		}

		var pushed, created, linked, renamed, conflicts, failed int64
		var pushedNames, createdNames []string
		var mu sync.Mutex
		var wg sync.WaitGroup
		var warnings []string
		var pendingPrompts []pendingSuggestionPrompt
		sem := make(chan struct{}, 5) // max 5 concurrent uploads

		// Free tier limits (checked client-side as guidance, server enforces)
		const freeSkillLimit = 100
		const freeStorageLimit int64 = 100 * 1024 * 1024 // 100MB total
		if len(skills) > freeSkillLimit {
			warnings = append(warnings, fmt.Sprintf("%d skills exceeds %d free tier limit — will not be supported in future versions. See airskills.ai/pricing", len(skills), freeSkillLimit))
		}
		// Calculate total local storage
		var totalStorage int64
		for _, s := range skills {
			localFiles := readSkillFiles(s.dir)
			for _, data := range localFiles {
				totalStorage += int64(len(data))
			}
		}
		if totalStorage > freeStorageLimit {
			warnings = append(warnings, fmt.Sprintf("%.1fMB total storage exceeds 100MB free tier limit — will not be supported in future versions. See airskills.ai/pricing",
				float64(totalStorage)/1024/1024))
		}

		for i, s := range skills {
			i, s := i, s
			wg.Add(1)
			sem <- struct{}{}
			go func() {
				defer wg.Done()
				defer func() { <-sem }()

				localFiles := readSkillFiles(s.dir)
				if err := validateSkillFiles(s.dir, localFiles); err != nil {
					lines[i].status = "invalid"
					lines[i].pct = 0
					renderProgress(lines)
					mu.Lock()
					validationMessages = append(validationMessages, validationInfo{name: s.name, err: err})
					mu.Unlock()
					atomic.AddInt64(&failed, 1)
					return
				}

				lines[i].status = "compressing"
				lines[i].pct = 0.2
				renderProgress(lines)

				archive, err := createTarGz(s.dir)
				if err != nil {
					lines[i].status = "failed"
					lines[i].pct = 0
					renderProgress(lines)
					atomic.AddInt64(&failed, 1)
					return
				}

				archiveSize := int64(len(archive))

				var sizeWarning string
				contentHash := computeMerkleHash(localFiles)

				// Skip unchanged skills (content hash matches what we last pushed)
				if s.marker != nil && s.marker.ContentHash != "" && s.marker.ContentHash == contentHash {
					if sizeWarning != "" {
						mu.Lock()
						warnings = append(warnings, sizeWarning)
						mu.Unlock()
					}
					lines[i].status = "unchanged"
					lines[i].pct = 1
					renderProgress(lines)
					return
				}

				isNew := s.marker == nil || s.marker.SkillID == ""
				if isNew {
					// Check for rename
					mu.Lock()
					oldName, found := orphanHashToName[contentHash]
					if found {
						oldEntry := syncState.Skills[oldName]
						s.marker = &SyncEntry{
							SkillID:     oldEntry.SkillID,
							Version:     oldEntry.Version,
							ContentHash: oldEntry.ContentHash,
							Tool:        oldEntry.Tool,
							Source:      oldEntry.Source,
						}
						isNew = false
						delete(syncState.Skills, oldName)
						delete(orphanHashToName, contentHash)
						syncState.Skills[s.name] = s.marker
						mu.Unlock()
						fmt.Fprintf(os.Stderr, "\n  %s → %s (renamed)\n", oldName, s.name)
						lines[i].status = "renamed"
						lines[i].pct = 1
						renderProgress(lines)
						atomic.AddInt64(&renamed, 1)
						return
					}
					mu.Unlock()

					// Check if skill already exists on server
					if remote, found := remoteByName[s.name]; found {
						s.marker = &SyncEntry{
							SkillID:     remote.ID,
							Version:     remote.Version,
							ContentHash: remote.ContentHash,
							Tool:        "claude-code",
						}
						isNew = false

						if remote.ContentHash == contentHash {
							mu.Lock()
							syncState.Skills[s.name] = s.marker
							mu.Unlock()
							lines[i].status = "linked"
							lines[i].pct = 1
							renderProgress(lines)
							atomic.AddInt64(&linked, 1)
							return
						}

						if !pushForce {
							lines[i].status = "CONFLICT"
							renderProgress(lines)

							tmpDir := filepath.Join(os.TempDir(), "airskills-conflicts", s.name)
							os.MkdirAll(tmpDir, 0755)
							rawBody, rawErr := client.get(fmt.Sprintf("/api/v1/skills/%s/raw", remote.ID))
							if rawErr == nil {
								tmpPath := filepath.Join(tmpDir, "SKILL.md")
								os.WriteFile(tmpPath, rawBody, 0644)
								mu.Lock()
								conflictMessages = append(conflictMessages, conflictInfo{
									name:       s.name,
									localPath:  filepath.Join(s.dir, "SKILL.md"),
									remotePath: tmpPath,
								})
								mu.Unlock()
							}
							atomic.AddInt64(&conflicts, 1)
							return
						}
					} else {
						lines[i].status = "creating"
						lines[i].pct = 0.4
						renderProgress(lines)

						var forkedFrom string
						if s.marker != nil && s.marker.Source != nil {
							forkedFrom = s.marker.Source.ID
						}

						skill, err := client.createSkill(s.name, "", []string{"claude-code"}, forkedFrom)
						if err != nil {
							lines[i].status = "failed"
							renderProgress(lines)
							atomic.AddInt64(&failed, 1)
							return
						}

						s.marker = &SyncEntry{SkillID: skill.ID, Version: skill.Version, Tool: "claude-code"}
						mu.Lock()
						if old, ok := syncState.Skills[s.name]; ok && old.Source != nil {
							s.marker.Source = old.Source
						}
						mu.Unlock()
					}
				}

				lines[i].status = "uploading"
				lines[i].pct = 0.6
				renderProgress(lines)

				expectedHash := ""
				if !pushForce && s.marker.ContentHash != "" {
					expectedHash = s.marker.ContentHash
				}

				updated, statusCode, err := client.putArchive(
					s.marker.SkillID, archive, expectedHash, contentHash,
				)
				if err != nil {
					if statusCode == 409 {
						lines[i].status = "CONFLICT"
						renderProgress(lines)

						var conflict struct {
							RemoteContentHash string `json:"remote_content_hash"`
						}
						json.Unmarshal([]byte(err.Error()), &conflict)

						tmpDir := filepath.Join(os.TempDir(), "airskills-conflicts", s.name)
						os.MkdirAll(tmpDir, 0755)
						rawBody, rawErr := client.get(fmt.Sprintf("/api/v1/skills/%s/raw", s.marker.SkillID))
						if rawErr == nil {
							tmpPath := filepath.Join(tmpDir, "SKILL.md")
							os.WriteFile(tmpPath, rawBody, 0644)
							mu.Lock()
							conflictMessages = append(conflictMessages, conflictInfo{
								name:       s.name,
								localPath:  filepath.Join(s.dir, "SKILL.md"),
								remotePath: tmpPath,
							})
							mu.Unlock()
						}
						atomic.AddInt64(&conflicts, 1)
						return
					}

					lines[i].status = "failed"
					renderProgress(lines)
					mu.Lock()
					warnings = append(warnings, fmt.Sprintf("%s: %v", s.name, err))
					mu.Unlock()
					atomic.AddInt64(&failed, 1)
					return
				}

				if isNew {
					atomic.AddInt64(&created, 1)
					mu.Lock()
					createdNames = append(createdNames, s.name)
					mu.Unlock()
				} else {
					atomic.AddInt64(&pushed, 1)
					mu.Lock()
					pushedNames = append(pushedNames, s.name)
					mu.Unlock()
				}

				if s.marker.Source != nil && s.marker.SuggestionID == "" && !s.marker.SuggestDeclined {
					mu.Lock()
					pendingPrompts = append(pendingPrompts, pendingSuggestionPrompt{
						name:             s.name,
						suggesterSkillID: s.marker.SkillID,
						source:           s.marker.Source,
					})
					mu.Unlock()
				}
				if updated != nil {
					s.marker.Version = updated.Version
					s.marker.ContentHash = updated.ContentHash
					if updated.Warning != "" {
						mu.Lock()
						warnings = append(warnings, fmt.Sprintf("%s: %s", s.name, updated.Warning))
						mu.Unlock()
					}
				}
				if sizeWarning != "" {
					mu.Lock()
					warnings = append(warnings, sizeWarning)
					mu.Unlock()
				}
				mu.Lock()
				syncState.Skills[s.name] = s.marker
				mu.Unlock()

				lines[i].status = "done"
				lines[i].size = formatSize(archiveSize)
				lines[i].pct = 1
				renderProgress(lines)
			}()
		}

		wg.Wait()

		// Drain sequentially so goroutines don't race on stdin. In a headless
		// session we can't prompt, so print agent-focused instructions instead
		// and leave the entry unmarked — the next interactive push will ask.
		if len(pendingPrompts) > 0 && !isTTY {
			fmt.Fprint(os.Stderr, agentSuggestionInstructions(pendingPrompts))
		}
		if len(pendingPrompts) > 0 && isTTY {
			reader := bufio.NewReader(os.Stdin)
			for _, p := range pendingPrompts {
				fmt.Printf("\n  %s was originally from %s/%s\n", p.name, p.source.Owner, p.source.Slug)
				fmt.Print("  Create a suggestion for the owner, or just keep your version? [s/K] ")
				answer, _ := reader.ReadString('\n')
				answer = strings.TrimSpace(strings.ToLower(answer))

				if answer != "s" && answer != "suggest" && answer != "y" && answer != "yes" {
					if entry, ok := syncState.Skills[p.name]; ok {
						entry.SuggestDeclined = true
					}
					continue
				}

				fmt.Print("  Message (optional, press Enter to skip): ")
				message, _ := reader.ReadString('\n')
				message = strings.TrimSpace(message)

				suggestion, err := client.createSuggestion(
					p.suggesterSkillID,
					p.source.ID,
					p.source.ContentHash,
					message,
				)
				if err != nil {
					fmt.Fprintf(os.Stderr, "  %s suggestion failed: %v\n", yellow("!"), err)
					continue
				}
				fmt.Printf("  %s suggestion sent to %s/%s\n", green("✓"), p.source.Owner, p.source.Slug)
				if entry, ok := syncState.Skills[p.name]; ok {
					entry.SuggestionID = suggestion.ID
				}
			}
		}

		saveSyncState(syncState)

		parts := []string{}
		if pushed > 0 {
			parts = append(parts, green(fmt.Sprintf("%d pushed", pushed)))
			for _, n := range pushedNames {
				fmt.Printf("  %s %s\n", green("↑"), n)
			}
		}
		if created > 0 {
			parts = append(parts, green(fmt.Sprintf("%d created", created)))
			for _, n := range createdNames {
				fmt.Printf("  %s %s\n", green("+"), n)
			}
		}
		if linked > 0 {
			parts = append(parts, fmt.Sprintf("%d linked", linked))
		}
		if renamed > 0 {
			parts = append(parts, fmt.Sprintf("%d renamed", renamed))
		}
		if conflicts > 0 {
			parts = append(parts, red(fmt.Sprintf("%d conflicts", conflicts)))
		}
		if failed > 0 {
			parts = append(parts, red(fmt.Sprintf("%d failed", failed)))
		}
		if skipped > 0 {
			parts = append(parts, dim(fmt.Sprintf("%d skipped (not yours)", skipped)))
		}
		if len(parts) == 0 {
			parts = append(parts, dim("all unchanged"))
		}
		fmt.Printf("\n%s\n", strings.Join(parts, ", "))

		if len(warnings) > 0 {
			for _, w := range warnings {
				fmt.Printf("  %s %s\n", yellow("!"), w)
			}
		}

		telemetry.Capture("cli_push", map[string]interface{}{
			"pushed":    pushed,
			"created":   created,
			"linked":    linked,
			"renamed":   renamed,
			"conflicts": conflicts,
			"failed":    failed,
			"skipped":   skipped,
			"force":     pushForce,
		})

		if len(validationMessages) > 0 {
			fmt.Println("\n--- Invalid SKILL.md frontmatter ---")
			for _, v := range validationMessages {
				fmt.Printf("\n  %s\n", v.name)
				fmt.Printf("  %s\n", strings.ReplaceAll(v.err.Error(), "\n", "\n  "))
			}
		}

		// Show conflict resolution instructions
		if len(conflictMessages) > 0 {
			fmt.Println("\n--- Conflicts ---")
			for _, c := range conflictMessages {
				fmt.Printf("\n  %s (content changed on remote)\n", c.name)
				fmt.Printf("  Local:  %s\n", c.localPath)
				fmt.Printf("  Remote: %s\n", c.remotePath)

				// Show a brief diff summary
				localData, _ := os.ReadFile(c.localPath)
				remoteData, _ := os.ReadFile(c.remotePath)
				localLines := len(strings.Split(string(localData), "\n"))
				remoteLines := len(strings.Split(string(remoteData), "\n"))
				fmt.Printf("  Local: %d lines, Remote: %d lines\n", localLines, remoteLines)

				fmt.Print(pushConflictResolutionInstructions(c, !isTTY))
			}
			fmt.Println("\n  After merging, run: airskills push --force")
			fmt.Println("  To see the full diff: diff", conflictMessages[0].localPath, conflictMessages[0].remotePath)
		}

		return nil
	},
}

func init() {
	pushCmd.Flags().BoolVar(&pushForce, "force", false, "Skip conflict check (use after resolving conflicts)")
	pushCmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "Show per-skill progress")
	rootCmd.AddCommand(pushCmd)
}

func createTarGz(dir string) ([]byte, error) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)

	base := filepath.Base(dir)

	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		// Skip .airskills marker
		if info.Name() == ".airskills" {
			return nil
		}

		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}

		// Use relative path inside the archive (always forward slashes for tar)
		rel, _ := filepath.Rel(dir, path)
		header.Name = filepath.ToSlash(filepath.Join(base, rel))

		if err := tw.WriteHeader(header); err != nil {
			return err
		}

		if info.IsDir() {
			return nil
		}

		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(tw, f)
		return err
	})

	if err != nil {
		return nil, err
	}

	tw.Close()
	gz.Close()
	return buf.Bytes(), nil
}

// readSkillFiles reads all files in a skill directory (excluding .airskills marker).
func readSkillFiles(dir string) map[string][]byte {
	files := map[string][]byte{}
	filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || info.Name() == ".airskills" {
			return nil
		}
		rel, _ := filepath.Rel(dir, path)
		data, err := os.ReadFile(path)
		if err == nil {
			files[rel] = data
		}
		return nil
	})
	return files
}
