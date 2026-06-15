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
	// Environment overrides users have set. Only printed when a flag is
	// active so unset-default doctor output stays uncluttered. The value
	// here is "is auto-update on?" — useful when something looks stuck
	// at an old version.
	renderEnvOverrides(os.Stdout)

	// Sync state: classify every known skill and surface any that are in a
	// non-trivial state (local edits, upstream moved, untracked, adoptable,
	// conflict, available). Informational only — exit code stays gated on
	// broken refs.
	if states, err := gatherSyncState(); err == nil {
		renderSyncStateReport(os.Stdout, states)
		renderPendingConflictReport(os.Stdout, pendingConflictNames())
		renderOverlayInvariantReport(os.Stdout, loadSyncState())
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

// renderOverlayInvariantReport validates the overlay-model invariants on
// the local markers and the caller's server-side rows:
//   - a Backup ref implies a Source (the overlay has an upstream)
//   - the Backup ref points at a row the caller still owns (flagged
//     backup=true server-side)
//   - every server-side backup row is referenced by some marker (an
//     orphaned one means a device deleted its marker without retiring
//     the fork — sync rebuilds it, so just surface it)
//
// Best-effort: skipped silently when not logged in.
func renderOverlayInvariantReport(w io.Writer, syncState *SyncState) {
	client, err := newAPIClientAuto()
	if err != nil {
		return
	}
	// Ownership query: backup rows are the caller's own personal skills,
	// shadowed out of the effective listing by the org skill they back up.
	owned, err := client.listSkills("personal")
	if err != nil {
		return
	}
	ownedBackupByID := map[string]bool{}
	for i := range owned {
		if isBackupRow(&owned[i]) {
			ownedBackupByID[owned[i].Id.String()] = false // false = unreferenced so far
		}
	}

	bang := red("!")
	for name, entry := range syncState.Skills {
		if entry == nil || entry.Backup == nil {
			continue
		}
		if entry.Source == nil {
			fmt.Fprintf(w, "  %s %s — marker has a backup ref but no upstream Source; run 'airskills sync' to heal it.\n", bang, name)
		}
		if _, ok := ownedBackupByID[entry.Backup.SkillID]; !ok {
			fmt.Fprintf(w, "  %s %s — marker's backup ref points at a row you no longer own; the next push will recreate the backup.\n", bang, name)
		} else {
			ownedBackupByID[entry.Backup.SkillID] = true
		}
	}
	for id, referenced := range ownedBackupByID {
		if !referenced {
			fmt.Fprintf(w, "  %s server-side backup copy %s is not referenced by any marker here — 'airskills sync' reconnects it (it may belong to another device).\n", bang, id)
		}
	}
}

func renderPendingConflictReport(w io.Writer, names []string) {
	if len(names) == 0 {
		return
	}
	fmt.Fprintf(w, "  %s pending conflict files in %s — merge them or discard the tmp copy.\n",
		red("!"), filepath.Join(os.TempDir(), "airskills-conflicts*"))
	for _, name := range names {
		fmt.Fprintf(w, "    %s\n", name)
	}
}

// renderEnvOverrides prints any active CLI environment overrides. No
// output when nothing is set — keeps the common-case doctor run clean.
func renderEnvOverrides(w io.Writer) {
	var lines []string
	if os.Getenv("AIRSKILLS_NO_AUTO_UPDATE") == "1" {
		lines = append(lines, "  AIRSKILLS_NO_AUTO_UPDATE=1 — per-command auto-update is OFF")
	}
	if os.Getenv("AIRSKILLS_NO_TELEMETRY") == "1" {
		lines = append(lines, "  AIRSKILLS_NO_TELEMETRY=1 — anonymous usage telemetry is OFF")
	}
	if len(lines) == 0 {
		return
	}
	fmt.Fprintln(w, "Environment overrides:")
	for _, line := range lines {
		fmt.Fprintln(w, line)
	}
	fmt.Fprintln(w)
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
		if r, _, fetchErr := client.listPersonalSkillsInSkillset(rememberedSkillsetSlug()); fetchErr == nil {
			remote = r
		}
	}

	hashLocal := func(path string) string {
		return computeMerkleHash(readSkillFiles(path))
	}
	return classifySkills(remote, localSkills, syncState, hashLocal), nil
}

// renderSyncStateReport writes the doctor "Sync state" section. Every
// notable state shares a uniform `!` prefix; the line itself names the
// cause and the action. Synced skills collapse into a single-line
// summary so the output stays scannable.
//
// Wording is the contract — the cli-reference.mdx sample and
// customisation.mdx pages are kept in sync with this.
func renderSyncStateReport(w io.Writer, states []SkillStateInfo) {
	fmt.Fprintln(w, "Sync state:")
	if len(states) == 0 {
		fmt.Fprintf(w, "  %s no skills tracked.\n", dim("·"))
		return
	}

	sorted := make([]SkillStateInfo, len(states))
	copy(sorted, states)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })

	bang := red("!")
	syncedCount := 0
	subscribedCount := 0
	for _, s := range sorted {
		// Tracked skills: the divergence booleans drive the line, not a flat
		// state value. A clean, non-moved tracked skill collapses into the
		// synced summary; everything else names its cause and action.
		if s.State == StateTracked {
			switch {
			case s.UpstreamMoved:
				// A fork whose upstream moved past what the user acknowledged.
				// Shown whether or not there are also local edits — the cell
				// the old encoding dropped to "synced" (spec bug #1).
				source := s.Marker.Source
				fromVer := s.Marker.Version
				toVer := ""
				if s.Remote != nil {
					toVer = s.Remote.Version
				}
				versionTransition := versionMoved(fromVer, toVer)
				// Taking the upstream's bytes goes through add --force for
				// every sourced shape: overlays aren't pull conflicts, and a
				// visible fork is retired by add --force (runPullForce
				// rejects both — "not in conflict").
				dropCmd := fmt.Sprintf("'airskills add %s/%s --force'", source.Owner, source.Slug)
				fmt.Fprintf(w, "  %s %s — customised copy of %s/%s. Original%s since you resolved. Review and 'airskills resolve %s', or %s to drop your customised copy.\n",
					bang, s.Name, source.Owner, source.Slug, versionTransition, s.Name, dropCmd)
			case s.Overlay && s.OverlayDiverged && !s.LocalDirty:
				// Overlay steady state: standing edits to a non-owned skill,
				// already backed up server-side. Informational, not work.
				suffix := ""
				if s.Marker.SuggestionID != "" {
					suffix = "; suggestion pending"
				} else if s.Marker.SuggestDeclined {
					suffix = "; suggestion declined"
				}
				fmt.Fprintf(w, "  %s %s — your edited copy of %s/%s (edits backed up%s).\n",
					bang, s.Name, s.Marker.Source.Owner, s.Marker.Source.Slug, suffix)
			case s.LocalDirty && s.Sourced:
				// Sourced + locally modified — unpublished customisations to a
				// sourced skill.
				fmt.Fprintf(w, "  %s %s — customised copy of %s/%s. Local has unpublished changes; run 'airskills push' to publish.\n",
					bang, s.Name, s.Marker.Source.Owner, s.Marker.Source.Slug)
			case s.LocalDirty:
				fmt.Fprintf(w, "  %s %s — local has unpublished changes. Run 'airskills push' to publish.\n",
					bang, s.Name)
			case isPersonalSubscriptionMarker(s.Marker):
				// A clean subscription — healthy, but counted apart from your
				// own skills so it never reads as owned.
				subscribedCount++
			default:
				syncedCount++
			}
			continue
		}
		switch s.State {
		case StateUntracked:
			fmt.Fprintf(w, "  %s %s — local exists, not tracked. No matching skill on the server.\n",
				bang, s.Name)
		case StateAdoptable:
			fmt.Fprintf(w, "  %s %s — local exists, not tracked. Original%s matches bytes — next sync will link.\n",
				bang, s.Name, versionLabel(s.Remote))
		case StateConflict:
			fmt.Fprintf(w, "  %s %s — local exists, not tracked. Original%s differs — next sync will surface conflict.\n",
				bang, s.Name, versionLabel(s.Remote))
		case StateAvailable:
			fmt.Fprintf(w, "  %s %s — on server, not installed here ('airskills sync' or 'airskills add').\n",
				bang, s.Name)
		}
	}
	if subscribedCount > 0 {
		fmt.Fprintf(w, "  %s %d added from others (subscriptions — they follow you across machines).\n", green("✓"), subscribedCount)
	}
	if syncedCount > 0 {
		fmt.Fprintf(w, "  %s %d synced.\n", green("✓"), syncedCount)
	}
}

// versionLabel returns " v<x.y.z>" when the remote carries a version,
// or "" otherwise. The leading space is intentional — call sites
// concatenate this directly into a sentence ("Original v1.0.8 matches…").
func versionLabel(r *apiSkill) string {
	if r == nil || r.Version == "" {
		return ""
	}
	return " v" + r.Version
}

// versionMoved formats the from→to transition for the modified-pending
// state. Falls back to a bare "has moved" when versions aren't both
// known so the line still reads naturally.
func versionMoved(from, to string) string {
	if from != "" && to != "" && from != to {
		return fmt.Sprintf(" moved %s → %s", from, to)
	}
	return " has moved"
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

// refSlugDenylist filters out tokens that match the regex but are never
// skill references: filesystem mount points, URL path placeholders, and
// built-in Claude Code slash commands. Without this filter, doctor floods
// with false positives like /tmp/foo (filesystem) or /clear (CC built-in).
var refSlugDenylist = map[string]bool{
	// Filesystem mount points and common path placeholders.
	"tmp": true, "dev": true, "var": true, "etc": true, "usr": true,
	"home": true, "opt": true, "sys": true, "proc": true, "run": true,
	"mnt": true, "media": true, "bin": true, "sbin": true, "lib": true,
	"path": true,
	// Built-in Claude Code slash commands. A user-defined skill with one of
	// these names would collide with a built-in anyway; not worth chasing.
	"add-dir": true, "agents": true, "bug": true, "clear": true,
	"compact": true, "config": true, "cost": true, "doctor": true,
	"exit": true, "export": true, "feedback": true, "help": true,
	"hooks": true, "ide": true, "init": true, "login": true,
	"logout": true, "mcp": true, "memory": true, "migrate-installer": true,
	"model": true, "output-style": true, "permissions": true,
	"pr-comments": true, "release-notes": true, "rename": true,
	"resume": true, "review": true, "status": true, "terminal-setup": true,
	"theme": true, "upgrade": true, "vim": true,
}

// extractRefSlugs extracts /slug references from SKILL.md text. Mirrors the
// platform's extractDependencySlugs logic.
//
// Skips contexts that are not skill references:
//   - YAML frontmatter (--- ... ---)
//   - Fenced code blocks (```...```)
//   - Inline code spans (`...`)
//   - Markdown link URLs ([text](url))
//   - Filesystem and URL paths (slug followed by /, e.g. /tmp/foo, /v3/api)
//   - refSlugDenylist (filesystem mounts and Claude Code built-ins like /clear)
func extractRefSlugs(text string) []string {
	body := text
	if strings.HasPrefix(text, "---\n") {
		if idx := strings.Index(text[4:], "\n---"); idx >= 0 {
			body = text[4+idx+4:]
		}
	}
	body = fencedCodeRe.ReplaceAllString(body, "")
	body = inlineCodeRe.ReplaceAllString(body, "")
	body = mdLinkURLRe.ReplaceAllString(body, "")

	seen := map[string]bool{}
	var slugs []string
	for _, match := range refSlugRe.FindAllStringSubmatch(body, -1) {
		if len(match) < 3 || match[2] == "/" {
			// slug followed by `/` is a path segment (e.g. /tmp/foo, /v3/api), not a skill ref
			continue
		}
		slug := match[1]
		if refSlugDenylist[slug] {
			continue
		}
		if !seen[slug] {
			seen[slug] = true
			slugs = append(slugs, slug)
		}
	}
	return slugs
}

var (
	fencedCodeRe = regexp.MustCompile("(?s)```[^\n]*\n.*?```")
	inlineCodeRe = regexp.MustCompile("`[^`\n]+`")
	mdLinkURLRe  = regexp.MustCompile(`\]\([^)]*\)`)
	refSlugRe    = regexp.MustCompile(`(?:^|[\s("'])/([a-z0-9][a-z0-9-]*)(/?)`)
)

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
