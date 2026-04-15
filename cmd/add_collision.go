package cmd

import (
	"fmt"
	"os"
	"path/filepath"
)

// detectAddCollision reports whether installing into `dirName` would clobber
// an existing skill that is NOT the same skill as `incomingSkillID`.
//
// "Clobber" means: a directory with the same name exists in any agent's
// global skills path AND the local sync state shows a different skill_id
// for it (or shows no skill_id at all — locally-created skills are still
// real work to protect).
//
// Returns the local SKILL.md path (one of them, for reporting) and true
// when there's a real conflict; "" and false otherwise.
func detectAddCollision(dirName, incomingSkillID string, state *SyncState) (string, bool) {
	if state != nil {
		if existing, ok := state.Skills[dirName]; ok && existing != nil {
			if existing.SkillID != "" && existing.SkillID == incomingSkillID {
				// Same skill — re-add is a no-op-ish refresh, not a conflict.
				return "", false
			}
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", false
	}
	for _, a := range agents {
		globalPath := resolveGlobalDir(home, a.GlobalDir)
		dir := filepath.Join(globalPath, dirName)
		mdPath := filepath.Join(dir, "SKILL.md")
		if _, err := os.Stat(mdPath); err == nil {
			return mdPath, true
		}
	}
	return "", false
}

// writeConflictToTmp persists the incoming skill files to
// /tmp/airskills-conflicts/<dirName>/ so the user (or their agent) can
// reconcile against the local copy. Returns the path to the SKILL.md
// inside the temp dir.
func writeConflictToTmp(dirName string, files map[string][]byte) (string, error) {
	tmpDir := filepath.Join(os.TempDir(), "airskills-conflicts", dirName)
	if err := os.MkdirAll(tmpDir, 0755); err != nil {
		return "", err
	}
	for relPath, data := range files {
		full := filepath.Join(tmpDir, relPath)
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			return "", err
		}
		if err := os.WriteFile(full, data, 0644); err != nil {
			return "", fmt.Errorf("writing %s: %w", full, err)
		}
	}
	return filepath.Join(tmpDir, "SKILL.md"), nil
}
