package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(installCmd)
}

// install is an alias for sync
var installCmd = &cobra.Command{
	Use:   "install",
	Short: "Alias for sync — push local changes and pull remote skills",
	RunE: func(cmd *cobra.Command, args []string) error {
		return syncCmd.RunE(cmd, args)
	},
}

// Shared helpers used by push, pull, sync, status

func extractDescription(content []byte) string {
	text := string(content)

	if strings.HasPrefix(text, "---") {
		parts := strings.SplitN(text, "---", 3)
		if len(parts) >= 3 {
			for _, line := range strings.Split(parts[1], "\n") {
				line = strings.TrimSpace(line)
				if strings.HasPrefix(line, "description:") {
					desc := strings.TrimPrefix(line, "description:")
					desc = strings.TrimSpace(desc)
					desc = strings.Trim(desc, "\"'")
					return desc
				}
			}
		}
	}

	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line == "---" || strings.HasPrefix(line, "#") {
			continue
		}
		if len(line) > 200 {
			return line[:200]
		}
		return line
	}
	return ""
}

func saveLastSync() error {
	dir, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	stateDir := filepath.Join(dir, ".config", "airskills")
	if err := os.MkdirAll(stateDir, 0700); err != nil {
		return err
	}
	ts := time.Now().UTC().Format(time.RFC3339)
	return os.WriteFile(filepath.Join(stateDir, "last_sync"), []byte(ts), 0600)
}

func loadLastSync() string {
	dir, err := os.UserHomeDir()
	if err != nil {
		return "1970-01-01T00:00:00Z"
	}
	data, err := os.ReadFile(filepath.Join(dir, ".config", "airskills", "last_sync"))
	if err != nil {
		return "1970-01-01T00:00:00Z"
	}
	return strings.TrimSpace(string(data))
}
