package cmd

import (
	"fmt"

	"github.com/chrismdp/airskills/config"
	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(syncCmd)
}

var syncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Push local changes and pull remote skills",
	Long:  "Logs in if needed, then runs push and pull — uploads local skills to your account, then downloads remote skills to this machine.",
	RunE: func(cmd *cobra.Command, args []string) error {
		// Check if logged in, prompt login if not
		token, _ := config.LoadToken()
		if token == nil {
			fmt.Println("Not logged in. Let's fix that.")
			fmt.Println()
			if err := loginCmd.RunE(cmd, args); err != nil {
				return err
			}
			fmt.Println()
		}

		fmt.Println("=== Push ===")
		if err := pushCmd.RunE(cmd, args); err != nil {
			return err
		}

		fmt.Println("\n=== Pull ===")
		if err := runPull(cmd, args); err != nil {
			return err
		}

		return nil
	},
}
