package cmd

// SkillState is the presence/lifecycle state of a single skill on this
// machine — the FIRST axis of the unified divergence model
// (platform/doc/changes/unify-skill-divergence-state-model.md). It is
// determined first and is mutually exclusive, from the (Local, Marker,
// Remote) presence triple alone.
//
// The SECOND axis — HOW a tracked skill diverges — is carried by the three
// orthogonal booleans on SkillStateInfo (LocalDirty, RemoteMoved,
// UpstreamMoved), never by this enum. The old flat vocabulary
// (synced/modified/modified-pending) collapsed those two axes into one 1-D
// encoding and could not name the (clean, upstream-moved) cell, which is
// why `list` and `status` disagreed. `decideState` is the single
// divergence classifier; every command projects from its output.
//
// User-facing description: doc/internals/sync-state.mdx in the platform repo.
type SkillState string

const (
	// StateAvailable — server has a skill the user has not installed on
	// this machine. Rendered as "—" in `airskills list`. (was not-local)
	StateAvailable SkillState = "available"

	// StateUntracked — local dir exists, no marker, and the server has no
	// skill that could account for it. Common when a skill arrived via git
	// rather than `airskills add`.
	StateUntracked SkillState = "untracked"

	// StateAdoptable — local dir exists, no marker, but the server has a
	// skill of the same name whose bytes match exactly. Next sync silently
	// claims it. (was linked)
	StateAdoptable SkillState = "adoptable"

	// StateConflict — local dir exists, no marker, server has a same-named
	// skill whose bytes differ. Folds the old untracked-conflict AND the
	// parked pending-conflict copy into one state, one report. (was
	// untracked-conflict)
	StateConflict SkillState = "conflict"

	// StateTracked — local dir, marker, and remote all present. How it
	// diverges is read off the (LocalDirty, RemoteMoved, UpstreamMoved)
	// booleans, not off this value. (replaces synced / modified /
	// modified-pending)
	StateTracked SkillState = "tracked"

	// StateOrphaned — local dir + marker, but the server no longer has the
	// skill (404 / archived). (was markerStateOrphan / orphan-*)
	StateOrphaned SkillState = "orphaned"

	// StateDisplaced — local dir + marker, but the name now belongs to a
	// different skill on the server (transferred away or org-shadowed).
	// (folds markerStateMoved / moved-keep / shadowed)
	StateDisplaced SkillState = "displaced"
)

// SkillStateInfo is one row of classifier output. The classifier emits one
// row per server-known skill plus one row per local directory that has no
// marker and no matching server skill.
type SkillStateInfo struct {
	// Name is the local directory name when the skill exists locally,
	// otherwise the server slug. Stable across server-side renames because
	// tracking matches by skill_id first.
	Name string

	State SkillState

	// Local is true when the skill has a directory on this machine.
	Local bool

	// Remote is the server-side view, if the server knows about this skill.
	// Nil for purely-local untracked directories.
	Remote *apiSkill

	// Marker is the sync.json entry for this skill, if one exists.
	Marker *SyncEntry

	// LocalHash is the Merkle hash of on-disk content. Empty when the skill
	// is not present locally.
	LocalHash string

	// The four divergence coordinates of the unified model. Meaningful only
	// when State == StateTracked; all false otherwise. Sourced is the
	// ownership axis; the other three are the orthogonal divergence flags.
	Sourced       bool // a Source/forked_from upstream pointer exists
	LocalDirty    bool // working ≠ base — I have edits not yet pushed
	RemoteMoved   bool // my_remote_head ≠ base — another of my machines pushed
	UpstreamMoved bool // forks only: the parent moved past upstream_base

	// Overlay marks the one-skill shape for non-owned skills: the marker
	// tracks the UPSTREAM id, local edits are an overlay backed up in a
	// hidden fork (cli-one-skill-overlay-and-lineage-split.md). For overlay
	// rows the matched remote IS the upstream, so RemoteMoved stays false;
	// standing divergence from the upstream is OverlayDiverged ("local
	// changes"), and UpstreamMoved means the upstream moved past the
	// baseline the user last incorporated.
	Overlay         bool
	OverlayDiverged bool // local ≠ upstream head — the user's standing edits
}

// markerUpstreamBase returns the parent version the consumer last
// acknowledged — the model's `upstream_base`. It collapses the two fields
// the marker used to split this across: ResolvedHash (written by `resolve`
// and `pull --keep-local`) and Source.UpstreamContentHash (written by `add`
// and `pull`). Both are READ here for back-compat; the canonical field
// going forward is ResolvedHash, which is the only one written for the
// "acknowledge" semantics. Source.ContentHash is the legacy pre-upstream
// fallback. Empty for owned skills.
func markerUpstreamBase(m *SyncEntry) string {
	if m == nil || m.Source == nil {
		return ""
	}
	if m.ResolvedHash != "" {
		return m.ResolvedHash
	}
	if m.Source.UpstreamContentHash != "" {
		return m.Source.UpstreamContentHash
	}
	return m.Source.ContentHash
}

// skillUpstreamMoved is the model's `upstreamMoved` — the ONLY correct "new
// upstream available" signal: the parent skill moved past the upstream
// version I last acknowledged (`parent_head ≠ upstream_base`).
//
// Gated on the server populating UpstreamContentHash, which it does ONLY for
// genuine forks ("Live content_hash of the parent skill (forks only)"). This
// keeps the fork axis independent of the own-copy axis: for a fork the
// parent's head is a DIFFERENT hash from my fork's own head, so this flag
// composes with RemoteMoved instead of doubling it. For an un-forked sourced
// skill (a plain `add`, or an org-distributed skill) the matched remote row
// IS the upstream — there is no separate parent head, so the move shows on
// the own axis (RemoteMoved) and this stays false, exactly as before.
//
// Replaces skillHasUpstreamUpdate, whose comparison (fork content vs parent
// head) is always true for a fork — it cannot tell "I customised" from "the
// parent moved." See bug #2 in the spec.
func skillUpstreamMoved(m *SyncEntry, r *apiSkill) bool {
	if m == nil || m.Source == nil || r == nil || r.UpstreamContentHash == nil {
		return false
	}
	parentHead := *r.UpstreamContentHash
	base := markerUpstreamBase(m)
	return parentHead != "" && base != "" && parentHead != base
}

// classifySkills returns the cross-state of every skill the CLI knows about:
// every server-known skill, plus any local directories that aren't accounted
// for by the server. It is pure — no I/O — and takes a hashLocal callback so
// callers can supply readSkillFiles + computeMerkleHash without this package
// taking an indirect dependency on disk state in tests.
//
// Matching priority for tracked skills: skill_id first (so server-side
// renames are followed), then local directory name. Each output row
// represents exactly one skill; we deduplicate by the local dir name when
// the skill is present locally and by server slug otherwise.
func classifySkills(
	remote []apiSkill,
	local map[string]string,
	state *SyncState,
	hashLocal func(path string) string,
) []SkillStateInfo {
	if state == nil {
		state = &SyncState{Version: 1, Skills: map[string]*SyncEntry{}}
	}

	// Backup rows are plumbing (filtered below), but a legacy/broken marker
	// may still track one directly (pre-heal shape: SkillID = the backup
	// fork). Map such markers to the backup's upstream so they match the
	// upstream row instead of matching nothing — without this the upstream
	// row pairs with the local dir as a marker-less conflict and renders
	// "untracked" (field report 2026-06-11).
	backupUpstream := map[string]string{}
	for i := range remote {
		if isBackupRow(&remote[i]) {
			backupUpstream[remote[i].Id.String()] = remote[i].ForkedFrom.String()
		}
	}

	skillIDToName := map[string]string{}
	for name, entry := range state.Skills {
		if entry != nil && entry.SkillID != "" {
			id := entry.SkillID
			if up, ok := backupUpstream[id]; ok {
				id = up
			}
			skillIDToName[id] = name
		}
	}

	results := []SkillStateInfo{}
	consumedLocal := map[string]bool{}

	for i := range remote {
		r := remote[i]

		// Hidden overlay-backup forks are plumbing, never a row: the skill
		// they back up renders once (as the upstream), with the divergence
		// carried by the marker's Backup ref. Rendering these used to
		// produce the two-rows-for-one-skill shape and a phantom untracked
		// conflict (cli-one-skill-overlay-and-lineage-split.md).
		if isBackupRow(&r) {
			continue
		}

		// Tracked match by skill_id wins. Falls back to dir-name match for
		// legacy markers that might not have a skill_id (defensive).
		trackedName := skillIDToName[r.Id.String()]
		var marker *SyncEntry
		if trackedName != "" {
			marker = state.Skills[trackedName]
		}

		// Resolve the local dir for this remote.
		localName, localPath := "", ""
		if trackedName != "" {
			if path, ok := local[trackedName]; ok {
				localName, localPath = trackedName, path
			}
		}
		if localName == "" {
			// No marker, or marker but no local dir under tracked name —
			// look for an untracked local with the same name as the remote.
			// This is the untracked / adoptable / conflict branch.
			if path, ok := local[r.Name]; ok && trackedName == "" {
				localName, localPath = r.Name, path
			}
		}

		info := SkillStateInfo{
			Remote: copyRemote(r),
			Marker: marker,
		}

		if localName != "" {
			info.Name = localName
			info.Local = true
			info.LocalHash = hashLocal(localPath)
			consumedLocal[localName] = true
		} else {
			info.Name = r.Name
		}

		info.State = decideState(info)
		deriveDivergence(&info)
		results = append(results, info)
	}

	// Any local directory that didn't pair up with a remote. With a marker
	// but no remote match the server has dropped the skill: orphaned, or —
	// if the marker is a transfer tombstone — displaced. Without a marker
	// it's purely untracked. (The orphaned/displaced rows are ignored by
	// list/doctor/pending-review, which only render remote-known or no-marker
	// rows; status and push consume them.)
	for name, path := range local {
		if consumedLocal[name] {
			continue
		}
		row := SkillStateInfo{Name: name, Local: true, LocalHash: hashLocal(path)}
		if entry, ok := state.Skills[name]; ok {
			row.Marker = entry
			if entry != nil && (entry.Deleted || entry.MovedTo != "") {
				row.State = StateDisplaced
			} else {
				row.State = StateOrphaned
			}
		} else {
			row.State = StateUntracked
		}
		results = append(results, row)
	}

	return results
}

// decideState applies the presence/lifecycle taxonomy (the first axis) to
// one populated SkillStateInfo. Caller fills Local / LocalHash / Remote /
// Marker first. The HOW-it-diverges second axis is computed by
// deriveDivergence, not here.
func decideState(info SkillStateInfo) SkillState {
	if !info.Local {
		return StateAvailable
	}

	if info.Marker == nil {
		// Untracked branch: differentiate adoptable vs conflict vs plain
		// untracked based on whether the server has a candidate skill of the
		// same name and whether its bytes match.
		if info.Remote == nil {
			return StateUntracked
		}
		remoteHash := strDeref(info.Remote.ContentHash)
		if remoteHash != "" && info.LocalHash == remoteHash {
			return StateAdoptable
		}
		return StateConflict
	}

	// Marker + local present, paired with a remote (callers only reach here
	// for skill_id/dir-name matched remotes): tracked. The divergence flags
	// carry the rest.
	return StateTracked
}

// deriveDivergence fills the four divergence coordinates for a tracked
// skill. Pure. No-op (leaves all flags false) for any state other than
// StateTracked, so projections can read the flags unconditionally.
func deriveDivergence(info *SkillStateInfo) {
	if info.State != StateTracked || info.Marker == nil {
		return
	}
	base := info.Marker.ContentHash
	info.Sourced = info.Marker.Source != nil

	if base != "" && info.LocalHash != "" && info.LocalHash != base {
		info.LocalDirty = true
	}

	remoteHead := ""
	if info.Remote != nil {
		remoteHead = strDeref(info.Remote.ContentHash)
	}

	// Overlay rows: the matched remote IS the upstream, so "remote head ≠
	// my base" is not another-of-my-machines-pushed — it's the overlay's
	// standing divergence (OverlayDiverged, rendered as "local changes"),
	// and a genuine upstream move is measured against the BASELINE the
	// user last incorporated. The remote-id comparison covers the legacy
	// pre-heal marker shape (SkillID = the backup fork) that classifySkills
	// remapped onto the upstream row.
	matchedUpstream := info.Remote != nil && info.Marker.Source != nil &&
		info.Remote.Id.String() == sourceUpstreamID(info.Marker.Source)
	if isOverlayMarker(info.Marker) || matchedUpstream {
		info.Overlay = true
		if remoteHead != "" && info.LocalHash != "" && info.LocalHash != remoteHead {
			info.OverlayDiverged = true
		}
		baseline := markerUpstreamBase(info.Marker)
		if remoteHead != "" && baseline != "" && remoteHead != baseline {
			info.UpstreamMoved = true
		}
		return
	}

	if base != "" && remoteHead != "" && remoteHead != base {
		info.RemoteMoved = true
	}

	info.UpstreamMoved = skillUpstreamMoved(info.Marker, info.Remote)
}

// copyRemote returns a heap-allocated copy of an apiSkill so SkillStateInfo
// holds a stable pointer rather than referencing the caller's slice element.
func copyRemote(r apiSkill) *apiSkill {
	c := r
	return &c
}
