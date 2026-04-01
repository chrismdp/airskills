package cmd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chrismdp/airskills/config"
)

type updateState struct {
	CheckedAt     string `json:"checked_at"`
	LatestVersion string `json:"latest_version"`
}

const updateCheckInterval = 24 * time.Hour
const releasesURL = "https://api.github.com/repos/chrismdp/airskills/releases/latest"

func checkForUpdates() {
	if version == "dev" {
		return
	}

	dir, err := config.Dir()
	if err != nil {
		return
	}
	statePath := filepath.Join(dir, "update_state.json")

	data, err := os.ReadFile(statePath)
	if err == nil {
		var state updateState
		if json.Unmarshal(data, &state) == nil {
			// If a newer version is known, print hint
			if state.LatestVersion != "" && isNewer(state.LatestVersion, version) {
				fmt.Fprintf(os.Stderr, "\nA new version of airskills is available: %s (you have %s)\nRun \"airskills self-update\" to update.\n\n", state.LatestVersion, version)
			}

			// If checked recently, don't check again
			if t, err := time.Parse(time.RFC3339, state.CheckedAt); err == nil {
				if time.Since(t) < updateCheckInterval {
					return
				}
			}
		}
	}

	// Background check — don't block the command
	go func() {
		latest := fetchLatestVersion()
		if latest == "" {
			return
		}
		state := updateState{
			CheckedAt:     time.Now().UTC().Format(time.RFC3339),
			LatestVersion: latest,
		}
		data, _ := json.Marshal(state)
		os.WriteFile(statePath, data, 0644)
	}()
}

func fetchLatestVersion() string {
	client := &http.Client{Timeout: 5 * time.Second}
	req, err := http.NewRequest("GET", releasesURL, nil)
	if err != nil {
		return ""
	}
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := client.Do(req)
	if err != nil || resp.StatusCode != 200 {
		return ""
	}
	defer resp.Body.Close()

	var release struct {
		TagName string `json:"tag_name"`
	}
	if json.NewDecoder(resp.Body).Decode(&release) != nil {
		return ""
	}
	return strings.TrimPrefix(release.TagName, "v")
}

// isNewer returns true if remote > local using simple semver comparison.
func isNewer(remote, local string) bool {
	rParts := parseSemver(remote)
	lParts := parseSemver(local)
	for i := 0; i < 3; i++ {
		if rParts[i] > lParts[i] {
			return true
		}
		if rParts[i] < lParts[i] {
			return false
		}
	}
	return false
}

func parseSemver(v string) [3]int {
	v = strings.TrimPrefix(v, "v")
	var parts [3]int
	fmt.Sscanf(v, "%d.%d.%d", &parts[0], &parts[1], &parts[2])
	return parts
}
