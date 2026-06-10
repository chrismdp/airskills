package cmd

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/chrismdp/airskills/telemetry"
	"github.com/spf13/cobra"
)

type skillSource struct {
	Owner               string `json:"owner"`                           // username of the original author
	Slug                string `json:"slug"`                            // original skill slug
	ID                  string `json:"id"`                              // original skill ID from the server
	ContentHash         string `json:"content_hash,omitempty"`          // legacy upstream hash at add/sync time
	UpstreamSkillID     string `json:"upstream_skill_id,omitempty"`     // upstream skill ID, explicit alias for ID
	UpstreamContentHash string `json:"upstream_content_hash,omitempty"` // upstream hash last incorporated
	UpstreamVersion     string `json:"upstream_version,omitempty"`      // display-only upstream version last seen
	SkillsetSlug        string `json:"skillset_slug,omitempty"`         // org skillset slug (non-empty for org-distributed skills)
	GitHubURL           string `json:"github_url,omitempty"`            // GitHub repo URL (for skills imported from GitHub)
	GitHubSkill         string `json:"github_skill,omitempty"`          // skill subdirectory within the GitHub repo (for multi-skill repos)
}

type conflictInfo struct {
	name       string
	localPath  string
	remotePath string
}

type validationInfo struct {
	name string
	err  error
}

// pendingSuggestionPrompt is collected inside the concurrent push goroutines
// and drained sequentially after wg.Wait so we can prompt the user without
// racing multiple goroutines on stdin.
type pendingSuggestionPrompt struct {
	name             string
	suggesterSkillID string
	source           *skillSource
}

// pendingShadowFork captures the inputs the drain pass needs to fork an
// edit-on-upstream skill into the caller's namespace, upload the local
// content to the fork, and submit a suggestion against the upstream.
// See cli-org-member-suggest-via-shadow-fork.md (Option B). The prompt
// + sequential API calls live in the drain pass so stdin and the user
// summary aren't racing across goroutines.
type pendingShadowFork struct {
	name        string
	dir         string
	archive     []byte
	contentHash string
	archiveSize int64
	source      *skillSource
	prevMarker  *SyncEntry
	progressIdx int
}

var pushForce bool
var pushOrg string
var pushForceSuggest bool
var pushNoIgnore bool

var pushCmd = &cobra.Command{
	Use:   "push [skill...]",
	Short: "Push local skill changes to airskills.ai",
	Args:  cobra.ArbitraryArgs,
	Long: `Scans local skills, detects changes, and pushes updates (including all files) to the server.

With no positional arguments, push operates on every dirty skill in the
caller's effective set. Pass one or more skill names to scope push to just
those skills — useful after resolving a conflict on one skill without
touching the others.

--force tells the server "my local wins, take it as-is" by skipping the
content-hash conflict check. Use it after you've reviewed a conflict and
decided your local copy is the truth. The mirror flag is 'pull --force',
which means "remote wins, overwrite my local". Both flags express the same
intent — "I'm about to overwrite the other side, do it anyway" — pointed
in opposite directions.

Mirror and orphan-classifier passes always run across every detected skill,
even when push is scoped to named skills — mirroring keeps multi-agent dirs
consistent and surfacing moved/deleted markers is important even when the
caller asked about something else.

A skill folder may contain a .askignore (or .gitignore) at any depth using
standard gitignore syntax to keep local-only files (cron wrappers, sync
state, secrets) out of the upload. Both files are honoured and merge — use
.askignore for "git-tracked but not airskills-pushed", or vice versa. Run
'airskills diff <skill>' to see exactly what's being excluded, or push -v
for a per-file rundown. SKILL.md is always uploaded — if a rule matches it,
push fails so the rule can be fixed. --no-ignore bypasses both files for
one push (handy for moving a skill between machines without losing local
config).`,
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := newAPIClientAuto()
		if err != nil {
			return err
		}

		// Resolve org ID once before the goroutines so we don't hammer the API
		// with concurrent identical lookups.
		var createOrgID string
		if pushOrg != "" {
			createOrgID, err = lookupCallerOrgID(client, pushOrg)
			if err != nil {
				return fmt.Errorf("org %q: %w", pushOrg, err)
			}
		}

		// Validate positional args up front — before any expensive work or
		// the force confirmation prompt. Each named skill must correspond
		// to a local directory; push uploads local content, so a name
		// that isn't on disk can't be pushed. A bad name should fail fast,
		// not after the user has accepted a force-overwrite prompt.
		if len(args) > 0 {
			preScan, scanErr := scanSkillsFromAgents()
			if scanErr != nil || len(preScan) == 0 {
				return fmt.Errorf("unknown skill %q (no local skill directories found)", args[0])
			}
			for _, name := range args {
				if _, ok := preScan[name]; !ok {
					return fmt.Errorf("unknown skill %q (no local directory found)", name)
				}
			}
		}

		// Confirm force push. When scoped to named skills, the prompt names
		// them — the user is force-pushing exactly the skills they typed,
		// not the wider dirty set.
		if pushForce {
			prompt := "Force push will overwrite remote versions. Continue? [y/N] "
			if len(args) > 0 {
				quoted := make([]string, len(args))
				for i, a := range args {
					quoted[i] = fmt.Sprintf("%q", a)
				}
				noun := "the remote version"
				if len(args) > 1 {
					noun = "the remote versions"
				}
				prompt = fmt.Sprintf("Force push %s will overwrite %s. Continue? [y/N] ",
					strings.Join(quoted, ", "), noun)
			}
			fmt.Print(prompt)
			reader := bufio.NewReader(os.Stdin)
			answer, _ := reader.ReadString('\n')
			if strings.TrimSpace(strings.ToLower(answer)) != "y" {
				fmt.Println("Aborted.")
				return nil
			}
		}

		var conflictMessages []conflictInfo
		var validationMessages []validationInfo

		syncState := loadSyncState()

		// Propagate any local edit across every detected agent dir before
		// we scan, so push sees a consistent view. Mirror runs only when
		// push is invoked standalone; in a `sync` run pull already
		// executed mirror (plus the partial-rename pass) and registered
		// the conflict set in syncActiveConflicts. Reading that set lets
		// push skip both the redundant mirror call and any upload that
		// would collide with a pull-detected divergence.
		mirrorConflictSet := map[string]bool{}
		if syncActiveConflicts != nil {
			for slug := range syncActiveConflicts {
				mirrorConflictSet[slug] = true
			}
		} else {
			// Standalone push: run the partial-rename pass and mirror as
			// before. A manual `mv` in one agent dir leaves the same
			// skill living under two names; if mirror runs first it
			// cross-pollinates both names across every agent and the
			// rename signal is lost.
			if scanned, scanErr := scanSkillsFromAgents(); scanErr == nil {
				propagatePartialRenames(scanned, syncState)
			}
			// Resolve hand-deleted files before the mirror resurrects them
			// from a sibling agent dir (see resolveIntraSkillDeletions).
			// --force removes permanently; a terminal prompts; headless keeps
			// + hints. In a `sync` run this branch is skipped (syncActiveConflicts
			// is non-nil), so the pull-phase call above handles it once.
			deletionDec := deletionKeep
			if pushForce {
				deletionDec = deletionRemove
			} else if isTTY {
				deletionDec = deletionAsk
			}
			// Scope deletion handling to the named skills on a scoped push,
			// so `push <skill> --force` can never remove files from a skill
			// the user didn't name. args is nil for a full push (all skills).
			resolveIntraSkillDeletions(client, syncState, deletionDec, args)
			_, mirrorConflicts, restoreHints := mirrorLocalSkills(syncState)
			printMirrorConflicts(mirrorConflicts)
			printMirrorRestoreHints(restoreHints)
			for _, c := range mirrorConflicts {
				mirrorConflictSet[c.slug] = true
			}
		}

		// Scan all detected agent directories for skills
		localSkills, err := scanSkillsFromAgents()
		if err != nil || len(localSkills) == 0 {
			fmt.Println("No skills found in any agent directory.")
			return nil
		}

		// Collect skills to push
		type skillEntry struct {
			name   string
			dir    string
			marker *SyncEntry
		}

		var skills []skillEntry
		for name, dir := range localSkills {
			if mirrorConflictSet[name] {
				continue
			}
			se := skillEntry{name: name, dir: dir}
			if m, ok := syncState.Skills[name]; ok {
				se.marker = m
			}
			skills = append(skills, se)
		}

		// Detect renames: entries in sync state whose directory no longer exists
		localDirSet := map[string]bool{}
		for _, s := range skills {
			localDirSet[s.name] = true
		}
		orphanHashToName := map[string]string{} // content_hash → old dir name
		for name, entry := range syncState.Skills {
			if !localDirSet[name] && entry.ContentHash != "" {
				orphanHashToName[entry.ContentHash] = name
			}
		}

		if len(skills) == 0 {
			fmt.Println("No skills to push.")
			return nil
		}

		// Fetch the caller's EFFECTIVE skillset — owned skills PLUS any
		// org skills they can reach via membership (default + assigned
		// skillsets). scope=personal would exclude the org slice and turn
		// every org-membership marker into a spurious "moved (re-link
		// needed)" warning, because its skill_id would look unowned. See
		// cli-push-owned-listing-excludes-org-membership-skills.md (and
		// the matching pull-side fix in skills-list-scope-personal-…).
		//
		// Empty skillset slug => server resolves to the caller's default.
		// 404 (no default skillset) is treated as an empty listing so push
		// still works for brand-new accounts before any skillset exists.
		remoteSkills, _, listErr := client.listPersonalSkillsInSkillset("")
		if listErr != nil {
			if _, isNotFound := listErr.(*SkillsetNotFoundError); !isNotFound {
				return fmt.Errorf("fetching skills: %w", listErr)
			}
		}
		remoteByName := map[string]*apiSkill{}
		remoteByID := map[string]*apiSkill{}
		effectiveSkillIDs := map[string]bool{}
		for i := range remoteSkills {
			remoteByName[remoteSkills[i].Name] = &remoteSkills[i]
			remoteByID[remoteSkills[i].Id.String()] = &remoteSkills[i]
			effectiveSkillIDs[remoteSkills[i].Id.String()] = true
		}

		// Filter out skills whose sync state SkillID isn't in the caller's
		// owned set. Two cases:
		//  - has Source (was added from another user) → clear SkillID so it
		//    gets created as a fork.
		//  - no Source → the skill_id existed at some point and we tracked it,
		//    but the server doesn't return it anymore. Either it was deleted
		//    (orphan), transferred to a different owner (moved), or the server
		//    is having a bad day (transient). Classify and act on each.
		var filtered []skillEntry
		type skippedCandidate struct {
			name     string
			localDir string
			marker   *SyncEntry
		}
		var skippedCandidates []skippedCandidate
		for _, s := range skills {
			if s.marker != nil && s.marker.Deleted {
				// Tombstoned by a prior push (orphan or moved). The dir is
				// kept locally on purpose; do not re-classify it and do not
				// auto-publish it as a new personal skill.
				continue
			}
			if s.marker != nil && s.marker.SkillID != "" && !effectiveSkillIDs[s.marker.SkillID] {
				if s.marker.Source != nil {
					s.marker.SkillID = ""
				} else {
					skippedCandidates = append(skippedCandidates, skippedCandidate{
						name: s.name, localDir: s.dir, marker: s.marker,
					})
					continue
				}
			}
			filtered = append(filtered, s)
		}
		skills = filtered

		// Resolve each skipped marker against the server and apply the
		// appropriate action. See cmd/skipped_marker.go for the decision
		// tree; mutations happen here so we keep all sync-state writes in
		// one place. Buckets surface in the summary further down.
		var orphanRemoved, orphanKept, movedKept, transient []skippedAction
		var earlyWarnings []string
		for _, c := range skippedCandidates {
			// Orphan/moved detection lives in push only, deliberately — see
			// platform/doc/changes/cli-moved-transfer-marker-validation.md.
			action := classifySkippedMarker(client, c.name, c.marker, c.localDir)
			switch action.kind {
			case actionOrphanRemove:
				if _, err := removeLocalSkill(c.name); err != nil {
					earlyWarnings = append(earlyWarnings, fmt.Sprintf("%s: deleted server-side but local removal failed: %v", c.name, err))
				}
				delete(syncState.Skills, c.name)
				orphanRemoved = append(orphanRemoved, action)
			case actionOrphanKeep:
				// Tombstone the marker rather than dropping it: a bare dir
				// with no marker would otherwise be auto-published as a
				// new personal skill on the next push.
				syncState.Skills[c.name] = &SyncEntry{Deleted: true}
				orphanKept = append(orphanKept, action)
			case actionMovedKeep:
				syncState.Skills[c.name] = &SyncEntry{
					Deleted: true,
					MovedTo: action.newOwnerSlug + "/" + action.newSkillSlug,
				}
				movedKept = append(movedKept, action)
			case actionTransient:
				// Leave marker + dir intact; retry next sync.
				transient = append(transient, action)
			}
		}

		// Scope filter: when positional args were given, narrow the upload
		// pass to just those skills. Mirror, rename detection, and the
		// moved/orphan classifier above all ran across every detected skill
		// so their warnings still surface — only the upload pass narrows.
		if len(args) > 0 {
			requested := make(map[string]bool, len(args))
			for _, a := range args {
				requested[a] = true
			}
			var scoped []skillEntry
			for _, s := range skills {
				if requested[s.name] {
					scoped = append(scoped, s)
				}
			}
			skills = scoped
		}

		// Pre-filter unchanged so the progress counter reflects what we're
		// actually about to push. Without this, "77/77 skills" gets printed
		// for a run where nothing changed — the unchanged check inside each
		// goroutine still fires correctly, but the user sees the full local
		// count. Only filter tracked, non-deleted skills with a known hash;
		// new skills (no marker) and shadow-fork / deleted markers must go
		// through the goroutine so their full logic still runs.
		var unchangedCount int
		var pending []skillEntry
		for _, s := range skills {
			if s.marker != nil && !s.marker.Deleted && s.marker.ContentHash != "" {
				if computeMerkleHash(readSkillFiles(s.dir)) == s.marker.ContentHash {
					unchangedCount++
					continue
				}
			}
			pending = append(pending, s)
		}
		skills = pending

		// Print initial progress lines
		lines := make([]progressLine, len(skills))
		for i, s := range skills {
			lines[i] = progressLine{name: s.name, status: "waiting", pct: 0}
		}
		if verbose && isTTY {
			for _, l := range lines {
				fmt.Printf("  %-20s  %s  %s\n", l.name, renderBar(0), "waiting")
			}
		} else if isTTY && len(skills) > 0 {
			fmt.Printf("  %s %d skills\n", dim("·"), len(skills))
		}

		var pushed, created, linked, renamed, conflicts, failed int64
		var pushedNames, createdNames []string
		var mu sync.Mutex
		var wg sync.WaitGroup
		var warnings []string
		var pendingPrompts []pendingSuggestionPrompt
		var pendingShadowForks []pendingShadowFork
		// Lazy owner lookup for the 403 self-heal below. Safe to share
		// across goroutines: init is sync.Once-guarded and the maps are
		// read-only afterwards.
		owners := newOwnerResolver(client)
		sem := make(chan struct{}, 5) // max 5 concurrent uploads

		// Free tier limits (checked client-side as guidance, server enforces)
		const freeSkillLimit = 100
		const freeStorageLimit int64 = 100 * 1024 * 1024 // 100MB total
		if len(skills) > freeSkillLimit {
			warnings = append(warnings, fmt.Sprintf("%d skills exceeds %d free tier limit — will not be supported in future versions. See airskills.ai/pricing", len(skills), freeSkillLimit))
		}
		// Fail fast if any skill's ignore rules would exclude SKILL.md —
		// the skill is meaningless without it and we'd rather the author
		// fix the rule than silently ship a broken upload.
		for _, s := range skills {
			if err := newPushMatcher(s.dir).CheckSkillFile(); err != nil {
				return fmt.Errorf("%s: %w", s.name, err)
			}
		}

		// Warn (don't fail) when a file the SKILL.md frontmatter declares in
		// allowed-tools / script: would be excluded by the ignore rules — it
		// ships missing and the skill breaks at use time. Skipped under
		// --no-ignore, where the rules aren't being applied.
		if !pushNoIgnore {
			for _, s := range skills {
				for _, w := range checkIgnoredToolReferences(s.dir, newPushMatcher(s.dir)) {
					warnings = append(warnings, fmt.Sprintf("%s: %s", s.name, w))
				}
			}
		}

		// Verbose mode: print the files each skill will exclude, with the
		// matching rule, before any upload starts. Run serially so output
		// doesn't interleave with the parallel push progress bar below.
		if verbose {
			for _, s := range skills {
				if pushNoIgnore {
					continue // user opted out of ignore rules entirely
				}
				entries := listIgnoredFiles(s.dir)
				if len(entries) == 0 {
					continue
				}
				fmt.Printf("  %s: excluding %d file(s)\n", s.name, len(entries))
				for _, e := range entries {
					fmt.Printf("    %s  (%s)\n", e.Path, e.Reason)
				}
			}
		}

		// Calculate total local storage
		var totalStorage int64
		for _, s := range skills {
			localFiles := readSkillFiles(s.dir)
			for _, data := range localFiles {
				totalStorage += int64(len(data))
			}
		}
		if totalStorage > freeStorageLimit {
			warnings = append(warnings, fmt.Sprintf("%.1fMB total storage exceeds 100MB free tier limit — will not be supported in future versions. See airskills.ai/pricing",
				float64(totalStorage)/1024/1024))
		}

		for i, s := range skills {
			i, s := i, s
			wg.Add(1)
			sem <- struct{}{}
			go func() {
				defer wg.Done()
				defer func() { <-sem }()

				// Proactive refuse for markers flagged as deleted/transferred
				if s.marker != nil && s.marker.Deleted {
					msg := fmt.Sprintf("%s: skill was transferred to %s — local edits here are NOT being pushed. Merge them into %s/ and rm -rf %s/",
						s.name, s.marker.MovedTo, s.marker.MovedTo, s.name)
					if s.marker.MovedTo == "" {
						msg = fmt.Sprintf("%s: skill was deleted server-side — local edits here are NOT being pushed. Run 'airskills restore %s' to recover.", s.name, s.name)
					}
					lines[i].status = "skipped"
					lines[i].pct = 0
					renderProgress(lines)
					mu.Lock()
					warnings = append(warnings, msg)
					mu.Unlock()
					atomic.AddInt64(&failed, 1)
					return
				}

				localFiles := readSkillFiles(s.dir)

				// Compute hash from raw files before any name-fix so that rename
				// detection (orphan hash lookup) still works even when SKILL.md name
				// doesn't yet match the directory. The name-fix changes the hash,
				// so the orphan map must be probed with the pre-fix hash.
				rawContentHash := computeMerkleHash(localFiles)

				// Auto-fix SKILL.md name for new skills: cp-ing a skill dir leaves the
				// original name field intact. The server validates name == slug strictly,
				// so rewrite client-side and warn rather than fail the push.
				if s.marker == nil || s.marker.SkillID == "" {
					if skillMd, ok := localFiles["SKILL.md"]; ok {
						dirName := filepath.Base(s.dir)
						if fixed, changed := fixSkillNameInContent(dirName, skillMd); changed {
							_ = os.WriteFile(filepath.Join(s.dir, "SKILL.md"), fixed, 0644)
							localFiles["SKILL.md"] = fixed
							mu.Lock()
							warnings = append(warnings, fmt.Sprintf("%s: SKILL.md name field auto-updated to %q to match directory", s.name, dirName))
							mu.Unlock()
						}
					}
				}

				if err := validateSkillFiles(s.dir, localFiles); err != nil {
					lines[i].status = "invalid"
					lines[i].pct = 0
					renderProgress(lines)
					mu.Lock()
					validationMessages = append(validationMessages, validationInfo{name: s.name, err: err})
					mu.Unlock()
					atomic.AddInt64(&failed, 1)
					return
				}

				lines[i].status = "compressing"
				lines[i].pct = 0.2
				renderProgress(lines)

				archive, err := createTarGz(s.dir)
				if err != nil {
					lines[i].status = "failed"
					lines[i].pct = 0
					renderProgress(lines)
					atomic.AddInt64(&failed, 1)
					return
				}

				archiveSize := int64(len(archive))

				var sizeWarning string
				contentHash := computeMerkleHash(localFiles)

				// Skip unchanged skills (content hash matches what we last pushed)
				if s.marker != nil && s.marker.ContentHash != "" && s.marker.ContentHash == contentHash {
					if sizeWarning != "" {
						mu.Lock()
						warnings = append(warnings, sizeWarning)
						mu.Unlock()
					}
					lines[i].status = "unchanged"
					lines[i].pct = 1
					renderProgress(lines)
					return
				}

				if s.marker != nil && s.marker.Source != nil && !pushForceSuggest {
					if remote := remoteForUpstreamCheck(s.marker, remoteByID); remote != nil && upstreamAdvanced(s.marker, *remote) {
						src := s.marker.Source.Owner + "/" + s.marker.Source.Slug
						mu.Lock()
						warnings = append(warnings, fmt.Sprintf(
							"%s: upstream %s advanced — take it with 'airskills add %s --force' (drops your edits), or use --force-suggest to submit anyway.",
							s.name, src, src))
						mu.Unlock()
						lines[i].status = "incoming"
						lines[i].pct = 1
						renderProgress(lines)
						return
					}
				}

				// Shadow-fork detection: marker points at the upstream skill
				// directly (org-member sync, or a non-owned skill whose marker
				// pre-dates Source population in pull.go). A local edit means
				// we must fork into the caller's namespace and submit a
				// suggestion, NOT overwrite the upstream. See
				// platform/doc/changes/cli-org-member-suggest-via-shadow-fork.md.
				//
				// Discriminator: marker.SkillID == marker.Source.ID. A real
				// fork (created by `airskills add` while logged in, or by a
				// prior shadow-fork pass) has a distinct SkillID and continues
				// down the normal upload path with the existing suggest prompt.
				if s.marker != nil && s.marker.Source != nil &&
					s.marker.SkillID != "" && s.marker.SkillID == s.marker.Source.ID {
					if s.marker.SuggestionID != "" {
						// Edit-while-pending: leave the upstream alone. Amending
						// an existing suggestion is out of scope for this ticket;
						// surface a warning so the user knows nothing happened.
						mu.Lock()
						warnings = append(warnings, fmt.Sprintf(
							"%s: a suggestion to %s/%s is still pending — local edits NOT pushed. Wait for the owner to accept/decline, then re-edit.",
							s.name, s.marker.Source.Owner, s.marker.Source.Slug))
						mu.Unlock()
						lines[i].status = "pending suggestion"
						lines[i].pct = 1
						renderProgress(lines)
						return
					}
					// A past SuggestDeclined doesn't block this path: the
					// decline applied to those bytes. New edits are backed
					// up again and the suggest question is asked afresh.
					// Collect for sequential prompt + fork + upload + suggest
					// after wg.Wait so we don't race goroutines on stdin or
					// the (sequential) caller-side rename + suggestion APIs.
					mu.Lock()
					pendingShadowForks = append(pendingShadowForks, pendingShadowFork{
						name:        s.name,
						dir:         s.dir,
						archive:     archive,
						contentHash: contentHash,
						archiveSize: archiveSize,
						source:      s.marker.Source,
						prevMarker:  s.marker,
						progressIdx: i,
					})
					mu.Unlock()
					lines[i].status = "fork-suggest"
					lines[i].pct = 0.5
					renderProgress(lines)
					return
				}

				// Detect rename via orphan-hash BEFORE classifying isNew. If a
				// marker's stored hash matches this dir's pre-name-fix hash, it's
				// the same skill under a new name. PATCH the server, re-key the
				// marker, then proceed straight to upload — skipping the new-skill
				// branch below which would otherwise call createSkill and 409 on
				// slug_conflict (the server already has the renamed skill).
				if s.marker == nil || s.marker.SkillID == "" {
					mu.Lock()
					oldName, found := orphanHashToName[rawContentHash]
					if found {
						oldEntry := syncState.Skills[oldName]
						mu.Unlock()

						body, status, perr := client.put(
							fmt.Sprintf("/api/v1/skills/%s", oldEntry.SkillID),
							map[string]interface{}{"name": s.name},
						)
						if perr != nil || status >= 400 {
							lines[i].status = "failed"
							renderProgress(lines)
							mu.Lock()
							warnings = append(warnings, fmt.Sprintf(
								"%s: server rename failed (%s → %s, status %d): %s",
								s.name, oldName, s.name, status, strings.TrimSpace(string(body)),
							))
							mu.Unlock()
							atomic.AddInt64(&failed, 1)
							return
						}

						mu.Lock()
						s.marker = &SyncEntry{
							SkillID:     oldEntry.SkillID,
							Version:     oldEntry.Version,
							ContentHash: oldEntry.ContentHash,
							Tool:        oldEntry.Tool,
							Source:      oldEntry.Source,
						}
						delete(syncState.Skills, oldName)
						delete(orphanHashToName, rawContentHash)
						syncState.Skills[s.name] = s.marker
						mu.Unlock()
						fmt.Fprintf(os.Stderr, "\n  %s → %s (renamed)\n", oldName, s.name)
						atomic.AddInt64(&renamed, 1)
						// Fall through to upload with the now-tracked marker.
					} else {
						mu.Unlock()
					}
				}

				// Recompute after possible rename above.
				isNew := s.marker == nil || s.marker.SkillID == ""
				if isNew {
					// Check if skill already exists on server
					if remote, found := remoteByName[s.name]; found {
						s.marker = &SyncEntry{
							SkillID:     remote.Id.String(),
							Version:     remote.Version,
							ContentHash: strDeref(remote.ContentHash),
							Tool:        "claude-code",
						}
						isNew = false

						if strDeref(remote.ContentHash) == contentHash {
							mu.Lock()
							syncState.Skills[s.name] = s.marker
							mu.Unlock()
							lines[i].status = "linked"
							lines[i].pct = 1
							renderProgress(lines)
							atomic.AddInt64(&linked, 1)
							return
						}

						if !pushForce {
							lines[i].status = "CONFLICT"
							renderProgress(lines)

							tmpDir := filepath.Join(os.TempDir(), "airskills-conflicts", s.name)
							os.MkdirAll(tmpDir, 0755)
							rawBody, rawErr := client.get(fmt.Sprintf("/api/v1/skills/%s/raw", remote.Id))
							if rawErr == nil {
								tmpPath := filepath.Join(tmpDir, "SKILL.md")
								os.WriteFile(tmpPath, rawBody, 0644)
								mu.Lock()
								conflictMessages = append(conflictMessages, conflictInfo{
									name:       s.name,
									localPath:  filepath.Join(s.dir, "SKILL.md"),
									remotePath: tmpPath,
								})
								mu.Unlock()
							}
							atomic.AddInt64(&conflicts, 1)
							return
						}
					} else {
						lines[i].status = "creating"
						lines[i].pct = 0.4
						renderProgress(lines)

						var forkedFrom string
						if s.marker != nil && s.marker.Source != nil {
							forkedFrom = s.marker.Source.ID
						}

						skill, err := client.createSkill(s.name, "", []string{"claude-code"}, forkedFrom, createOrgID)
						if err != nil {
							// Migration-047 effective-set collision: surface the
							// conflicting source clearly instead of "failed".
							var sc *SkillConflictError
							if errors.As(err, &sc) {
								lines[i].status = "conflict"
								renderProgress(lines)
								mu.Lock()
								warnings = append(warnings, fmt.Sprintf("%s: %s", s.name, sc.Error()))
								mu.Unlock()
							} else {
								lines[i].status = "failed"
								renderProgress(lines)
							}
							atomic.AddInt64(&failed, 1)
							return
						}

						s.marker = &SyncEntry{SkillID: skill.Id.String(), Version: skill.Version, Tool: "claude-code"}
						if createOrgID != "" {
							s.marker.OwnerKind = "org"
							s.marker.OwnerSlug = pushOrg
						}
						mu.Lock()
						if old, ok := syncState.Skills[s.name]; ok && old.Source != nil {
							s.marker.Source = old.Source
						}
						mu.Unlock()
					}
				}

				lines[i].status = "uploading"
				lines[i].pct = 0.6
				renderProgress(lines)

				expectedHash := ""
				if !pushForce && s.marker.ContentHash != "" {
					expectedHash = s.marker.ContentHash
				}

				updated, statusCode, err := client.putArchive(
					s.marker.SkillID, archive, expectedHash, contentHash,
				)
				if err != nil {
					// 403 on a skill that's still alive in the caller's
					// effective set isn't a transfer — the caller just can't
					// write it (org member, or a marker that lost its Source
					// pointer, e.g. one written by an older `pull --force`).
					// Backfill the upstream pointer and take the same
					// fork+suggest path as any other non-owned edit instead
					// of dead-ending on a "moved" warning whose suggested
					// fix (`add --force`) would discard the user's edits.
					if statusCode == 403 {
						if remote, ok := remoteByID[s.marker.SkillID]; ok {
							if src := owners.sourceForNonOwned(remote); src != nil {
								mu.Lock()
								pendingShadowForks = append(pendingShadowForks, pendingShadowFork{
									name:        s.name,
									dir:         s.dir,
									archive:     archive,
									contentHash: contentHash,
									archiveSize: archiveSize,
									source:      src,
									prevMarker:  s.marker,
									progressIdx: i,
								})
								mu.Unlock()
								lines[i].status = "fork-suggest"
								lines[i].pct = 0.5
								renderProgress(lines)
								return
							}
						}
					}
					// 403/404: most likely server-side transfer or deletion.
					// GET the skill to confirm and produce a clear message.
					if statusCode == 403 || statusCode == 404 {
						state, gerr := classifyMarkerSkill(client, s.marker)
						if gerr == nil {
							switch state.kind {
							case markerStateMoved:
								mu.Lock()
								warnings = append(warnings, fmt.Sprintf(
									"%s: skill was moved to %s/%s and you no longer have write access. Run `airskills add %s/%s` to install the new copy.",
									s.name, state.ownerSlug, state.skillSlug,
									state.ownerSlug, state.skillSlug))
								mu.Unlock()
							case markerStateOrphan:
								mu.Lock()
								warnings = append(warnings, fmt.Sprintf(
									"%s: skill no longer exists on the server. Marker is orphaned — remove the local dir if you don't need it.",
									s.name))
								mu.Unlock()
							default:
								mu.Lock()
								warnings = append(warnings, fmt.Sprintf("%s: %v", s.name, err))
								mu.Unlock()
							}
						} else {
							mu.Lock()
							warnings = append(warnings, fmt.Sprintf("%s: %v", s.name, err))
							mu.Unlock()
						}
						lines[i].status = "skipped"
						renderProgress(lines)
						atomic.AddInt64(&failed, 1)
						return
					}
					if statusCode == 410 {
						var resp struct {
							Message string `json:"message"`
						}
						json.Unmarshal([]byte(err.Error()), &resp)
						msg := resp.Message
						if msg == "" {
							msg = "skill was deleted server-side"
						}
						mu.Lock()
						warnings = append(warnings, fmt.Sprintf("%s: %s Run `airskills restore %s` to recover it.", s.name, msg, s.name))
						mu.Unlock()
						lines[i].status = "skipped"
						renderProgress(lines)
						atomic.AddInt64(&failed, 1)
						return
					}
					if statusCode == 409 {
						var conflictResp struct {
							RemoteContentHash string `json:"remote_content_hash"`
						}
						json.Unmarshal([]byte(err.Error()), &conflictResp)

						// Auto-detect: if local already matches remote, the marker is stale.
						// Link the marker silently without uploading.
						if conflictResp.RemoteContentHash != "" && conflictResp.RemoteContentHash == contentHash {
							s.marker.ContentHash = contentHash
							if remote, gerr := client.getSkill(s.marker.SkillID); gerr == nil {
								s.marker.Version = remote.Version
							}
							mu.Lock()
							syncState.Skills[s.name] = s.marker
							mu.Unlock()
							lines[i].status = "linked"
							lines[i].pct = 1
							renderProgress(lines)
							atomic.AddInt64(&linked, 1)
							return
						}

						lines[i].status = "CONFLICT"
						renderProgress(lines)

						tmpDir := filepath.Join(os.TempDir(), "airskills-conflicts", s.name)
						os.MkdirAll(tmpDir, 0755)
						rawBody, rawErr := client.get(fmt.Sprintf("/api/v1/skills/%s/raw", s.marker.SkillID))
						if rawErr == nil {
							tmpPath := filepath.Join(tmpDir, "SKILL.md")
							os.WriteFile(tmpPath, rawBody, 0644)
							mu.Lock()
							conflictMessages = append(conflictMessages, conflictInfo{
								name:       s.name,
								localPath:  filepath.Join(s.dir, "SKILL.md"),
								remotePath: tmpPath,
							})
							mu.Unlock()
						}
						atomic.AddInt64(&conflicts, 1)
						return
					}

					lines[i].status = "failed"
					renderProgress(lines)
					mu.Lock()
					warnings = append(warnings, fmt.Sprintf("%s: %v", s.name, err))
					mu.Unlock()
					atomic.AddInt64(&failed, 1)
					return
				}

				if isNew {
					atomic.AddInt64(&created, 1)
					mu.Lock()
					createdNames = append(createdNames, s.name)
					mu.Unlock()
				} else {
					atomic.AddInt64(&pushed, 1)
					mu.Lock()
					pushedNames = append(pushedNames, s.name)
					mu.Unlock()
				}

				// SuggestDeclined deliberately not consulted: a decline
				// applies to the bytes it was asked about. This branch only
				// runs after uploading CHANGED content, so new edits always
				// re-offer the question.
				if s.marker.Source != nil && s.marker.SuggestionID == "" {
					mu.Lock()
					pendingPrompts = append(pendingPrompts, pendingSuggestionPrompt{
						name:             s.name,
						suggesterSkillID: s.marker.SkillID,
						source:           s.marker.Source,
					})
					mu.Unlock()
				}
				if updated != nil {
					s.marker.Version = updated.Version
					s.marker.ContentHash = updated.ContentHash
					if w := strDeref(updated.Warning); w != "" {
						mu.Lock()
						warnings = append(warnings, fmt.Sprintf("%s: %s", s.name, w))
						mu.Unlock()
					}

					// Track current owner namespace in the marker so the CLI
					// always knows which namespace a skill lives in. We do
					// NOT rename the local dir on transfer — the agentskills
					// spec requires the SKILL.md `name` field to match the
					// dir name, so renaming the dir would also require
					// rewriting `name` (a content change). Keep the dir
					// stable; ownership lives in the marker only.
					if updated.CurrentOwner != nil {
						oldSlug := s.marker.OwnerSlug
						newSlug := updated.CurrentOwner.Slug
						if oldSlug != "" && oldSlug != newSlug {
							fmt.Fprintf(os.Stderr,
								"\n  %s moved namespace %s → %s (dir unchanged)\n",
								s.name, oldSlug, newSlug)
						}
						s.marker.OwnerKind = string(updated.CurrentOwner.Kind)
						s.marker.OwnerSlug = updated.CurrentOwner.Slug
					}
				}
				if sizeWarning != "" {
					mu.Lock()
					warnings = append(warnings, sizeWarning)
					mu.Unlock()
				}
				mu.Lock()
				syncState.Skills[s.name] = s.marker
				mu.Unlock()

				lines[i].status = "done"
				lines[i].size = formatSize(archiveSize)
				lines[i].pct = 1
				renderProgress(lines)
			}()
		}

		wg.Wait()

		// Shadow-fork drain: for each non-owned skill the user has edited,
		// fork the upstream into the caller's namespace, upload the edit
		// to the fork, and submit a suggestion. Sequential because each
		// step (prompt, three API calls per skill) is serial — and racing
		// stdin across goroutines is not acceptable.
		if len(pendingShadowForks) > 0 {
			drainShadowForks(client, pendingShadowForks, syncState, &mu, &created, &createdNames, &warnings)
		}

		// Drain sequentially so goroutines don't race on stdin. In a headless
		// session we can't prompt, so print agent-focused instructions instead
		// and leave the entry unmarked — the next interactive push will ask.
		if len(pendingPrompts) > 0 && !isTTY {
			fmt.Fprint(os.Stderr, agentSuggestionInstructions(pendingPrompts))
		}
		if len(pendingPrompts) > 0 && isTTY {
			reader := bufio.NewReader(os.Stdin)
			for _, p := range pendingPrompts {
				fmt.Printf("\n  %s — suggest your edits to %s/%s? [y/N] ", p.name, p.source.Owner, p.source.Slug)
				answer, _ := reader.ReadString('\n')
				answer = strings.TrimSpace(strings.ToLower(answer))

				if answer != "s" && answer != "suggest" && answer != "y" && answer != "yes" {
					if entry, ok := syncState.Skills[p.name]; ok {
						entry.SuggestDeclined = true
					}
					continue
				}

				fmt.Print("  Message (optional, press Enter to skip): ")
				message, _ := reader.ReadString('\n')
				message = strings.TrimSpace(message)

				suggestion, err := client.createSuggestion(
					p.suggesterSkillID,
					p.source.ID,
					p.source.ContentHash,
					message,
				)
				if err != nil {
					fmt.Fprintf(os.Stderr, "  %s suggestion failed: %v\n", yellow("!"), err)
					continue
				}
				fmt.Printf("  %s suggestion sent to %s/%s\n", green("✓"), p.source.Owner, p.source.Slug)
				if entry, ok := syncState.Skills[p.name]; ok {
					entry.SuggestionID = suggestion.Id.String()
				}
			}
		}

		saveSyncState(syncState)

		parts := []string{}
		if pushed > 0 {
			parts = append(parts, green(fmt.Sprintf("%d pushed", pushed)))
			for _, n := range pushedNames {
				fmt.Printf("  %s %s\n", green("↑"), n)
			}
		}
		if created > 0 {
			parts = append(parts, green(fmt.Sprintf("%d created", created)))
			for _, n := range createdNames {
				fmt.Printf("  %s %s\n", green("+"), n)
			}
		}
		if linked > 0 {
			parts = append(parts, fmt.Sprintf("%d linked", linked))
		}
		if renamed > 0 {
			parts = append(parts, fmt.Sprintf("%d renamed", renamed))
		}
		if conflicts > 0 {
			parts = append(parts, red(fmt.Sprintf("%d conflicts", conflicts)))
		}
		if failed > 0 {
			parts = append(parts, red(fmt.Sprintf("%d failed", failed)))
		}
		if len(orphanRemoved) > 0 {
			parts = append(parts, fmt.Sprintf("%d orphan removed", len(orphanRemoved)))
		}
		if len(orphanKept) > 0 {
			parts = append(parts, yellow(fmt.Sprintf("%d orphan with local edits", len(orphanKept))))
		}
		if len(movedKept) > 0 {
			parts = append(parts, yellow(fmt.Sprintf("%d moved (re-link needed)", len(movedKept))))
		}
		if len(transient) > 0 {
			parts = append(parts, dim(fmt.Sprintf("%d couldn't verify", len(transient))))
		}
		if unchangedCount > 0 {
			parts = append(parts, dim(fmt.Sprintf("%d unchanged", unchangedCount)))
		}
		if len(parts) == 0 {
			parts = append(parts, dim("all unchanged"))
		}
		fmt.Printf("\n%s\n", strings.Join(parts, ", "))

		// Per-skill detail for actions we took on skipped markers. Always
		// print these by default — the user removed/transferred something
		// elsewhere, and they need to see the consequence on this machine
		// plus how to undo it.
		orgSlugByID, callerOrgSlugs := movedDestinationOrgLookups(client, movedKept)
		for _, a := range orphanRemoved {
			fmt.Printf("  %s %s: removed locally (deleted server-side).\n", green("-"), a.name)
			fmt.Printf("       Undo: airskills restore %s\n", a.name)
		}
		for _, a := range orphanKept {
			fmt.Printf("  %s %s: deleted server-side; local edits kept and marked untracked.\n", yellow("!"), a.name)
			fmt.Printf("       Restore server version: airskills restore %s\n", a.name)
			fmt.Printf("       Discard local copy:     airskills rm --keep-remote %s\n", a.name)
		}
		for _, a := range movedKept {
			fmt.Printf("  %s %s: moved to %s/%s; local kept and marked untracked.\n", yellow("!"), a.name, a.newOwnerSlug, a.newSkillSlug)
			switch {
			case movedDestinationInEffectiveSet(a, remoteSkills, orgSlugByID):
				fmt.Println("       New owner org membership detected; this skill is in a skillset this machine receives. Next sync will re-link automatically. No action needed.")
			case a.newOwnerKind == "org" && callerOrgSlugs[a.newOwnerSlug]:
				fmt.Printf("       This org skill is not in a skillset you receive. To avoid creating a duplicate, run: airskills rm --keep-remote %s\n", a.name)
				fmt.Println("       Then ask an admin of the new owner org to add you to a skillset that contains it.")
			default:
				fmt.Printf("       Follow updates from the new owner: airskills rm --keep-remote %s && airskills add %s/%s\n", a.name, a.newOwnerSlug, a.newSkillSlug)
				fmt.Printf("       Discard local copy:                airskills rm --keep-remote %s\n", a.name)
			}
		}
		for _, a := range transient {
			fmt.Printf("  %s %s: couldn't verify server state — will retry next sync.\n", dim("?"), a.name)
		}
		for _, w := range earlyWarnings {
			fmt.Printf("  %s %s\n", yellow("!"), w)
		}

		if len(warnings) > 0 {
			for _, w := range warnings {
				fmt.Printf("  %s %s\n", yellow("!"), w)
			}
		}

		telemetry.Capture("cli_push", map[string]interface{}{
			"pushed":         pushed,
			"created":        created,
			"linked":         linked,
			"renamed":        renamed,
			"conflicts":      conflicts,
			"failed":         failed,
			"orphan_removed": len(orphanRemoved),
			"orphan_kept":    len(orphanKept),
			"moved_kept":     len(movedKept),
			"transient":      len(transient),
			"force":          pushForce,
		})

		if len(validationMessages) > 0 {
			fmt.Println("\n--- Invalid SKILL.md frontmatter ---")
			for _, v := range validationMessages {
				fmt.Printf("\n  %s\n", v.name)
				fmt.Printf("  %s\n", strings.ReplaceAll(v.err.Error(), "\n", "\n  "))
			}
		}

		// Show conflict resolution instructions. During `sync`, pull emits the
		// canonical block after it has full sync-state context.
		if len(conflictMessages) > 0 && cmd.Name() != "sync" {
			var entries []conflictEntry
			for _, c := range conflictMessages {
				var source *skillSource
				if entry := syncState.Skills[c.name]; entry != nil {
					source = entry.Source
				}
				entries = append(entries, conflictEntry{
					name:      c.name,
					localDir:  filepath.Dir(c.localPath),
					remoteDir: filepath.Dir(c.remotePath),
					source:    source,
				})
			}
			fmt.Print(conflictResolutionMessage(entries, !isTTY))
		}

		// Next-step hints for an agent. Skip when called from `sync` —
		// sync prints its own consolidated block after pull so we don't
		// double up.
		if cmd.Name() != "sync" {
			steps := []agentNextStep{
				{Cmd: "airskills status", Why: "confirm everything landed on the server"},
			}
			if len(conflictMessages) > 0 {
				steps = []agentNextStep{
					{Cmd: "airskills push --force", Why: "re-push after merging the conflicts above"},
				}
			}
			printAgentNextSteps(os.Stdout, steps)
		}

		return nil
	},
}

func movedDestinationOrgLookups(client *apiClient, moved []skippedAction) (map[string]string, map[string]bool) {
	needsOrgs := false
	for _, a := range moved {
		if a.newOwnerKind == "org" {
			needsOrgs = true
			break
		}
	}
	if !needsOrgs {
		return nil, nil
	}
	orgs, err := listCallerOrgs(client)
	if err != nil {
		return nil, nil
	}
	orgSlugByID := make(map[string]string, len(orgs))
	callerOrgSlugs := make(map[string]bool, len(orgs))
	for _, org := range orgs {
		orgSlugByID[org.ID] = org.Slug
		callerOrgSlugs[org.Slug] = true
	}
	return orgSlugByID, callerOrgSlugs
}

func movedDestinationInEffectiveSet(a skippedAction, remoteSkills []apiSkill, orgSlugByID map[string]string) bool {
	if a.newOwnerKind != "org" || a.newOwnerSlug == "" || a.newSkillSlug == "" {
		return false
	}
	for i := range remoteSkills {
		skill := remoteSkills[i]
		if skill.Slug != a.newSkillSlug || skill.OrgId == nil {
			continue
		}
		if orgSlugByID[skill.OrgId.String()] == a.newOwnerSlug {
			return true
		}
	}
	return false
}

func init() {
	pushCmd.Flags().BoolVar(&pushForce, "force", false, "Skip conflict check (use after resolving conflicts)")
	pushCmd.Flags().BoolVar(&pushForceSuggest, "force-suggest", false, "Submit a suggestion even when upstream has changed")
	pushCmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "Show per-skill progress (and the files excluded by .askignore/.gitignore)")
	pushCmd.Flags().StringVar(&pushOrg, "org", "", "Create new skills under this org (org admins only)")
	pushCmd.Flags().BoolVar(&pushNoIgnore, "no-ignore", false, "Bypass .askignore and .gitignore for this push (built-in noise like node_modules is still excluded)")
	rootCmd.AddCommand(pushCmd)
}

func remoteForUpstreamCheck(marker *SyncEntry, remoteByID map[string]*apiSkill) *apiSkill {
	if marker == nil || marker.Source == nil {
		return nil
	}
	if marker.SkillID != "" {
		if remote := remoteByID[marker.SkillID]; remote != nil {
			return remote
		}
	}
	return remoteByID[sourceUpstreamID(marker.Source)]
}

// createTarGz builds the push tarball, honouring per-skill .askignore /
// .gitignore (plus built-in noise). Fails fast if the matcher would exclude
// SKILL.md — a skill without its manifest is broken and the user needs to
// see and fix the offending rule.
func createTarGz(dir string) ([]byte, error) {
	return createTarGzWith(dir, newPushMatcher(dir))
}

func createTarGzWith(dir string, matcher *ignoreMatcher) ([]byte, error) {
	if err := matcher.CheckSkillFile(); err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)

	base := filepath.Base(dir)

	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		// Skip .airskills marker
		if info.Name() == ".airskills" {
			return nil
		}

		rel, _ := filepath.Rel(dir, path)
		if rel != "." {
			if ignored, _ := matcher.Decide(rel, info.IsDir()); ignored {
				if info.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
		}

		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}

		// Use relative path inside the archive (always forward slashes for tar)
		header.Name = filepath.ToSlash(filepath.Join(base, rel))

		if err := tw.WriteHeader(header); err != nil {
			return err
		}

		if info.IsDir() {
			return nil
		}

		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(tw, f)
		return err
	})

	if err != nil {
		return nil, err
	}

	tw.Close()
	gz.Close()
	return buf.Bytes(), nil
}

// readSkillFiles reads all files in a skill directory (excluding .airskills
// marker, universal dev noise, and per-skill .askignore/.gitignore patterns —
// see ignoreMatcher). Used as the canonical "what belongs to this skill"
// view by hash computation, diff, status, doctor, pull and push.
func readSkillFiles(dir string) map[string][]byte {
	return readSkillFilesWith(dir, newPushMatcher(dir))
}

func readSkillFilesWith(dir string, matcher *ignoreMatcher) map[string][]byte {
	files := map[string][]byte{}
	filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.Name() == ".airskills" {
			return nil
		}
		rel, _ := filepath.Rel(dir, path)
		if rel != "." {
			if ignored, _ := matcher.Decide(rel, info.IsDir()); ignored {
				if info.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
		}
		if info.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err == nil {
			// Normalise to forward-slash so the merkle hash is identical
			// on Windows and Unix for the same skill content.
			files[filepath.ToSlash(rel)] = data
		}
		return nil
	})
	return files
}

// newPushMatcher returns the matcher used by the push code paths. When
// `--no-ignore` is set, the matcher is empty so only built-in noise applies
// — user .askignore / .gitignore rules are bypassed. Built-in noise is
// always enforced even with `--no-ignore` (you really don't want node_modules
// in your skill upload).
func newPushMatcher(dir string) *ignoreMatcher {
	if pushNoIgnore {
		return &ignoreMatcher{}
	}
	return newIgnoreMatcher(dir)
}

// propagatePartialRenames detects skills whose dir was manually renamed in
// some agents but not others (so the same skill exists under two names),
// and propagates the rename to every agent that still has the old name.
// Mutates `localSkills` in place — the renamed-away old name is dropped so
// downstream rename detection treats it as an orphan and reuses the marker.
//
// Without this, a `mv ~/.claude/skills/X ~/.claude/skills/Y` while
// `~/.cursor/skills/X` still exists would silently push Y as a brand-new
// skill on the server, leaving the original X skill orphaned.
// drainShadowForks executes the fork-then-suggest flow for each non-owned
// skill the user has edited locally. Each entry runs sequentially: fork
// the upstream into the caller's namespace, upload the edited bytes to
// the fork, submit a suggestion against the upstream, then rewrite the
// marker so the local dir is now tracked to the fork. Source stays
// pointing at the upstream so subsequent edits keep going through the
// suggest path and the (separate) incorporate-upstream-changes ticket
// has the link it needs.
//
// Best-effort across failures: if createSkill works but the upload or
// suggestion call fails, the marker still records the new fork id —
// otherwise the next push would re-fork and accumulate duplicate rows.
// Errors are surfaced via the shared warnings slice.
func drainShadowForks(
	client *apiClient,
	queue []pendingShadowFork,
	syncState *SyncState,
	mu *sync.Mutex,
	created *int64,
	createdNames *[]string,
	warnings *[]string,
) {
	// One profile fetch per push run, shared across queue entries — we
	// need the caller's username for the summary line and the rewritten
	// marker.
	profile, profileErr := client.getMe()
	var callerUsername string
	if profileErr == nil && profile != nil {
		callerUsername = profile.Username
	}

	reader := bufio.NewReader(os.Stdin)

	for _, p := range queue {
		// 1+2. Back up the edit unconditionally: create the fork in the
		// caller's namespace and upload the local content to it. The fork
		// is plumbing, not a user decision — nothing the user typed (or
		// declined) should ever lose their edited copy. expectedHash is
		// empty: the fork is brand-new and has no prior hash to conflict
		// on.
		fork, err := client.createSkill(p.source.Slug, "", []string{"claude-code"}, p.source.ID, "")
		if err != nil {
			mu.Lock()
			*warnings = append(*warnings, fmt.Sprintf(
				"%s: could not save your edits to your account: %v. Local edit kept; nothing pushed upstream.",
				p.name, err))
			mu.Unlock()
			continue
		}
		updated, _, archiveErr := client.putArchive(fork.Id.String(), p.archive, "", p.contentHash)

		// 3. The only real decision: suggest the edit to the upstream
		// owner? Headless runs answer yes (matches the pre-existing
		// non-interactive behaviour); interactive runs get the question.
		suggest := true
		if isTTY {
			fmt.Printf("\n  %s — suggest your edits to %s/%s? [Y/n] ", p.name, p.source.Owner, p.source.Slug)
			answer, _ := reader.ReadString('\n')
			answer = strings.TrimSpace(strings.ToLower(answer))
			if answer == "n" || answer == "no" {
				suggest = false
			}
		}

		// base_content_hash is the upstream baseline we last sync'd
		// (stored on the marker's Source).
		var suggestErr error
		suggestionID := ""
		if suggest {
			suggestion, serr := client.createSuggestion(
				fork.Id.String(), p.source.ID, p.source.ContentHash, "")
			suggestErr = serr
			if suggestion != nil {
				suggestionID = suggestion.Id.String()
			}
		}

		// 4. Rewrite the marker even on partial failure — the fork now
		// exists server-side and must be tracked, otherwise the next
		// push would re-fork.
		mu.Lock()
		entry := syncState.Skills[p.name]
		if entry == nil {
			entry = &SyncEntry{Tool: "claude-code"}
		}
		entry.SkillID = fork.Id.String()
		entry.OwnerKind = "user"
		if callerUsername != "" {
			entry.OwnerSlug = callerUsername
		}
		entry.Source = p.source // unchanged — keeps the upstream link for future syncs
		entry.ContentHash = p.contentHash
		if updated != nil {
			entry.Version = updated.Version
			if updated.ContentHash != "" {
				entry.ContentHash = updated.ContentHash
			}
		} else {
			entry.Version = fork.Version
		}
		if suggestionID != "" {
			entry.SuggestionID = suggestionID
		}
		entry.SuggestDeclined = !suggest
		entry.Deleted = false
		entry.MovedTo = ""
		syncState.Skills[p.name] = entry

		if archiveErr != nil {
			*warnings = append(*warnings, fmt.Sprintf(
				"%s: your edits are saved but the upload failed: %v. Re-run push to retry the upload.",
				p.name, archiveErr))
		}
		if suggestErr != nil {
			*warnings = append(*warnings, fmt.Sprintf(
				"%s: your edits are saved but the suggestion failed: %v. Re-run push to retry.",
				p.name, suggestErr))
		}

		atomic.AddInt64(created, 1)
		*createdNames = append(*createdNames, p.name)
		mu.Unlock()

		if archiveErr == nil && suggestErr == nil {
			if suggest {
				fmt.Printf("  %s %s: edits saved; suggestion sent to %s/%s\n",
					green("✓"), p.name, p.source.Owner, p.source.Slug)
			} else {
				fmt.Printf("  %s %s: edits saved; no suggestion sent\n",
					green("✓"), p.name)
			}
		}
	}
}

func propagatePartialRenames(localSkills map[string]string, syncState *SyncState) {
	for newName, dir := range localSkills {
		if _, tracked := syncState.Skills[newName]; tracked {
			continue
		}
		rawHash := computeMerkleHash(readSkillFiles(dir))
		for oldName, marker := range syncState.Skills {
			if oldName == newName || marker.ContentHash == "" || marker.ContentHash != rawHash {
				continue
			}
			if _, oldStillThere := localSkills[oldName]; !oldStillThere {
				continue // already a clean orphan; the existing detector will handle it
			}
			if err := renameSkillDirAcrossAgents(oldName, newName); err != nil {
				fmt.Fprintf(os.Stderr, "  ! could not propagate rename %s → %s across agents: %v\n", oldName, newName, err)
				break
			}
			delete(localSkills, oldName)
			fmt.Printf("  %s detected partial rename: %s → %s, propagated across all agent dirs\n", green("✓"), oldName, newName)
			break
		}
	}
}
