package cmd

import (
	"fmt"

	"github.com/chrismdp/airskills/config"
	"github.com/spf13/cobra"
)

var syncVerbose bool

func init() {
	syncCmd.Flags().BoolVarP(&syncVerbose, "verbose", "v", false, "Show per-skill progress")
	rootCmd.AddCommand(syncCmd)
}

var syncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Push local changes and pull remote skills",
	Long:  "Uploads local skills to your account (if logged in), then downloads remote skills to this machine.",
	RunE: func(cmd *cobra.Command, args []string) error {
		verbose = syncVerbose

		token, _ := config.LoadToken()
		loggedIn := token != nil

		if loggedIn {
			fmt.Printf("%s %s\n", cyan("▲"), "Push")
			if err := pushCmd.RunE(cmd, args); err != nil {
				return err
			}
		} else {
			fmt.Printf("%s %s\n", dim("▲"), dim("Push skipped (not logged in)"))
			fmt.Printf("  %s\n", dim("Log in to push your skills, back up, and share: airskills login"))
		}

		fmt.Printf("\n%s %s\n", cyan("▼"), "Pull")
		if err := runPull(cmd, args); err != nil {
			return err
		}

		return nil
	},
}
