package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/chrismdp/airskills/config"
	"github.com/spf13/cobra"
)

const posthogAPIKey = "" // TODO: set PostHog project API key
const posthogEndpoint = "https://eu.i.posthog.com/capture/"

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

		// Build PostHog event
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

		// Try to get user identity from token
		distinctID := "anonymous"
		token, _ := config.LoadToken()
		if token != nil && time.Now().Unix() < token.ExpiresAt {
			distinctID = token.AccessToken[:16] // truncated token as stable ID
		}

		event := map[string]interface{}{
			"api_key":     posthogAPIKey,
			"event":       "cli_feedback",
			"distinct_id": distinctID,
			"properties":  properties,
			"timestamp":   time.Now().UTC().Format(time.RFC3339),
		}

		data, err := json.Marshal(event)
		if err != nil {
			return err
		}

		resp, err := http.Post(posthogEndpoint, "application/json", bytes.NewReader(data))
		if err != nil {
			return fmt.Errorf("failed to send feedback: %w", err)
		}
		resp.Body.Close()

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
