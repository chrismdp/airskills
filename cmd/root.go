package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

var rootCmd = &cobra.Command{
	Use:   "airskills",
	Short: "Manage AI skills across Claude Code, Cursor, Copilot, Cowork, and more",
	Long: `Airskills manages your AI skills from a single source of truth.

Get started:
  airskills sync       Log in (if needed) and sync your skills
  airskills add u/s    Install a public skill
  airskills status     Check for updates

Works with 18 AI agents.`,
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

	rootCmd.PersistentPreRun = func(cmd *cobra.Command, args []string) {
		name := cmd.Name()
		if name != "self-update" && name != "version" {
			checkForUpdates()
		}
	}

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func init() {
	rootCmd.AddCommand(versionCmd)
}

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the version number",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Printf("airskills %s (%s, %s)\n", version, commit, date)
	},
}
