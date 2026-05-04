package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/chrismdp/airskills/config"
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
	// Sync state: classify every known skill and surface any that are
	// in a non-trivial state (modified, modified-pending, untracked,
	// linked, untracked-conflict, not-local). Informational only —
	// exit code stays gated on broken refs.
	if states, err := gatherSyncState(); err == nil {
		renderSyncStateReport(os.Stdout, states)
		fmt.Println()
	}

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

// gatherSyncState assembles the inputs the classifier needs and returns
// the cross-state of every skill on the machine. Best-effort: returns
// an error only when local scanning fails. Server fetch failures
// (offline, not logged in) yield local-only classification.
func gatherSyncState() ([]SkillStateInfo, error) {
	localSkills, err := scanSkillsFromAgents()
	if err != nil {
		return nil, err
	}
	syncState := loadSyncState()

	var remote []apiSkill
	if client, clientErr := newAPIClientAuto(); clientErr == nil {
		if cfg, cfgErr := config.Load(); cfgErr == nil {
			if r, _, fetchErr := client.listPersonalSkillsInSkillset(""); fetchErr == nil {
				remote = r
			}
			_ = cfg
		}
	}

	hashLocal := func(path string) string {
		return computeMerkleHash(readSkillFiles(path))
	}
	return classifySkills(remote, localSkills, syncState, hashLocal), nil
}

// renderSyncStateReport writes the doctor "Sync state" section. Notable
// states get one line each; synced skills get a single-line summary so
// the output stays scannable.
func renderSyncStateReport(w io.Writer, states []SkillStateInfo) {
	fmt.Fprintln(w, "Sync state:")
	if len(states) == 0 {
		fmt.Fprintf(w, "  %s no skills tracked.\n", dim("·"))
		return
	}

	sorted := make([]SkillStateInfo, len(states))
	copy(sorted, states)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })

	syncedCount := 0
	for _, s := range sorted {
		if s.State == StateSynced {
			syncedCount++
			continue
		}
		switch s.State {
		case StateModified:
			fmt.Fprintf(w, "  %s %s — modified locally (sync to publish)\n", yellow("M"), s.Name)
		case StateModifiedPending:
			fmt.Fprintf(w, "  %s %s — modified, original has moved → 'airskills resolve %s' or 'airskills pull --force %s'\n",
				yellow("M*"), s.Name, s.Name, s.Name)
		case StateUntracked:
			fmt.Fprintf(w, "  %s %s — untracked locally (no marker; arrived outside airskills?)\n", yellow("?"), s.Name)
		case StateLinked:
			fmt.Fprintf(w, "  %s %s — bytes match server; next sync will link silently\n", green("·"), s.Name)
		case StateUntrackedConflict:
			fmt.Fprintf(w, "  %s %s — server has a same-named skill with different bytes; next sync will surface a conflict\n",
				red("!"), s.Name)
		case StateNotLocal:
			fmt.Fprintf(w, "  %s %s — on server, not installed here ('airskills sync' or 'airskills add')\n", dim("—"), s.Name)
		}
	}
	if syncedCount > 0 {
		fmt.Fprintf(w, "  %s %d synced.\n", green("✓"), syncedCount)
	}
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
