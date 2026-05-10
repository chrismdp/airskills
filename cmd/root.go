package cmd

import (
	"fmt"
	"os"
	"time"

	"github.com/chrismdp/airskills/telemetry"
	"github.com/spf13/cobra"
)

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"

	// noUpdate disables the per-command auto-update check (--no-update).
	// Companion to AIRSKILLS_NO_AUTO_UPDATE=1; the env var is for
	// shells/profiles, the flag for one-off CI invocations.
	noUpdate bool
)

var rootCmd = &cobra.Command{
	Use:   "airskills",
	Short: "Manage AI skills across Claude Code, Cursor, Copilot, Cowork, and more",
	Long: `Airskills manages your AI skills from a single source of truth.

Get started:
  airskills sync       Log in (if needed) and sync your skills
  airskills add u/s    Install a public skill
  airskills status     Check for updates

Works with 19 AI agents.

Hit a bug or got feedback?  airskills feedback --include-logs

Docs: https://airskills.ai/docs`,
	// Don't print usage on runtime errors (e.g. "skill not found").
	// Cobra still prints usage on argument-parse errors, which is correct.
	SilenceUsage: true,
	// Don't let cobra print "Error: ..." — Execute() prints the error itself
	// in a single, prefix-free line. Without this we double-print.
	SilenceErrors: true,
}

func Execute() {
	initLogging()
	if logFile != nil {
		defer logFile.Close()
	}

	// Skip telemetry init/flush for no-op commands so `airskills version`,
	// `--help`, and arg-parse errors don't pay the file-read / 2s-flush cost.
	if wantsTelemetry(os.Args[1:]) {
		telemetry.CLIVersion = version
		telemetry.Init()
		defer telemetry.Flush(2 * time.Second)
	}

	rootCmd.PersistentPreRun = func(cmd *cobra.Command, args []string) {
		if shouldSkipUpdateCheck(cmd.Name()) {
			return
		}
		// maybeAutoUpdate handles the active path (download + replace
		// when an update is known and the binary is user-writable).
		// checkForUpdates handles the passive path (print hint, kick
		// the daily background fetch). They're mutually exclusive: if
		// auto-update tried, we don't also print "new version available".
		if !maybeAutoUpdate() {
			checkForUpdates()
		}
	}

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// wantsTelemetry returns false for commands that don't need the telemetry
// subsystem — version, help, and argless invocations. This keeps those paths
// free of file I/O and the Flush wait.
func wantsTelemetry(args []string) bool {
	if len(args) == 0 {
		return false
	}
	switch args[0] {
	case "version", "--version", "-v", "help", "--help", "-h":
		return false
	}
	return true
}

// shouldSkipUpdateCheck reports whether PersistentPreRun's update flow
// (auto-update + passive hint) should be skipped for the named command.
// self-update has its own explicit version; version/help are non-actions
// that shouldn't pay the cost or surprise the user.
func shouldSkipUpdateCheck(cmdName string) bool {
	switch cmdName {
	case "self-update", "version", "help":
		return true
	}
	return false
}

func init() {
	rootCmd.PersistentFlags().BoolVar(&noUpdate, "no-update", false,
		"Skip the per-run auto-update check (also: AIRSKILLS_NO_AUTO_UPDATE=1)")
	rootCmd.AddCommand(versionCmd)
}

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the version number",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Printf("airskills %s (%s, %s)\n", version, commit, date)
	},
}
