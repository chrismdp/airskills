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

	"github.com/spf13/cobra"
)

type skillSource struct {
	Owner       string `json:"owner"`                  // username of the original author
	Slug        string `json:"slug"`                   // original skill slug
	ID          string `json:"id"`                     // original skill ID from the server
	ContentHash string `json:"content_hash,omitempty"` // sha256 of original content at add time
}

type conflictInfo struct {
	name       string
	localPath  string
	remotePath string
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

		syncState := loadSyncState()

		var skills []skillEntry
		for name, dir := range localSkills {
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

		// Fetch remote skills to detect already-existing skills (multi-machine sync)
		remoteSkills, _ := client.listSkills("")
		remoteByName := map[string]*apiSkill{}
		for i := range remoteSkills {
			remoteByName[remoteSkills[i].Name] = &remoteSkills[i]
		}

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
		var mu sync.Mutex
		var wg sync.WaitGroup
		var warnings []string
		sem := make(chan struct{}, 5) // max 5 concurrent uploads

		for i, s := range skills {
			i, s := i, s
			wg.Add(1)
			sem <- struct{}{}
			go func() {
				defer wg.Done()
				defer func() { <-sem }()

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

				localFiles := readSkillFiles(s.dir)

				// Size limits based on uncompressed content
				var uncompressedSize int64
				for _, data := range localFiles {
					uncompressedSize += int64(len(data))
				}

				const softLimit int64 = 10 * 1024 * 1024   // 10MB — free tier
				const hardLimit int64 = 100 * 1024 * 1024   // 100MB — absolute max
				if uncompressedSize > hardLimit {
					lines[i].status = "too large"
					renderProgress(lines)
					fmt.Fprintf(os.Stderr, "\n  %s: %dMB exceeds the 100MB limit.\n",
						s.name, uncompressedSize/1024/1024)
					atomic.AddInt64(&failed, 1)
					return
				}
				var sizeWarning string
				if uncompressedSize > softLimit {
					sizeWarning = fmt.Sprintf("%s: %.1fMB exceeds 10MB free tier limit. See airskills.ai/pricing to upgrade.",
						s.name, float64(uncompressedSize)/1024/1024)
				}
				contentHash := computeMerkleHash(localFiles)

				// Sourced skill with no changes — skip
				if s.marker != nil && s.marker.Source != nil && s.marker.Source.ContentHash != "" {
					if contentHash == s.marker.Source.ContentHash {
						lines[i].status = "unchanged"
						lines[i].pct = 1
						renderProgress(lines)
						return
					}
				}

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
					atomic.AddInt64(&failed, 1)
					return
				}

				if isNew {
					atomic.AddInt64(&created, 1)
				} else {
					atomic.AddInt64(&pushed, 1)
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
		saveSyncState(syncState)

		parts := []string{}
		if pushed > 0 {
			parts = append(parts, green(fmt.Sprintf("%d pushed", pushed)))
		}
		if created > 0 {
			parts = append(parts, green(fmt.Sprintf("%d created", created)))
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
		if len(parts) == 0 {
			parts = append(parts, dim("all unchanged"))
		}
		fmt.Printf("\n%s\n", strings.Join(parts, ", "))

		if len(warnings) > 0 {
			for _, w := range warnings {
				fmt.Printf("  %s %s\n", yellow("!"), w)
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

				fmt.Printf("\n  To resolve, tell your AI agent:\n")
				fmt.Printf("  \"Merge %s (remote) with %s (my version),\n", c.remotePath, c.localPath)
				fmt.Printf("   keeping my local changes where possible. Show me the diff before saving.\"\n")
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


