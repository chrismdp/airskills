package cmd

import (
	"fmt"

	"github.com/chrismdp/airskills/config"
	"github.com/spf13/cobra"
)

func init() {
	configCmd.AddCommand(configSetCmd)
	configCmd.AddCommand(configGetCmd)
	configCmd.AddCommand(configPathCmd)
	rootCmd.AddCommand(configCmd)
}

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Manage airskills configuration",
}

var configSetCmd = &cobra.Command{
	Use:   "set <key> <value>",
	Short: "Set a configuration value",
	Long: `Set a configuration value. Valid keys:
  api_url - API endpoint (default: https://airskills.ai)`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		key, value := args[0], args[1]

		cfg, err := config.Load()
		if err != nil {
			return err
		}

		switch key {
		case "api_url":
			cfg.APIURL = value
		default:
			return fmt.Errorf("unknown config key: %s (valid: api_url)", key)
		}

		if err := cfg.Save(); err != nil {
			return fmt.Errorf("failed to save config: %w", err)
		}

		fmt.Printf("Set %s = %s\n", key, value)
		return nil
	},
}

var configGetCmd = &cobra.Command{
	Use:   "get <key>",
	Short: "Get a configuration value",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}

		switch args[0] {
		case "api_url":
			fmt.Println(cfg.APIURL)
		default:
			return fmt.Errorf("unknown config key: %s", args[0])
		}
		return nil
	},
}

var configPathCmd = &cobra.Command{
	Use:   "path",
	Short: "Show the config directory path",
	RunE: func(cmd *cobra.Command, args []string) error {
		dir, err := config.Dir()
		if err != nil {
			return err
		}
		fmt.Println(dir)
		return nil
	},
}
