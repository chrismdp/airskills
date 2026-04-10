package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chrismdp/airskills/telemetry"
	"github.com/spf13/cobra"
)

var reviewCmd = &cobra.Command{
	Use:   "review [skill]",
	Short: "Review suggestions submitted against your skills",
	Long: `Review suggestions from other users against your skills.

Running 'airskills review' with no arguments lists all pending suggestions
and prints the full agent-friendly workflow. Narrow to a single skill by
passing its local directory name.

Subcommands let an agent fetch a suggestion's files, mark it accepted, or
decline it with a response message.`,
	RunE: runReviewList,
}

var reviewDownloadCmd = &cobra.Command{
	Use:   "download <suggestion-id>",
	Short: "Download a suggestion's files to a temp directory for review",
	Args:  cobra.ExactArgs(1),
	RunE:  runReviewDownload,
}

var reviewAcceptCmd = &cobra.Command{
	Use:   "accept <suggestion-id>",
	Short: "Mark a suggestion as accepted",
	Long: `Mark a suggestion as accepted. Bookkeeping only — you should have already
merged the changes into your local skill and pushed. Nothing auto-pushes.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runReviewResolve(args[0], "accepted", "")
	},
}

var reviewDeclineCmd = &cobra.Command{
	Use:   "decline <suggestion-id>",
	Short: "Mark a suggestion as declined",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runReviewResolve(args[0], "declined", reviewDeclineMessage)
	},
}

var reviewDeclineMessage string

const reviewGuide = `=== How to review and merge suggestions ===

You can batch multiple suggestions into a single push — that's the
intended workflow. Read all pending suggestions, merge what you want
from each, push once, then accept/decline each individually.

For each suggestion:

  1. Download the suggested version:
       airskills review download <suggestion-id>
     Prints a tmp path containing the suggester's files.

  2. Read both the suggested files and your current skill files.
     The suggestion was built against a specific version hash of your
     skill — shown above. Your current version may have moved on.

  3. Decide what to incorporate. Merge desired changes into your
     local skill directory — or replace entirely, or leave as-is.
     Nothing auto-merges; you stay in control of versioning and the
     changelog.

  4. Once you've merged everything you want from all suggestions,
     push your changes:
       airskills push

  5. Mark each suggestion resolved:
       airskills review accept <suggestion-id>
       airskills review decline <suggestion-id> --message "why"

`

func init() {
	reviewDeclineCmd.Flags().StringVar(&reviewDeclineMessage, "message", "", "Optional reason shown to the contributor")
	reviewCmd.AddCommand(reviewDownloadCmd)
	reviewCmd.AddCommand(reviewAcceptCmd)
	reviewCmd.AddCommand(reviewDeclineCmd)
	rootCmd.AddCommand(reviewCmd)
}

func runReviewList(cmd *cobra.Command, args []string) error {
	client, err := newAPIClientAuto()
	if err != nil {
		return err
	}

	var skillFilter string
	if len(args) == 1 {
		syncState := loadSyncState()
		if entry, ok := syncState.Skills[args[0]]; ok && entry.SkillID != "" {
			skillFilter = entry.SkillID
		} else {
			return fmt.Errorf("skill %q not found locally (use the directory name)", args[0])
		}
	}

	suggestions, err := client.listSuggestions("owner", "pending", skillFilter)
	if err != nil {
		return fmt.Errorf("fetching suggestions: %w", err)
	}

	fmt.Println("=== Pending suggestions ===")
	fmt.Println()
	if len(suggestions) == 0 {
		label := "no pending suggestions"
		if skillFilter != "" {
			label += " for " + args[0]
		}
		fmt.Printf("  %s %s\n\n", green("✓"), label)
	} else {
		for i, s := range suggestions {
			who := s.SuggesterUsername
			if who == "" {
				who = "someone"
			}
			skillName := s.OwnerSkillName
			if skillName == "" {
				skillName = s.OwnerSkillID
			}
			fmt.Printf("  %d. %s suggested changes to %q (%s)\n",
				i+1, who, skillName, suggestionAge(s.CreatedAt))
			if s.Message != "" {
				fmt.Printf("     \"%s\"\n", s.Message)
			}
			fmt.Printf("     Suggestion ID: %s\n", s.ID)
			fmt.Printf("     Based on your version hash: %s\n", shortHash(s.BaseContentHash))
			fmt.Println()
		}
	}

	fmt.Print(reviewGuide)

	telemetry.Capture("cli_review_list", map[string]interface{}{
		"pending_count": len(suggestions),
		"filtered":      skillFilter != "",
	})
	return nil
}

func runReviewDownload(cmd *cobra.Command, args []string) error {
	client, err := newAPIClientAuto()
	if err != nil {
		return err
	}
	suggestionID := args[0]

	s, err := client.getSuggestion(suggestionID)
	if err != nil {
		return fmt.Errorf("fetching suggestion: %w", err)
	}

	// RLS grants owner access to the suggester's skill while pending.
	files, err := downloadSkillFiles(client, s.SuggesterSkillID)
	if err != nil {
		return fmt.Errorf("downloading suggestion files: %w", err)
	}
	if len(files) == 0 {
		return fmt.Errorf("suggestion has no files")
	}

	tmpDir, err := os.MkdirTemp("", "airskills-suggestion-"+suggestionID+"-")
	if err != nil {
		return fmt.Errorf("creating tmp dir: %w", err)
	}
	if err := writeFilesToDir(tmpDir, files); err != nil {
		return fmt.Errorf("writing suggestion files: %w", err)
	}

	fmt.Printf("Downloaded suggestion to:\n  %s\n\n", tmpDir)
	fmt.Printf("Based on your version hash: %s\n", shortHash(s.BaseContentHash))
	if s.Message != "" {
		fmt.Printf("Message from suggester: %q\n", s.Message)
	}
	fmt.Println()
	fmt.Println("Read the files, merge what you want into your local skill, push,")
	fmt.Println("then run 'airskills review accept' or 'airskills review decline'.")

	telemetry.Capture("cli_review_download", map[string]interface{}{
		"suggestion_id": suggestionID,
		"file_count":    len(files),
	})
	return nil
}

// runReviewResolve implements the common body of accept and decline.
func runReviewResolve(id, status, message string) error {
	client, err := newAPIClientAuto()
	if err != nil {
		return err
	}
	s, err := client.updateSuggestion(id, status, message)
	if err != nil {
		return fmt.Errorf("%s suggestion: %w", status, err)
	}

	var symbol string
	if status == "accepted" {
		symbol = green("✓")
	} else {
		symbol = yellow("✗")
	}
	fmt.Printf("%s Suggestion %s marked as %s\n", symbol, id, status)
	if s.OwnerSkillName != "" {
		fmt.Printf("  %s\n", s.OwnerSkillName)
	}
	if message != "" {
		fmt.Printf("  Reason sent to contributor: %q\n", message)
	}

	telemetry.Capture("cli_review_resolve", map[string]interface{}{
		"suggestion_id": id,
		"status":        status,
		"has_message":   message != "",
	})
	return nil
}

// suggestionAge wraps formatAge so the list survives a bad timestamp instead
// of blowing up — the string "recently" is unambiguous and harmless.
func suggestionAge(ts string) string {
	t, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return "recently"
	}
	return formatAge(t)
}

func shortHash(h string) string {
	h = strings.TrimSpace(h)
	if len(h) <= 8 {
		return h
	}
	return h[:8]
}

// writeFilesToDir materialises a files map into dir, creating intermediate
// directories as needed. Shared by review download, pull divergence, and
// export flows — same pattern, avoid duplicating.
func writeFilesToDir(dir string, files map[string][]byte) error {
	for relPath, data := range files {
		target := filepath.Join(dir, relPath)
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}
		if err := os.WriteFile(target, data, 0644); err != nil {
			return err
		}
	}
	return nil
}
