package cmd

import (
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/spf13/cobra"
)

func init() {
	historyCmd.Flags().IntP("limit", "n", 20, "Number of recent changes to show")
	rootCmd.AddCommand(historyCmd)
}

var historyCmd = &cobra.Command{
	Use:   "log",
	Short: "Show recent changes across all skills",
	Long:  "Fetches version history for all your skills and shows a unified timeline of recent changes.",
	RunE: func(cmd *cobra.Command, args []string) error {
		limit, _ := cmd.Flags().GetInt("limit")

		client, err := newAPIClientAuto()
		if err != nil {
			return err
		}

		skills, err := client.listSkills("personal")
		if err != nil {
			return fmt.Errorf("fetching skills: %w", err)
		}

		if len(skills) == 0 {
			fmt.Println("No skills found.")
			return nil
		}

		// Fetch version history for all skills in parallel
		type entry struct {
			skillName string
			commit    skillCommit
		}

		var mu sync.Mutex
		var all []entry
		var wg sync.WaitGroup
		sem := make(chan struct{}, 10)

		for _, s := range skills {
			s := s
			wg.Add(1)
			sem <- struct{}{}
			go func() {
				defer wg.Done()
				defer func() { <-sem }()
				commits, err := client.getVersionHistory(s.ID)
				if err != nil || len(commits) == 0 {
					return
				}
				mu.Lock()
				for _, c := range commits {
					all = append(all, entry{skillName: s.Name, commit: c})
				}
				mu.Unlock()
			}()
		}
		wg.Wait()

		if len(all) == 0 {
			fmt.Println("No history yet.")
			return nil
		}

		// Sort by created_at descending
		sort.Slice(all, func(i, j int) bool {
			return all[i].commit.CreatedAt > all[j].commit.CreatedAt
		})

		if len(all) > limit {
			all = all[:limit]
		}

		for _, e := range all {
			ts, _ := time.Parse(time.RFC3339Nano, e.commit.CreatedAt)
			age := formatAge(ts)
			msg := e.commit.Message
			if msg == "" {
				msg = dim("(no message)")
			}
			fmt.Printf("  %s  %-20s  %s\n", dim(age), cyan(e.skillName), msg)
		}

		return nil
	},
}

func formatAge(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 7*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	default:
		return t.Format("Jan 2")
	}
}
