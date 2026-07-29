package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// dirNameForOwner computes the local directory name a skill should have
// after its owner namespace changes. Strips the old owner prefix if present
// (so "chrismdp-deploy-check" with newSlug "cherrypick" → "cherrypick-deploy-check"),
// otherwise treats the whole name as the base slug.
//
// If newSlug is empty, returns the bare base slug (used when transferring
// out of an org back to bare-personal — though personal skills have an
// owner slug too, this branch is defensive).
func dirNameForOwner(currentName, oldSlug, newSlug string) string {
	base := currentName
	if oldSlug != "" && strings.HasPrefix(currentName, oldSlug+"-") {
		base = strings.TrimPrefix(currentName, oldSlug+"-")
	}
	if newSlug == "" {
		return base
	}
	return newSlug + "-" + base
}

// removeSkillDirAcrossAgents removes a skill directory (identified by its
// base name) from every agent's global skills directory where it exists.
// fullPath is the canonical path on this machine; basename is derived from it.
// Errors from individual agents are ignored (best-effort cleanup).
func removeSkillDirAcrossAgents(fullPath string) error {
	basename := filepath.Base(fullPath)
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	for _, a := range agents {
		globalPath := resolveGlobalDir(home, a.GlobalDir)
		dir := filepath.Join(globalPath, basename)
		if _, err := os.Stat(dir); err == nil {
			_ = os.RemoveAll(dir)
		}
	}
	return nil
}

// renameSkillDirAcrossAgents renames `oldName` → `newName` in every agent's
// global skills directory where `oldName` exists. Bails out (returning an
// error) if `newName` already exists in any agent dir, to avoid clobber.
func renameSkillDirAcrossAgents(oldName, newName string) error {
	if oldName == newName {
		return nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	type op struct{ oldDir, newDir string }
	var ops []op
	for _, a := range agents {
		globalPath := resolveGlobalDir(home, a.GlobalDir)
		oldDir := filepath.Join(globalPath, oldName)
		newDir := filepath.Join(globalPath, newName)
		if _, err := os.Stat(oldDir); err != nil {
			continue
		}
		if _, err := os.Stat(newDir); err == nil {
			return fmt.Errorf("target dir already exists: %s", newDir)
		}
		ops = append(ops, op{oldDir, newDir})
	}
	for _, o := range ops {
		if err := os.Rename(o.oldDir, o.newDir); err != nil {
			return err
		}
	}
	return nil
}

// markerStateKind classifies what happened to a marker's skill on the server.
type markerStateKind int

const (
	markerStateOK     markerStateKind = iota // still writable by you
	markerStateMoved                         // exists, but you can't write (transferred away or role downgraded)
	markerStateOrphan                        // 404 — gone
	markerStateError                         // some other error; treat as transient
)

// markerState describes the server-truth view of a marker after re-resolution.
type markerState struct {
	kind      markerStateKind
	ownerKind string // "user" | "org"
	ownerSlug string
	skillSlug string
}

// updateLocalMarkerForTransfer records the new owner namespace AND the new
// skill_id in the local marker after the CLI itself executed a transfer.
//
// Under the v2 transfer model the skill_id changes — server soft-deletes the
// original and creates a new skill row at the target owner with a fresh
// skill_id. The local copy is the same bytes as before; we just need to
// repoint the marker so subsequent push/pull find the new server-side row.
//
// The local dir is NOT renamed: the agentskills.io spec requires SKILL.md
// `name` to equal the parent dir name, so renaming the dir would also
// require rewriting `name` (a content change). Ownership and skill_id
// are tracked in the marker; the dir stays stable.
func updateLocalMarkerForTransfer(localName, oldSkillID, newSkillID, newKind, newOwnerSlug, newSkillSlug, newVersion, newContentHash string) error {
	state := loadSyncState()
	movedTo := newOwnerSlug
	if newSkillSlug != "" {
		movedTo += "/" + newSkillSlug
	}

	for name, e := range state.Skills {
		if e == nil {
			continue
		}
		matchesOldID := oldSkillID != "" && e.SkillID == oldSkillID
		matchesTransferTombstone := name == localName && e.Deleted && e.MovedTo == movedTo
		if matchesOldID || matchesTransferTombstone {
			repointMarkerToTransferredSkill(e, newSkillID, newKind, newOwnerSlug, newVersion, newContentHash)
			state.Skills[name] = e
			return saveSyncState(state)
		}
	}

	// No marker matched, but the skill may still be on disk (its marker was
	// never written, or an earlier sync cleared it). If the dir is present,
	// link it to the transferred copy exactly as a pull would, so the
	// transferring machine keeps tracking the skill at its new owner. Without
	// this the local dir is left orphaned and the next sync sees the old
	// skill_id archived and tombstones it ("upstream archived").
	localSkills, err := scanSkillsFromAgents()
	if err != nil {
		return err
	}
	if _, ok := localSkills[localName]; !ok {
		// Skill isn't present locally — nothing to link.
		return nil
	}
	state.Skills[localName] = &SyncEntry{
		SkillID:     newSkillID,
		Version:     newVersion,
		ContentHash: newContentHash,
		Tool:        "claude-code",
		OwnerKind:   newKind,
		OwnerSlug:   newOwnerSlug,
	}
	return saveSyncState(state)
}

// clearTransferTombstone un-tombstones a marker that was wrongly (or stalely)
// flagged as transferred away. Used when a tracked skill_id reappears in the
// caller's listing — e.g. an org skill re-added to a skillset — so the local
// copy is tracked normally again. SkillID/Source are left intact; only the
// tombstone flags, version, and owner namespace are refreshed. Divergence (if
// any) is handled by the normal pull rules, not here.
func clearTransferTombstone(e *SyncEntry, newVersion, newOwnerKind, newOwnerSlug string) {
	if e == nil {
		return
	}
	e.Deleted = false
	e.MovedTo = ""
	if newVersion != "" {
		e.Version = newVersion
	}
	if newOwnerKind != "" {
		e.OwnerKind = newOwnerKind
	}
	if newOwnerSlug != "" {
		e.OwnerSlug = newOwnerSlug
	}
}

func repointMarkerToTransferredSkill(e *SyncEntry, newSkillID, newKind, newOwnerSlug, newVersion, newContentHash string) {
	e.SkillID = newSkillID
	e.OwnerKind = newKind
	e.OwnerSlug = newOwnerSlug
	if newVersion != "" {
		e.Version = newVersion
	}
	if newContentHash != "" {
		e.ContentHash = newContentHash
	}
	e.Deleted = false
	e.MovedTo = ""
}

// classifyMarkerSkill calls the server to learn what state a marker's skill
// is currently in. Used after a 403/404 from push to decide what to tell the
// user. Server is the single source of truth — we never infer from the local
// marker.
func classifyMarkerSkill(c *apiClient, marker *SyncEntry) (markerState, error) {
	if marker == nil || marker.SkillID == "" {
		return markerState{kind: markerStateError}, fmt.Errorf("no skill id")
	}
	body, err := c.get(fmt.Sprintf("/api/v1/skills/%s", marker.SkillID))
	if err != nil {
		if status, ok := httpErrorStatus(err); ok && status == 404 {
			return markerState{kind: markerStateOrphan}, nil
		}
		// Keep recognizing older/fake error values that predate typed HTTP
		// response errors.
		msg := err.Error()
		if strings.Contains(msg, "(404)") || strings.Contains(msg, "not found") {
			return markerState{kind: markerStateOrphan}, nil
		}
		return markerState{kind: markerStateError}, err
	}
	// Parse just enough to learn the current owner namespace.
	var resp struct {
		Slug  string `json:"slug"`
		Owner *struct {
			Username string `json:"username"`
		} `json:"owner"`
		Org *struct {
			Slug string `json:"slug"`
		} `json:"org"`
		CurrentOwner *struct {
			Kind string `json:"kind"`
			Slug string `json:"slug"`
		} `json:"current_owner"`
	}
	if err := parseJSON(body, &resp); err != nil {
		return markerState{kind: markerStateError}, err
	}
	state := markerState{kind: markerStateMoved, skillSlug: resp.Slug}
	if resp.Org != nil {
		state.ownerKind = "org"
		state.ownerSlug = resp.Org.Slug
	} else if resp.Owner != nil {
		state.ownerKind = "user"
		state.ownerSlug = resp.Owner.Username
	} else if resp.CurrentOwner != nil {
		state.ownerKind = resp.CurrentOwner.Kind
		state.ownerSlug = resp.CurrentOwner.Slug
	}
	if state.skillSlug == "" || state.ownerSlug == "" {
		return markerState{kind: markerStateError}, fmt.Errorf("skill detail response missing moved destination")
	}
	// We're here because push returned 403/404; if GET succeeds, the user can
	// READ but not WRITE. Always classify as moved (i.e. stale marker).
	return state, nil
}
