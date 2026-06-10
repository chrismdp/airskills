package cmd

import "time"

// classifyCopyDivergence decides which content is authoritative across the
// on-disk copies of a skill, using ONLY the per-copy baseline ledger — never
// file mtime, and never the marker's single ContentHash as a stand-in baseline
// (which is unsafe: an optimistically-advanced marker matches the edit, not the
// pre-edit baseline, so trusting it can revert the edit). It returns the hash
// to converge on, or an empty hash plus the paths to surface when it cannot
// prove which copy is right.
//
// Inputs mirror what mirrorLocalSkills already computes: paths is the set of
// skill dirs that hold the skill, hashByPath their current Merkle hashes, and
// hashGroups the inverse (hash -> paths).
//
// Decision:
//
//  1. All copies already agree → that hash, trivially (and the ledger seeds
//     itself from this state on the clean pass that follows).
//  2. Copies diverge AND the ledger records a baseline for EVERY present copy
//     → compare each to its own baseline. Exactly one distinct edited hash →
//     that edit wins and fans out. Anything else (none moved yet copies still
//     differ; two or more distinct edits) → a fork we won't flatten → surface.
//  3. Copies diverge and the ledger is absent or does not cover every present
//     copy (first run after upgrade, a never-synced skill, or a freshly-added
//     agent dir) → we have no trustworthy history, so we refuse to guess and
//     surface it. Identical copies still converge via step 1 and seed the
//     ledger, so steady state is always step 1 or 2; only a genuine
//     divergence-without-history reaches here, and surfacing it is the only
//     choice that cannot silently lose an edit.
func classifyCopyDivergence(
	marker *SyncEntry,
	paths []string,
	hashByPath map[string]string,
	hashGroups map[string][]string,
) (authHash string, forkPaths []string) {
	// 1) Unanimous — no divergence to resolve.
	if len(hashGroups) == 1 {
		for h := range hashGroups {
			return h, nil
		}
	}

	// 2/3) Copies diverge — only the ledger may decide, and only if it covers
	// every present copy. A present copy the ledger has never recorded means
	// incomplete history: surface rather than guess.
	if marker == nil || len(marker.Copies) == 0 {
		return "", paths
	}
	editedHashes := map[string]bool{}
	for _, p := range paths {
		base, known := ledgerBaseline(marker, p)
		if !known {
			return "", paths
		}
		if hashByPath[p] != base {
			editedHashes[hashByPath[p]] = true
		}
	}
	if len(editedHashes) == 1 {
		for h := range editedHashes {
			return h, nil
		}
	}
	// Zero edits but copies still differ (inconsistent ledger), or two-plus
	// independent edits (a real fork) → surface every copy for reconciliation.
	return "", paths
}

// ledgerBaseline returns the recorded baseline hash for one skill-dir copy and
// whether the ledger knows that copy at all. Unlike a marker-wide hash, a copy
// the ledger has never seen returns known=false so the caller skips it rather
// than treating "unknown" as "edited".
func ledgerBaseline(marker *SyncEntry, dir string) (hash string, known bool) {
	if marker == nil {
		return "", false
	}
	cs, ok := marker.Copies[dir]
	if !ok || cs.Hash == "" {
		return "", false
	}
	return cs.Hash, true
}

// recordCopyBaselines rewrites the marker's per-copy ledger so every dir that
// now holds the authoritative content is stamped with it. The map is rebuilt
// from scratch each time so dirs that no longer hold the skill drop out. No-op
// for a nil marker (untracked skill — nowhere to store the ledger).
func recordCopyBaselines(marker *SyncEntry, authoritativeDirs []string, authHash string) {
	if marker == nil || authHash == "" || len(authoritativeDirs) == 0 {
		return
	}
	now := time.Now()
	copies := make(map[string]CopyState, len(authoritativeDirs))
	for _, dir := range authoritativeDirs {
		copies[dir] = CopyState{Hash: authHash, SyncedAt: now}
	}
	marker.Copies = copies
}
