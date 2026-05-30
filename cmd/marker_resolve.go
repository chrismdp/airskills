package cmd

import (
	"fmt"
	"net/url"
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
	// No marker on this machine — nothing to update.
	return nil
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

// repairTransferTombstoneMarkers handles the local-machine aftermath of the
// old transfer bug: the originating machine could keep a Deleted/MovedTo
// tombstone even though its local bytes are exactly the newly-owned skill.
//
// This is intentionally conservative. It only repairs when the marker's
// moved_to destination resolves and the local Merkle hash exactly equals the
// resolved skill's content_hash. Diverged local edits stay tombstoned so push
// continues to refuse them instead of silently binding the wrong content.
func repairTransferTombstoneMarkers(c *apiClient, localSkills map[string]string, state *SyncState) int {
	if c == nil || state == nil || len(state.Skills) == 0 || len(localSkills) == 0 {
		return 0
	}

	repaired := 0
	for name, e := range state.Skills {
		if e == nil || !e.Deleted || e.MovedTo == "" || e.SkillID != "" {
			continue
		}
		localDir, ok := localSkills[name]
		if !ok {
			continue
		}
		owner, slug, ok := strings.Cut(e.MovedTo, "/")
		if !ok || owner == "" || slug == "" {
			continue
		}
		resolved, err := resolveTransferDestination(c, owner, slug)
		if err != nil || resolved.skillID == "" || resolved.contentHash == "" || resolved.ownerKind == "" || resolved.ownerSlug == "" {
			continue
		}
		localHash := computeMerkleHash(readSkillFiles(localDir))
		if localHash != resolved.contentHash {
			continue
		}
		repointMarkerToTransferredSkill(e, resolved.skillID, resolved.ownerKind, resolved.ownerSlug, resolved.version, resolved.contentHash)
		state.Skills[name] = e
		repaired++
	}
	if repaired > 0 {
		_ = saveSyncState(state)
	}
	return repaired
}

type transferDestination struct {
	skillID     string
	version     string
	contentHash string
	ownerKind   string
	ownerSlug   string
}

func resolveTransferDestination(c *apiClient, owner, slug string) (transferDestination, error) {
	body, status, err := c.getWithStatus(fmt.Sprintf(
		"/api/v1/resolve/%s/%s",
		url.PathEscape(owner),
		url.PathEscape(slug),
	))
	if err != nil {
		return transferDestination{}, err
	}
	if status != 200 {
		return transferDestination{}, fmt.Errorf("resolve returned %d", status)
	}
	var resp struct {
		ID           string  `json:"id"`
		Version      string  `json:"version"`
		ContentHash  *string `json:"content_hash"`
		CurrentOwner *struct {
			Kind string `json:"kind"`
			Slug string `json:"slug"`
		} `json:"current_owner"`
	}
	if err := parseJSON(body, &resp); err != nil {
		return transferDestination{}, err
	}
	out := transferDestination{
		skillID:     resp.ID,
		version:     resp.Version,
		contentHash: strDeref(resp.ContentHash),
	}
	if resp.CurrentOwner != nil {
		out.ownerKind = resp.CurrentOwner.Kind
		out.ownerSlug = resp.CurrentOwner.Slug
	}
	return out, nil
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
		// 404 manifests as an API error; the get helper doesn't expose status.
		// Treat any "not found" wording as orphan.
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
