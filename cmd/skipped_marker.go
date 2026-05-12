package cmd

// skippedActionKind labels what to do with a marker whose skill_id the
// server's "owned" listing didn't return. Computed by classifySkippedMarker;
// applied by the push orchestrator.
type skippedActionKind int

const (
	// actionOrphanRemove: server says the skill is gone (404) and local
	// content matches the last-known marker hash. Mirror the delete: rm
	// the dir across agents and drop the marker.
	actionOrphanRemove skippedActionKind = iota

	// actionOrphanKeep: server says gone, but local content has diverged
	// from the marker hash — user has local edits worth preserving. Drop
	// the marker, keep the dir. Next push will publish it as a new skill.
	actionOrphanKeep

	// actionMovedKeep: server still has the skill, but under a different
	// owner (transfer). We don't auto-re-link because we may not be able
	// to write to the new namespace. Drop the marker, keep the dir.
	actionMovedKeep

	// actionTransient: classifier couldn't decide (network error, server
	// 5xx). Leave the marker and the dir alone; retry next sync.
	actionTransient
)

// skippedAction is the typed result of classifying a sync-skipped marker.
// Carries enough payload for the caller to render the user-facing message
// and execute the action without re-fetching anything.
type skippedAction struct {
	kind         skippedActionKind
	name         string
	localDir     string
	newOwnerSlug string // populated for actionMovedKeep
	newSkillSlug string // populated for actionMovedKeep
}

// classifySkippedMarker decides what to do with a marker whose skill_id
// is no longer in the caller's "owned" listing. Calls the server once
// (via classifyMarkerSkill) and uses the local content hash to detect
// whether the user has unsaved edits worth preserving.
//
// Returned action is always safe to apply unconditionally — including
// actionTransient, which is a no-op.
func classifySkippedMarker(client *apiClient, name string, marker *SyncEntry, localDir string) skippedAction {
	state, err := classifyMarkerSkill(client, marker)
	if err != nil || state.kind == markerStateError {
		return skippedAction{kind: actionTransient, name: name, localDir: localDir}
	}

	switch state.kind {
	case markerStateOrphan:
		localFiles := readSkillFiles(localDir)
		localHash := computeMerkleHash(localFiles)
		if marker.ContentHash != "" && localHash == marker.ContentHash {
			return skippedAction{kind: actionOrphanRemove, name: name, localDir: localDir}
		}
		return skippedAction{kind: actionOrphanKeep, name: name, localDir: localDir}

	case markerStateMoved:
		return skippedAction{
			kind:         actionMovedKeep,
			name:         name,
			localDir:     localDir,
			newOwnerSlug: state.ownerSlug,
			newSkillSlug: state.skillSlug,
		}

	default:
		// markerStateOK shouldn't occur — we only call this for skills
		// that listSkills omitted. If it does, treat as transient so
		// we don't accidentally destroy anything.
		return skippedAction{kind: actionTransient, name: name, localDir: localDir}
	}
}
