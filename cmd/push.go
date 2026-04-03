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

		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}

		skillsDir := filepath.Join(home, ".claude", "skills")
		entries, err := os.ReadDir(skillsDir)
		if err != nil {
			if os.IsNotExist(err) {
				fmt.Println("No skills directory found at ~/.claude/skills/")
				return nil
			}
			return err
		}

		// Collect skills to push
		type skillEntry struct {
			name   string
			dir    string
			marker *SyncEntry
		}

		syncState := loadSyncState()

		var skills []skillEntry
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			skillDir := filepath.Join(skillsDir, entry.Name())
			if _, err := os.Stat(filepath.Join(skillDir, "SKILL.md")); err != nil {
				continue
			}

			se := skillEntry{name: entry.Name(), dir: skillDir}
			if m, ok := syncState.Skills[entry.Name()]; ok {
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
		for _, l := range lines {
			fmt.Printf("  %-20s  %s  %s\n", l.name, renderBar(0), "waiting")
		}

		var pushed, created, linked, renamed, conflicts, failed int64
		var mu sync.Mutex
		var wg sync.WaitGroup
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

				const softLimit = 1024 * 1024
				const hardLimit = 100 * 1024 * 1024
				if archiveSize > hardLimit {
					lines[i].status = "too large"
					renderProgress(lines)
					fmt.Fprintf(os.Stderr, "\n  %s: %dMB exceeds the 100MB hard limit.\n",
						s.name, archiveSize/1024/1024)
					atomic.AddInt64(&failed, 1)
					return
				}
				if archiveSize > softLimit {
					fmt.Fprintf(os.Stderr, "\n  %s: %.1fMB exceeds 1MB free tier limit. Contact chris@airskills.ai to upgrade.\n",
						s.name, float64(archiveSize)/1024/1024)
				}

				localFiles := readSkillFiles(s.dir)
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
						fmt.Fprintf(os.Stderr, "\n  ⚠ %s: %s\n", s.name, updated.Warning)
					}
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

		fmt.Printf("\n%d pushed, %d created, %d linked, %d renamed, %d conflicts, %d failed\n",
			pushed, created, linked, renamed, conflicts, failed)

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


