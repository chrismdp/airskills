package cmd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/chrismdp/airskills/config"
	"github.com/chrismdp/airskills/telemetry"
	"github.com/spf13/cobra"
)

var syncVerbose bool

// The default guide skill that gets auto-installed on first sync.
const guideOwner = "chrismdp"
const guideSlug = "airskills-guide"

func init() {
	syncCmd.Flags().BoolVarP(&syncVerbose, "verbose", "v", false, "Show per-skill progress")
	syncCmd.Flags().StringVar(&skillsetFlag, "skillset", "", "Personal skillset to sync against (default: your last-used skillset)")
	rootCmd.AddCommand(syncCmd)
}

// autoInstallGuide silently installs the airskills guide skill if the user
// doesn't already have it. Runs once on first sync after login. Errors are
// swallowed — the guide is a convenience, not a hard dependency.
func autoInstallGuide() {
	dirName := namespacedSlug(guideOwner, guideSlug)
	syncState := loadSyncState()
	if _, ok := syncState.Skills[dirName]; ok {
		return // already installed (namespaced key)
	}
	if _, ok := syncState.Skills[guideSlug]; ok {
		return // already installed (old bare-slug key)
	}

	cfg, err := config.Load()
	if err != nil {
		return
	}
	token, _ := config.LoadToken()
	var authHeader string
	if token != nil && time.Now().Unix() < token.ExpiresAt {
		authHeader = "Bearer " + token.AccessToken
	}

	// Resolve the guide skill
	resolveURL := fmt.Sprintf("%s/api/v1/resolve/%s/%s", cfg.APIURL, guideOwner, guideSlug)
	req, err := http.NewRequest("GET", resolveURL, nil)
	if err != nil {
		return
	}
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	setAnonHeader(req)

	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 {
		return // guide not published yet or network issue — skip silently
	}
	defer resp.Body.Close()

	var result struct {
		ID      string `json:"id"`
		Slug    string `json:"slug"`
		Content string `json:"content"`
		Version string `json:"version"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return
	}

	// Download files
	lines := []progressLine{{name: guideSlug, status: "downloading", pct: 0}}
	files, err := fetchSkillFiles(cfg, result.ID, result.Content, authHeader, lines)
	if err != nil {
		return
	}

	// Install silently
	installSkillToAgents(dirName, files)

	// Update sync state
	syncState.Skills[dirName] = &SyncEntry{
		Version: result.Version,
		Tool:    "claude-code",
		Source: &skillSource{
			Owner: guideOwner,
			Slug:  guideSlug,
			ID:    result.ID,
		},
	}
	saveSyncState(syncState)

	fmt.Printf("  %s %s\n", green("✓"), dim("Installed airskills guide"))
}

var syncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Push local changes and pull remote skills",
	Long:  "Uploads local skills to your account (if logged in), then downloads remote skills to this machine.",
	RunE: func(cmd *cobra.Command, args []string) error {
		verbose = syncVerbose

		// Check if we can authenticate (handles no token, expired token, failed refresh)
		_, authErr := newAPIClientAuto()
		canPush := authErr == nil

		// Resolve the --skillset flag once up front so push and pull see a
		// consistent value and the confirmation prompt doesn't fire twice.
		// Errors from the prompt (cancel) should abort the whole sync.
		if canPush {
			cfg, cfgErr := config.Load()
			if cfgErr != nil {
				return cfgErr
			}
			if _, err := resolveSkillsetFlag(cfg, skillsetFlag, stdinReader(), stderrWriter()); err != nil {
				fmt.Fprintln(os.Stderr, err)
				return err
			}
		}

		if canPush {
			fmt.Printf("%s %s\n", cyan("▲"), "Push")
			if err := pushCmd.RunE(cmd, args); err != nil {
				return err
			}

			// Auto-install the airskills guide on first sync (silently)
			autoInstallGuide()
		} else {
			fmt.Printf("%s %s\n", dim("▲"), dim("Push skipped (not logged in)"))
			fmt.Printf("  %s\n", dim("Log in to push your skills, back up, and share: airskills login"))
		}

		// Check GitHub-sourced skills for upstream updates
		syncGitHubSkills()

		fmt.Printf("\n%s %s\n", cyan("▼"), "Pull")
		if err := runPull(cmd, args); err != nil {
			return err
		}

		telemetry.Capture("cli_sync", map[string]interface{}{
			"pushed": canPush,
		})
		return nil
	},
}
