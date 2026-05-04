package cmd

// SkillState is the cross-state of a single skill on this machine, as
// reported by classifySkills. It is the single vocabulary that
// `airskills sync`, `airskills doctor`, and `airskills list` all share.
//
// User-facing description: doc/internals/sync-state.mdx in the platform
// repo.
type SkillState string

const (
	// StateSynced — local matches remote, marker tracks.
	StateSynced SkillState = "synced"

	// StateModified — local has diverged from the marker. For owned
	// skills this means unpublished changes; for sourced skills it
	// means a customised copy that has been resolved against the
	// current upstream.
	StateModified SkillState = "modified"

	// StateModifiedPending — sourced skill, customised, AND the
	// upstream content hash has moved past the marker's ResolvedHash.
	// Surfaces on every sync until the user resolves or pulls --force.
	StateModifiedPending SkillState = "modified-pending"

	// StateUntracked — local skill directory exists, no marker, and
	// the server has no skill that could account for it. Common when
	// a skill arrived via git rather than `airskills add`.
	StateUntracked SkillState = "untracked"

	// StateLinked — transient pull-time classification. Local dir
	// exists, no marker, but the server has a skill of the same name
	// whose bytes match exactly. Next sync silently claims it.
	StateLinked SkillState = "linked"

	// StateUntrackedConflict — transient pull-time classification.
	// Local dir exists, no marker, server has a same-named skill
	// whose bytes differ. Next sync surfaces this via the existing
	// conflict UX.
	StateUntrackedConflict SkillState = "untracked-conflict"

	// StateNotLocal — server has a skill the user has not installed
	// on this machine. Rendered as "—" in `airskills list`.
	StateNotLocal SkillState = "not-local"
)

// SkillStateInfo is one row of classifier output. The classifier emits
// one row per server-known skill plus one row per local directory that
// has no marker and no matching server skill.
type SkillStateInfo struct {
	// Name is the local directory name when the skill exists locally,
	// otherwise the server slug. Stable across server-side renames
	// because tracking matches by skill_id first.
	Name string

	State SkillState

	// Local is true when the skill has a directory on this machine.
	Local bool

	// Remote is the server-side view, if the server knows about this
	// skill. Nil for purely-local untracked directories.
	Remote *apiSkill

	// Marker is the sync.json entry for this skill, if one exists.
	Marker *SyncEntry

	// LocalHash is the Merkle hash of on-disk content. Empty when the
	// skill is not present locally.
	LocalHash string
}

// classifySkills returns the cross-state of every skill the CLI knows
// about: every server-known skill, plus any local directories that
// aren't accounted for by the server. It is pure — no I/O — and takes
// a hashLocal callback so callers can supply readSkillFiles +
// computeMerkleHash without this package taking an indirect dependency
// on disk state in tests.
//
// Matching priority for tracked skills: skill_id first (so server-side
// renames are followed), then local directory name. Each output row
// represents exactly one skill; we deduplicate by the local dir name
// when the skill is present locally and by server slug otherwise.
func classifySkills(
	remote []apiSkill,
	local map[string]string,
	state *SyncState,
	hashLocal func(path string) string,
) []SkillStateInfo {
	if state == nil {
		state = &SyncState{Version: 1, Skills: map[string]*SyncEntry{}}
	}

	skillIDToName := map[string]string{}
	for name, entry := range state.Skills {
		if entry != nil && entry.SkillID != "" {
			skillIDToName[entry.SkillID] = name
		}
	}

	results := []SkillStateInfo{}
	consumedLocal := map[string]bool{}

	for i := range remote {
		r := remote[i]

		// Tracked match by skill_id wins. Falls back to dir-name match
		// for legacy markers that might not have a skill_id (defensive).
		trackedName := skillIDToName[r.ID]
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
			// look for an untracked local with the same name as the
			// remote. This is the untracked / linked / untracked-conflict
			// branch.
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
		results = append(results, info)
	}

	// Any local directory that didn't pair up with a remote is purely
	// untracked. Skip ones that paired with a tracked marker but had
	// no remote match — that's an orphan handled elsewhere.
	for name, path := range local {
		if consumedLocal[name] {
			continue
		}
		// If there's a marker for this name, the server has presumably
		// dropped the skill (orphan / archived). Out of scope here.
		if _, ok := state.Skills[name]; ok {
			continue
		}
		results = append(results, SkillStateInfo{
			Name:      name,
			State:     StateUntracked,
			Local:     true,
			LocalHash: hashLocal(path),
		})
	}

	return results
}

// decideState applies the state taxonomy to one populated SkillStateInfo.
// Caller fills Local / LocalHash / Remote / Marker first.
func decideState(info SkillStateInfo) SkillState {
	if !info.Local {
		return StateNotLocal
	}

	if info.Marker == nil {
		// Untracked branch: differentiate linked vs untracked-conflict
		// vs plain untracked based on whether the server has a
		// candidate skill of the same name.
		if info.Remote == nil {
			return StateUntracked
		}
		if info.Remote.ContentHash != "" && info.LocalHash == info.Remote.ContentHash {
			return StateLinked
		}
		return StateUntrackedConflict
	}

	// Tracked branch.
	markerHash := info.Marker.ContentHash
	if markerHash != "" && info.LocalHash != "" && info.LocalHash != markerHash {
		// Local has diverged from the marker.
		if info.Marker.Source != nil {
			// Sourced skill — modified vs modified-pending depends on
			// whether the upstream has moved past ResolvedHash.
			upstream := ""
			if info.Remote != nil {
				upstream = info.Remote.ContentHash
			}
			if info.Marker.ResolvedHash != "" && upstream != "" && upstream == info.Marker.ResolvedHash {
				return StateModified
			}
			return StateModifiedPending
		}
		return StateModified
	}

	return StateSynced
}

// copyRemote returns a heap-allocated copy of an apiSkill so SkillStateInfo
// holds a stable pointer rather than referencing the caller's slice element.
func copyRemote(r apiSkill) *apiSkill {
	c := r
	return &c
}
