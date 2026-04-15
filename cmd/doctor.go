package cmd

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(doctorCmd)
}

var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Check for broken skill references",
	Long: `Scans all locally installed skills for broken /skill-name references.
For each broken ref, reports whether it was moved (and where), deleted, or unknown.

Exit code is 0 if no issues are found, 1 if any are found.`,
	RunE: runDoctor,
}

// refIssue describes one broken reference found in a skill.
type refIssue struct {
	skillDir string // local dir name (= skill slug)
	refSlug  string // the broken /slug reference
	status   string // "moved", "deleted", "unknown", "offline"
	newSlug  string // set when status == "moved"
}

func runDoctor(cmd *cobra.Command, args []string) error {
	issues, err := walkBrokenRefs()
	if err != nil {
		return err
	}
	printRefReport(issues)
	if len(issues) > 0 {
		os.Exit(1)
	}
	return nil
}

// walkBrokenRefs scans all locally installed skills for broken /ref references
// and classifies each via the server. Returns nil slice if everything is clean.
func walkBrokenRefs() ([]refIssue, error) {
	localSkills, err := scanSkillsFromAgents()
	if err != nil {
		return nil, err
	}

	knownNames := map[string]bool{}
	for name := range localSkills {
		knownNames[name] = true
	}

	type skillRefs struct {
		dir  string
		refs []string
	}
	var withBroken []skillRefs

	for name, path := range localSkills {
		content, err := os.ReadFile(filepath.Join(path, "SKILL.md"))
		if err != nil {
			continue
		}
		deps := extractRefSlugs(string(content))
		var broken []string
		for _, dep := range deps {
			if !knownNames[dep] {
				broken = append(broken, dep)
			}
		}
		if len(broken) > 0 {
			withBroken = append(withBroken, skillRefs{name, broken})
		}
	}

	if len(withBroken) == 0 {
		return nil, nil
	}

	client, clientErr := newAPIClientAuto()
	syncState := loadSyncState()

	var issues []refIssue
	for _, s := range withBroken {
		marker := syncState.Skills[s.dir]
		if clientErr != nil || marker == nil || marker.SkillID == "" {
			for _, ref := range s.refs {
				issues = append(issues, refIssue{skillDir: s.dir, refSlug: ref, status: "unknown"})
			}
			continue
		}

		results, err := resolveRefs(client, marker.SkillID, s.refs)
		if err != nil {
			for _, ref := range s.refs {
				issues = append(issues, refIssue{skillDir: s.dir, refSlug: ref, status: "offline"})
			}
			continue
		}

		for _, r := range results {
			issues = append(issues, refIssue{
				skillDir: s.dir,
				refSlug:  r.Ref,
				status:   r.Status,
				newSlug:  r.NewSlug,
			})
		}
	}

	return issues, nil
}

// resolveRefsResult is the per-ref result from the server.
type resolveRefsResult struct {
	Ref     string `json:"ref"`
	Status  string `json:"status"`
	NewSlug string `json:"new_slug,omitempty"`
}

// resolveRefs calls /api/v1/refs/resolve to classify broken refs server-side.
func resolveRefs(client *apiClient, skillID string, refs []string) ([]resolveRefsResult, error) {
	query := url.Values{}
	query.Set("skill", skillID)
	query.Set("refs", strings.Join(refs, ","))
	body, err := client.get("/api/v1/refs/resolve?" + query.Encode())
	if err != nil {
		return nil, err
	}
	var resp struct {
		Results []resolveRefsResult `json:"results"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}
	return resp.Results, nil
}

// extractRefSlugs extracts /slug references from SKILL.md text, stripping frontmatter.
// Mirrors the platform's extractDependencySlugs logic.
func extractRefSlugs(text string) []string {
	body := text
	if strings.HasPrefix(text, "---\n") {
		if idx := strings.Index(text[4:], "\n---"); idx >= 0 {
			body = text[4+idx+4:]
		}
	}
	pattern := regexp.MustCompile(`(?:^|[\s("'])\/([a-z0-9][a-z0-9-]*)`)
	seen := map[string]bool{}
	var slugs []string
	for _, match := range pattern.FindAllStringSubmatch(body, -1) {
		if len(match) >= 2 && !seen[match[1]] {
			seen[match[1]] = true
			slugs = append(slugs, match[1])
		}
	}
	return slugs
}

func printRefReport(issues []refIssue) {
	fmt.Println("Refs:")
	if len(issues) == 0 {
		fmt.Printf("  %s no broken refs found.\n", green("✓"))
		return
	}

	// Group by skill dir
	type group struct {
		dir    string
		issues []refIssue
	}
	seen := map[string]bool{}
	var groups []group
	for _, issue := range issues {
		if !seen[issue.skillDir] {
			seen[issue.skillDir] = true
			groups = append(groups, group{dir: issue.skillDir})
		}
		for i := range groups {
			if groups[i].dir == issue.skillDir {
				groups[i].issues = append(groups[i].issues, issue)
				break
			}
		}
	}

	for _, g := range groups {
		for _, issue := range g.issues {
			fmt.Printf("  %s %s/SKILL.md references /%s\n", red("✗"), issue.skillDir, issue.refSlug)
			switch issue.status {
			case "moved":
				fmt.Printf("    → moved to /%s\n", issue.newSlug)
				fmt.Printf("    → patch %s/SKILL.md to use /%s\n", issue.skillDir, issue.newSlug)
			case "deleted":
				fmt.Printf("    → was deleted\n")
				fmt.Printf("    → remove or replace the reference\n")
			case "offline":
				fmt.Printf("    → could not check server (offline or not logged in)\n")
			default:
				fmt.Printf("    → does not exist (no redirect found)\n")
				fmt.Printf("    → remove or replace the reference\n")
			}
		}
	}

	fmt.Printf("\n%d issue(s) found.\n", len(issues))
}
