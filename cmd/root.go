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
	Short: "Manage AI coding skills across Claude Code, Cursor, and Copilot",
	Long: `Airskills manages your AI coding skills from a single source of truth.

Add skills from Git repos, sync them to every AI tool's expected directory,
and keep everything up to date with one command.`,
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
