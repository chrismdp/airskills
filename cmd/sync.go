package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(syncCmd)
}

var syncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Push local changes and pull remote skills",
	Long:  "Runs push then pull — uploads local skills to your account, then downloads remote skills to this machine.",
	RunE: func(cmd *cobra.Command, args []string) error {
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
