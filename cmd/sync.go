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

// syncActiveConflicts is non-nil for the duration of a `sync` run and
// tracks every slug that must be skipped this sync — both mirror
// conflicts (divergent local copies) AND pull divergences (3-way splits
// detected by decidePullActions). Pull populates it; push reads from
// it and skips both its own mirror call and any matching upload. This
// guarantees mirror runs exactly once per sync (pull runs it pre-pull
// after pull's rename pass) and that a 3-way divergence produces
// exactly one conflict dir — pull's, not a second one from push's 409
// path. See platform/doc/changes/cli-mirror-cannot-distinguish-delete-from-never-installed.md
// for the order flip rationale.
//
// Standalone `airskills push` and `airskills pull` leave this nil, so
// each runs its own mirror.
var syncActiveConflicts map[string]bool

// The default guide skill that gets auto-installed on first sync.
const guideOwner = "chrismdp"
const guideSlug = "airskills-guide"

func init() {
	syncCmd.Flags().BoolVarP(&syncVerbose, "verbose", "v", false, "Show per-skill progress")
	syncCmd.Flags().StringVar(&skillsetFlag, "skillset", "", "Personal skillset to sync against; sets the default for future runs (default: your last-used skillset)")
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
	setStandardHeaders(req)

	resp, err := doRequest(http.DefaultClient, req)
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

		// Mark this process as inside a sync invocation. Pull populates the
		// conflict set (mirror + divergence); push reads from it and skips
		// both its own mirror call and any matching upload.
		syncActiveConflicts = map[string]bool{}
		defer func() { syncActiveConflicts = nil }()

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

		// Auto-install the guide and check GitHub-sourced skills BEFORE
		// pull so pull's classifier sees the post-install state. Both are
		// silent no-ops when there's nothing to do. autoInstallGuide is
		// gated on canPush (it writes to the user's server account).
		if canPush {
			autoInstallGuide()
		}
		syncGitHubSkills()

		fmt.Printf("%s %s\n", cyan("▼"), "Pull")
		if err := runPull(cmd, args); err != nil {
			return err
		}

		if canPush {
			fmt.Printf("\n%s %s\n", cyan("▲"), "Push")
			if err := pushCmd.RunE(cmd, args); err != nil {
				return err
			}
		} else {
			fmt.Printf("\n%s %s\n", dim("▲"), dim("Push skipped (not logged in)"))
			fmt.Printf("  %s\n", dim("Log in to push your skills, back up, and share: airskills login"))
		}

		// Surface sourced skills whose upstream has moved past the
		// user's last resolved point. Terse for TTY humans; verbose for
		// agents (no-TTY) and explicit --verbose runs, with "don't
		// guess — ask the user" affordance.
		printPendingReviewSummary(verboseEnabled(syncVerbose))

		telemetry.Capture("cli_sync", map[string]interface{}{
			"pushed": canPush,
		})

		// Next-step hints for an agent. `sync` is normally a terminal
		// action — most agents run it to reach steady state, so the
		// primary nudge is to verify with status. Login-gated side paths
		// show up too when relevant.
		steps := []agentNextStep{
			{Cmd: "airskills status", Why: "confirm everything is in sync"},
		}
		if !canPush {
			steps = append(steps, agentNextStep{
				Cmd: "airskills login",
				Why: "log in to back up and push local changes",
			})
		}
		printAgentNextSteps(os.Stdout, steps)

		return nil
	},
}
