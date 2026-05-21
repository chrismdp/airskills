package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"

	"github.com/spf13/cobra"
)

func init() {
	diffCmd.Flags().BoolP("color", "c", true, "Use color in diff output")
	diffCmd.Flags().String("commit", "", "Diff against a specific commit ID (default: latest)")
	rootCmd.AddCommand(diffCmd)
}

var diffCmd = &cobra.Command{
	Use:   "diff <skill-name>",
	Short: "Show local changes against the server version",
	Long: `Downloads the current server version of a skill and diffs it against
your local files. Shows what would be pushed.

Checks ALL agent copies (Claude Code, pi, etc.), not just the first
one found. Each copy that differs from the server is shown separately.

Use --no-color to suppress ANSI color codes (e.g. for scripts).`,
	Args: cobra.ExactArgs(1),
	RunE: runDiff,
}

func runDiff(cmd *cobra.Command, args []string) error {
	skillName := args[0]
	useColor, _ := cmd.Flags().GetBool("color")
	commitID, _ := cmd.Flags().GetString("commit")

	client, err := newAPIClientAuto()
	if err != nil {
		return fmt.Errorf("not logged in — run 'airskills login' first: %w", err)
	}

	// Resolve the skill ID from sync state
	syncState := loadSyncState()
	entry, ok := syncState.Skills[skillName]
	if !ok || entry == nil || entry.SkillID == "" {
		return fmt.Errorf("skill %q not found in sync state — is it tracked by airskills? Run 'airskills list'", skillName)
	}

	var resolvedCommitID string
	if commitID != "" {
		resolvedCommitID, err = resolveCommitID(client, entry.SkillID, commitID)
		if err != nil {
			return err
		}
	} else {
		commits, err := client.getVersionHistory(entry.SkillID)
		if err != nil {
			return fmt.Errorf("fetching version history: %w", err)
		}
		if len(commits) == 0 {
			return fmt.Errorf("no versions found for %q on the server", skillName)
		}
		resolvedCommitID = commits[0].Id.String()
	}

	// Download server version
	serverFiles, err := client.getVersionContent(entry.SkillID, resolvedCommitID)
	if err != nil {
		return fmt.Errorf("downloading server version: %w", err)
	}

	// Write server files to a temp dir for diffing
	tmpDir, err := os.MkdirTemp("", "airskills-diff-"+skillName+"-")
	if err != nil {
		return fmt.Errorf("creating temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	for path, content := range serverFiles {
		fullPath := filepath.Join(tmpDir, path)
		if err := os.MkdirAll(filepath.Dir(fullPath), 0700); err != nil {
			return fmt.Errorf("creating temp dir: %w", err)
		}
		if err := os.WriteFile(fullPath, content, 0600); err != nil {
			return fmt.Errorf("writing temp file: %w", err)
		}
	}

	// Find ALL local skill copies across all agents (not just first-found)
	allCopies, _, err := scanSkillsAllPaths()
	if err != nil {
		return fmt.Errorf("scanning local skills: %w", err)
	}
	localDirs, ok := allCopies[skillName]
	if !ok || len(localDirs) == 0 {
		return fmt.Errorf("skill %q not found locally", skillName)
	}

	// For each local copy, diff against server
	anyChanged := false
	for _, localDir := range localDirs {
		localFiles := readSkillFiles(localDir)

		fileNames := map[string]bool{}
		for p := range serverFiles {
			fileNames[p] = true
		}
		for p := range localFiles {
			fileNames[p] = true
		}
		sortedNames := make([]string, 0, len(fileNames))
		for p := range fileNames {
			sortedNames = append(sortedNames, p)
		}
		sort.Strings(sortedNames)

		copyChanged := false
		for _, path := range sortedNames {
			serverPath := filepath.Join(tmpDir, path)
			localPath := filepath.Join(localDir, path)

			_, serverExists := serverFiles[path]
			_, localExists := localFiles[path]

			if serverExists && localExists {
				diffOutput, err := diffFiles(serverPath, localPath, useColor)
				if err != nil {
					return fmt.Errorf("diffing %s: %w", path, err)
				}
				if diffOutput != "" {
					if !copyChanged {
						fmt.Printf("=== %s (%s) ===\n", skillName, localDir)
					}
					fmt.Printf("\n--- %s ---\n%s", path, diffOutput)
					copyChanged = true
					anyChanged = true
				}
			} else if !serverExists && localExists {
				if !copyChanged {
					fmt.Printf("=== %s (%s) ===\n", skillName, localDir)
				}
				fmt.Printf("\n+++ %s (local-only)\n", path)
				copyChanged = true
				anyChanged = true
			} else if serverExists && !localExists {
				if !copyChanged {
					fmt.Printf("=== %s (%s) ===\n", skillName, localDir)
				}
				fmt.Printf("\n--- %s (server-only)\n", path)
				copyChanged = true
				anyChanged = true
			}
		}
	}

	if !anyChanged {
		fmt.Printf("%s %s is in sync with the server (commit %s)\n",
			green("✓"), skillName, resolvedCommitID[:8])
	}

	// Surface what .askignore / .gitignore is hiding so silent skipping
	// doesn't become debug hell. One block per local copy.
	for _, localDir := range localDirs {
		entries := listIgnoredFiles(localDir)
		if len(entries) == 0 {
			continue
		}
		fmt.Printf("\n# ignored (%s)\n", localDir)
		for _, e := range entries {
			fmt.Printf("  %s  (%s)\n", e.Path, e.Reason)
		}
	}

	return nil
}

// diffFiles runs diff -u between two files. Returns empty string if identical.
func diffFiles(oldPath, newPath string, color bool) (string, error) {
	args := []string{"-u"}
	if color {
		args = append(args, "--color=always")
	}
	args = append(args, oldPath, newPath)

	out, err := exec.Command("diff", args...).Output() //nolint:gosec
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			// diff exits 0 = identical, 1 = differences, 2 = error
			if exitErr.ExitCode() == 1 {
				return string(out), nil
			}
			return "", fmt.Errorf("diff failed: %w\n%s", err, string(exitErr.Stderr))
		}
		return "", err
	}
	// exit 0 = identical
	return "", nil
}
