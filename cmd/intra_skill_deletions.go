package cmd

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// suppressDeletionPrompt disables the intra-skill deletion resolver for the
// duration of a nested push. The `airskills rm <skill>/<path>` command
// deletes a file from every agent copy and then pushes; without this guard
// the resolver would notice the file is missing-versus-remote and offer to
// restore it — undoing the deletion the user explicitly asked for. Set it
// around that internal push (see runRemoveSkillFile).
var suppressDeletionPrompt bool

// deletionDecision is how the resolver acts on a confirmed local deletion.
type deletionDecision int

const (
	// deletionAsk prompts the user per skill (interactive terminals).
	deletionAsk deletionDecision = iota
	// deletionRemove permanently removes without asking (`push --force`).
	deletionRemove
	// deletionKeep restores the files from the remote and prints a hint —
	// the safe default for headless runs, which must never destroy data
	// without explicit confirmation.
	deletionKeep
)

// detectDeletedSkillFiles returns the relative paths that exist in the
// remote manifest but are missing from at least one local agent copy of the
// skill — i.e. files the user hand-deleted. The remote manifest is the only
// reliable baseline: a file present in one mirror but not another is
// ambiguous on its own (deleted here, or added there and not yet
// propagated?), and the remote tells us which. "Missing from at least one
// copy" (not from the union) is deliberate — it catches both a single-agent
// delete and a partial multi-agent delete that the mirror would otherwise
// resurrect. A file the user excludes locally via .askignore/.gitignore is
// never flagged (deliberately kept out, not deleted), and SKILL.md is never
// flagged (removing the manifest would break the skill). Pure; reads the
// copies only.
func detectDeletedSkillFiles(localDirs []string, remotePaths []string) []string {
	if len(localDirs) == 0 {
		return nil
	}

	copies := make([]map[string][]byte, 0, len(localDirs))
	matchers := make([]*ignoreMatcher, 0, len(localDirs))
	for _, d := range localDirs {
		copies = append(copies, readSkillFiles(d))
		matchers = append(matchers, newIgnoreMatcher(d))
	}

	var missing []string
	for _, rel := range remotePaths {
		if rel == "SKILL.md" {
			continue
		}
		ignored := false
		for _, m := range matchers {
			if ig, _ := m.Decide(filepath.FromSlash(rel), false); ig {
				ignored = true
				break
			}
		}
		if ignored {
			continue
		}
		for _, c := range copies {
			if _, ok := c[rel]; !ok {
				missing = append(missing, rel)
				break
			}
		}
	}
	sort.Strings(missing)
	return missing
}

// resolveIntraSkillDeletions detects files the user removed from tracked
// skills (present on the remote baseline, absent from a local copy) and
// applies the given decision. It runs as a pre-pass before the mirror in
// both push and pull so the two commands share one code path — the mirror
// itself would otherwise resurrect a partially-deleted file from a sibling
// agent dir. Best-effort: any skill it can't fetch a baseline for is left
// to the mirror.
//
// scope limits which skills are considered: a non-empty scope (the
// positional args of a scoped `airskills push <skill>`) restricts detection
// — and especially the destructive --force removal — to exactly those
// skills, so `push triage --force` can never delete files from an unrelated
// skill. An empty scope considers every local skill (plain push / pull /
// sync).
func resolveIntraSkillDeletions(client *apiClient, syncState *SyncState, decision deletionDecision, scope []string) {
	if suppressDeletionPrompt || client == nil || syncState == nil {
		return
	}

	var scopeSet map[string]bool
	if len(scope) > 0 {
		scopeSet = make(map[string]bool, len(scope))
		for _, s := range scope {
			scopeSet[s] = true
		}
	}

	slugToPaths, _, err := scanSkillsAllPaths()
	if err != nil {
		return
	}

	slugs := make([]string, 0, len(slugToPaths))
	for slug := range slugToPaths {
		slugs = append(slugs, slug)
	}
	sort.Strings(slugs)

	for _, slug := range slugs {
		if scopeSet != nil && !scopeSet[slug] {
			continue
		}
		dirs := slugToPaths[slug]
		entry := syncState.Skills[slug]
		if entry == nil || entry.SkillID == "" || entry.Deleted {
			continue
		}

		// Cheap, network-free gate: a skill whose every local copy still
		// matches the last-synced marker hash can't contain a deletion, so
		// skip it. Only skills the user actually changed cost a manifest
		// fetch — and that's exactly the set push is about to upload
		// anyway. (Empty marker hash = never synced; nothing to delete
		// against, skip.)
		if entry.ContentHash == "" || !skillChangedVsMarker(dirs, entry.ContentHash) {
			continue
		}

		// Fetch the remote manifest (paths only — no file bodies) and look
		// for files the user removed locally.
		remotePaths, perr := client.getSkillFilePaths(entry.SkillID)
		if perr != nil {
			continue
		}
		missing := detectDeletedSkillFiles(dirs, remotePaths)
		if len(missing) == 0 {
			continue
		}
		applyDeletionDecision(client, entry.SkillID, slug, dirs, missing, decision)
	}
}

// skillChangedVsMarker reports whether any local copy of the skill differs
// from the last-synced marker hash.
func skillChangedVsMarker(dirs []string, markerHash string) bool {
	for _, d := range dirs {
		if computeMerkleHash(readSkillFiles(d)) != markerHash {
			return true
		}
	}
	return false
}

func applyDeletionDecision(client *apiClient, skillID, slug string, dirs, missing []string, decision deletionDecision) {
	switch decision {
	case deletionAsk:
		fmt.Printf("\n  %s %s: these files were removed locally but still exist on the server:\n", yellow("?"), slug)
		for _, f := range missing {
			fmt.Printf("      - %s\n", f)
		}
		fmt.Printf("  Permanently remove %s from the server and every agent copy? [y/N] ", filesNoun(len(missing)))
		reader := bufio.NewReader(os.Stdin)
		answer, _ := reader.ReadString('\n')
		if strings.TrimSpace(strings.ToLower(answer)) == "y" {
			removeMissing(slug, missing)
			fmt.Printf("  %s removed; the deletion will be pushed.\n", green("-"))
		} else {
			restoreMissing(client, skillID, slug, dirs, missing)
			fmt.Printf("  %s kept; restored from the server where missing.\n", dim("·"))
		}
	case deletionRemove:
		removeMissing(slug, missing)
		fmt.Printf("  %s %s: removing %s (deletion will be pushed): %s\n",
			green("-"), slug, filesNoun(len(missing)), strings.Join(missing, ", "))
	case deletionKeep:
		restoreMissing(client, skillID, slug, dirs, missing)
		fmt.Printf("  %s %s: %s removed locally but kept (still on the server). To remove for good: airskills rm %s/%s\n",
			yellow("!"), slug, filesNoun(len(missing)), slug, missing[0])
	}
}

// removeMissing deletes each path from every agent copy of the skill so the
// mirror can't resurrect it; the follow-on push then drops it server-side.
// A path already absent from all copies is a no-op (the not-found error
// from removeLocalSkillFile is expected and ignored).
func removeMissing(slug string, missing []string) {
	for _, rel := range missing {
		_, _ = removeLocalSkillFile(slug, rel)
	}
}

// restoreMissing rewrites each missing path back into any agent copy that
// lacks it, using the server's content as the source of truth, so all
// copies match the server again and the mirror leaves them alone. The
// content isn't in the (paths-only) manifest, so it downloads the archive
// here — lazily, only when we're actually keeping files (the rare path).
func restoreMissing(client *apiClient, skillID, slug string, dirs, missing []string) {
	remoteFiles, err := downloadSkillFiles(client, skillID)
	if err != nil {
		fmt.Printf("  %s %s: could not fetch server copy to restore %s: %v\n",
			yellow("!"), slug, filesNoun(len(missing)), err)
		return
	}
	for _, dir := range dirs {
		for _, rel := range missing {
			content, ok := remoteFiles[rel]
			if !ok {
				continue
			}
			target := filepath.Join(dir, filepath.FromSlash(rel))
			if _, err := os.Stat(target); err == nil {
				continue // copy already has it
			}
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				continue
			}
			_ = os.WriteFile(target, content, fileMode(content))
		}
	}
}

func filesNoun(n int) string {
	if n == 1 {
		return "this file"
	}
	return fmt.Sprintf("these %d files", n)
}
