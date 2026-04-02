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

Run 'airskills' with no arguments to log in and sync your skills.
Works with 18 AI agents.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		// Check if logged in
		_, err := newAPIClientAuto()
		if err != nil {
			// Not logged in — run the login flow
			fmt.Println("Welcome to airskills! Let's get you set up.")
			fmt.Println()
			if err := loginCmd.RunE(cmd, args); err != nil {
				return err
			}
			fmt.Println()
		}

		// Now sync
		return syncCmd.RunE(cmd, args)
	},
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
