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

// renameSkillDirAcrossAgents renames `oldName` → `newName` in every agent's
// global skills directory where `oldName` exists. Bails out (returning an
// error) if `newName` already exists in any agent dir, to avoid clobber.
//
// Mirrors what `migrateToNamespacedDirs` does for legacy → namespaced moves.
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
	kind       markerStateKind
	ownerKind  string // "user" | "org"
	ownerSlug  string
	skillSlug  string
}

// updateLocalMarkerForTransfer records the new owner namespace in the local
// marker after the CLI itself executed a transfer. The local dir is NOT
// renamed: the agentskills.io spec requires SKILL.md `name` to equal the
// parent dir name, so renaming the dir would also require rewriting `name`
// (a content change). Ownership is tracked in the marker; the dir stays
// stable.
func updateLocalMarkerForTransfer(skillID, newKind, newSlug string) error {
	state := loadSyncState()
	for name, e := range state.Skills {
		if e != nil && e.SkillID == skillID {
			e.OwnerKind = newKind
			e.OwnerSlug = newSlug
			state.Skills[name] = e
			return saveSyncState(state)
		}
	}
	// No marker on this machine — nothing to update.
	return nil
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
	}
	// We're here because push returned 403/404; if GET succeeds, the user can
	// READ but not WRITE. Always classify as moved (i.e. stale marker).
	return state, nil
}
