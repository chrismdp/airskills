package cmd

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

type airskillsMarker struct {
	SkillID string `json:"skill_id"`
	Version string `json:"version"`
	Tool    string `json:"tool"`
	// Source tracks the original skill for no-auth installs.
	// When the user later syncs with an account, unchanged sourced skills
	// get linked (forked_from) rather than duplicated.
	Source *skillSource `json:"source,omitempty"`
}

type skillSource struct {
	Owner string `json:"owner"` // username of the original author
	Slug  string `json:"slug"`  // original skill slug
	ID    string `json:"id"`    // original skill ID from the server
}

type conflictInfo struct {
	name       string
	localPath  string
	remotePath string
	localVer   string
	remoteVer  string
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
			name      string
			dir       string
			marker    *airskillsMarker
			hasMarker bool
		}

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
			markerData, err := os.ReadFile(filepath.Join(skillDir, ".airskills"))
			if err == nil {
				var m airskillsMarker
				if json.Unmarshal(markerData, &m) == nil {
					se.marker = &m
					se.hasMarker = true
				}
			}
			skills = append(skills, se)
		}

		if len(skills) == 0 {
			fmt.Println("No skills to push.")
			return nil
		}

		// Print initial progress lines
		lines := make([]progressLine, len(skills))
		for i, s := range skills {
			lines[i] = progressLine{name: s.name, status: "waiting", pct: 0}
		}
		for _, l := range lines {
			fmt.Printf("  %-20s  %s  %s\n", l.name, renderBar(0), "waiting")
		}

		var pushed, created, conflicts, failed int

		for i, s := range skills {
			lines[i].status = "compressing"
			lines[i].pct = 0.3
			renderProgress(lines)

			// Create tar.gz of the skill folder
			archive, err := createTarGz(s.dir)
			if err != nil {
				lines[i].status = "failed"
				lines[i].pct = 0
				renderProgress(lines)
				failed++
				continue
			}

			archiveSize := int64(len(archive))

			if !s.hasMarker || (s.marker != nil && s.marker.SkillID == "") {
				// New skill or sourced skill (installed via add without account)
				lines[i].status = "creating"
				lines[i].pct = 0.5
				renderProgress(lines)

				content, _ := os.ReadFile(filepath.Join(s.dir, "SKILL.md"))

				// If sourced, pass forked_from so the server links to the original
				var forkedFrom string
				if s.marker != nil && s.marker.Source != nil {
					forkedFrom = s.marker.Source.ID
				}

				skill, err := client.createSkillFull(s.name, "", string(content), []string{"claude-code"}, forkedFrom)
				if err != nil {
					lines[i].status = "failed"
					renderProgress(lines)
					failed++
					continue
				}

				// Write marker — preserve source, add skill_id
				newMarker := &airskillsMarker{SkillID: skill.ID, Version: skill.Version, Tool: "claude-code"}
				if s.marker != nil {
					newMarker.Source = s.marker.Source
				}
				s.marker = newMarker
				writeMarker(filepath.Join(s.dir, ".airskills"), s.marker)
				created++
			} else {
				// Existing skill — push with version check
				lines[i].status = "pushing"
				lines[i].pct = 0.5
				renderProgress(lines)

				content, _ := os.ReadFile(filepath.Join(s.dir, "SKILL.md"))
				payload := map[string]interface{}{
					"content": string(content),
				}
				if !pushForce {
					payload["expected_version"] = s.marker.Version
				}

				body, statusCode, err := client.put(
					fmt.Sprintf("/api/v1/skills/%s", s.marker.SkillID),
					payload,
				)
				if err != nil {
					lines[i].status = "failed"
					renderProgress(lines)
					failed++
					continue
				}

				if statusCode == 409 {
					var conflict struct {
						RemoteVersion string `json:"remote_version"`
					}
					json.Unmarshal(body, &conflict)
					lines[i].status = "CONFLICT"
					renderProgress(lines)

					// Download remote version to tmp for merge
					tmpDir := filepath.Join(os.TempDir(), "airskills-conflicts", s.name)
					os.MkdirAll(tmpDir, 0755)
					remoteContent, err := client.getSkillContent(s.marker.SkillID)
					if err == nil {
						tmpPath := filepath.Join(tmpDir, "SKILL.md")
						os.WriteFile(tmpPath, []byte(remoteContent), 0644)
						conflictMessages = append(conflictMessages, conflictInfo{
							name:       s.name,
							localPath:  filepath.Join(s.dir, "SKILL.md"),
							remotePath: tmpPath,
							localVer:   s.marker.Version,
							remoteVer:  conflict.RemoteVersion,
						})
					}
					conflicts++
					continue
				}

				if statusCode >= 400 {
					lines[i].status = "failed"
					renderProgress(lines)
					failed++
					continue
				}

				// Update marker with new version
				var updated struct {
					Version string `json:"version"`
				}
				json.Unmarshal(body, &updated)
				s.marker.Version = updated.Version
				writeMarker(filepath.Join(s.dir, ".airskills"), s.marker)
				pushed++
			}

			// Upload archive
			lines[i].status = "uploading"
			lines[i].pct = 0.7
			renderProgress(lines)

			err = uploadArchive(client, s.marker.SkillID, archive)
			if err != nil {
				// Non-fatal — metadata pushed, archive upload failed
				lines[i].status = fmt.Sprintf("done (archive failed)")
				lines[i].size = formatSize(archiveSize)
				lines[i].pct = 1
				renderProgress(lines)
				continue
			}

			lines[i].status = "done"
			lines[i].size = formatSize(archiveSize)
			lines[i].pct = 1
			renderProgress(lines)
		}

		fmt.Printf("\n%d pushed, %d created, %d conflicts, %d failed\n",
			pushed, created, conflicts, failed)

		// Show conflict resolution instructions
		if len(conflictMessages) > 0 {
			fmt.Println("\n--- Conflicts ---")
			for _, c := range conflictMessages {
				fmt.Printf("\n  %s (local v%s, remote v%s)\n", c.name, c.localVer, c.remoteVer)
				fmt.Printf("  Local:  %s\n", c.localPath)
				fmt.Printf("  Remote: %s\n", c.remotePath)

				// Show a brief diff summary
				localData, _ := os.ReadFile(c.localPath)
				remoteData, _ := os.ReadFile(c.remotePath)
				localLines := len(strings.Split(string(localData), "\n"))
				remoteLines := len(strings.Split(string(remoteData), "\n"))
				fmt.Printf("  Local: %d lines, Remote: %d lines\n", localLines, remoteLines)

				fmt.Printf("\n  To resolve, tell your AI coding agent:\n")
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
	pushCmd.Flags().BoolVar(&pushForce, "force", false, "Skip version check (use after resolving conflicts)")
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

		// Use relative path inside the archive
		rel, _ := filepath.Rel(dir, path)
		header.Name = filepath.Join(base, rel)

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

func uploadArchive(client *apiClient, skillID string, data []byte) error {
	url := client.baseURL + fmt.Sprintf("/api/v1/skills/%s/archive", skillID)
	req, err := http.NewRequest("PUT", url, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+client.token)
	req.Header.Set("Content-Type", "application/gzip")

	resp, err := client.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("upload failed (%d): %s", resp.StatusCode, string(body))
	}
	return nil
}

func writeMarker(path string, marker *airskillsMarker) {
	data, _ := json.MarshalIndent(marker, "", "  ")
	os.WriteFile(path, data, 0644)
}

