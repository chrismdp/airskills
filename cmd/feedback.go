package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/chrismdp/airskills/config"
	"github.com/chrismdp/airskills/telemetry"
	"github.com/spf13/cobra"
)

var feedbackMessage string
var feedbackIncludeLogs bool

var feedbackCmd = &cobra.Command{
	Use:   "feedback",
	Short: "Send feedback to the airskills team",
	Long:  "Send a message to the airskills team. Use --include-logs to attach recent CLI log files.",
	RunE: func(cmd *cobra.Command, args []string) error {
		if feedbackMessage == "" {
			return fmt.Errorf("--message is required")
		}

		properties := map[string]interface{}{
			"message": feedbackMessage,
		}

		if feedbackIncludeLogs {
			logs, err := readRecentLogs()
			if err != nil {
				fmt.Printf("Warning: could not read logs: %v\n", err)
			} else if logs != "" {
				properties["logs"] = logs
			}
		}

		telemetry.Capture("cli_feedback", properties)
		fmt.Println("Thanks! Feedback sent.")
		return nil
	},
}

func readRecentLogs() (string, error) {
	dir, err := config.Dir()
	if err != nil {
		return "", err
	}

	logsDir := filepath.Join(dir, "logs")
	entries, err := os.ReadDir(logsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name() > entries[j].Name()
	})

	var parts []string
	limit := 3
	if len(entries) < limit {
		limit = len(entries)
	}

	for _, e := range entries[:limit] {
		data, err := os.ReadFile(filepath.Join(logsDir, e.Name()))
		if err != nil {
			continue
		}
		content := string(data)
		if len(content) > 10240 {
			content = content[len(content)-10240:]
		}
		parts = append(parts, fmt.Sprintf("=== %s ===\n%s", e.Name(), content))
	}

	return strings.Join(parts, "\n\n"), nil
}

func init() {
	feedbackCmd.Flags().StringVarP(&feedbackMessage, "message", "m", "", "Feedback message")
	feedbackCmd.Flags().BoolVar(&feedbackIncludeLogs, "include-logs", false, "Attach recent CLI log files")
	rootCmd.AddCommand(feedbackCmd)
}
