package cmd

import (
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/chrismdp/airskills/internal/apitypes"
	"github.com/spf13/cobra"
)

func init() {
	historyCmd.Flags().IntP("limit", "n", 20, "Number of recent changes to show")
	rootCmd.AddCommand(historyCmd)
}

var historyCmd = &cobra.Command{
	Use:   "log [skill-name]",
	Short: "Show version history for a skill, or a unified timeline across all skills",
	Long: `With a skill name: shows the version history for that skill, newest first,
with commit IDs so you can diff or export specific versions.

Without a skill name: fetches version history for all your skills and
shows a unified timeline of recent changes across your skillset.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		limit, _ := cmd.Flags().GetInt("limit")

		client, err := newAPIClientAuto()
		if err != nil {
			return err
		}

		if len(args) > 0 {
			return runSkillLog(client, args[0], limit)
		}
		return runUnifiedLog(client, limit)
	},
}

// runSkillLog shows version history for a single skill.
func runSkillLog(client *apiClient, skillName string, limit int) error {
	syncState := loadSyncState()
	entry, ok := syncState.Skills[skillName]
	if !ok || entry == nil || entry.SkillID == "" {
		return fmt.Errorf("skill %q not found in sync state — is it tracked by airskills? Run 'airskills list'", skillName)
	}

	commits, err := client.getVersionHistory(entry.SkillID)
	if err != nil {
		return fmt.Errorf("fetching version history: %w", err)
	}

	if len(commits) == 0 {
		fmt.Println("No history yet.")
		return nil
	}

	if len(commits) > limit {
		commits = commits[:limit]
	}

	fmt.Printf("%s (%d versions)\n\n", cyan(skillName), len(commits))
	for _, c := range commits {
		shortID := c.Id.String()[:8]
		age := formatAge(c.CreatedAt)
		msg := strDeref(c.Message)
		if msg == "" {
			msg = dim("(no message)")
		}
		fmt.Printf("  %s  %s  %s\n", yellow(shortID), dim(age), msg)
	}

	fmt.Printf("\n%s\n", dim("Diff a version:"))
	fmt.Printf("  %s\n", cyan("airskills diff "+skillName+" --commit <id>"))
	fmt.Printf("\n%s\n", dim("Export a version:"))
	fmt.Printf("  %s\n", cyan("airskills export "+skillName+" -f dir --commit <id> -o /tmp/"))
	return nil
}

// runUnifiedLog shows a merged timeline across all skills.
func runUnifiedLog(client *apiClient, limit int) error {
	skills, _, err := client.listPersonalSkillsInSkillset(rememberedSkillsetSlug())
	if err != nil {
		return fmt.Errorf("fetching skills: %w", err)
	}

	if len(skills) == 0 {
		fmt.Println("No skills found.")
		return nil
	}

	type entry struct {
		skillName string
		commit    apitypes.SkillCommit
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
			commits, err := client.getVersionHistory(s.Id.String())
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

	sort.Slice(all, func(i, j int) bool {
		return all[i].commit.CreatedAt.After(all[j].commit.CreatedAt)
	})

	if len(all) > limit {
		all = all[:limit]
	}

	for _, e := range all {
		age := formatAge(e.commit.CreatedAt)
		msg := strDeref(e.commit.Message)
		if msg == "" {
			msg = dim("(no message)")
		}
		fmt.Printf("  %s  %s  %-20s  %s\n", yellow(e.commit.Id.String()[:8]), dim(age), cyan(e.skillName), msg)
	}
	if len(all) == limit {
		fmt.Printf("\n%s %s\n", dim("Tip:"), dim("run 'airskills log <name>' for a specific skill's version history"))
	}

	return nil
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
