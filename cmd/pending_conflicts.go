package cmd

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// pendingConflictNames finds remote copies left by add/pull conflict paths.
// These dirs are deliberately outside sync.json, so status/doctor need to
// look at the tmp conflict area directly.
func pendingConflictNames() []string {
	byName := pendingConflictDirsByName()

	names := make([]string, 0, len(byName))
	for name := range byName {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func pendingConflictDirs(name string) []string {
	if name == "" {
		return nil
	}
	dirs := pendingConflictDirsByName()[name]
	out := append([]string(nil), dirs...)
	sort.Strings(out)
	return out
}

func pendingConflictDirsByName() map[string][]string {
	out := map[string][]string{}

	addRoot := filepath.Join(os.TempDir(), "airskills-conflicts")
	addPendingConflictDirs(out, addRoot)

	matches, _ := filepath.Glob(filepath.Join(os.TempDir(), "airskills-conflicts-*"))
	for _, root := range matches {
		info, err := os.Stat(root)
		if err != nil || !info.IsDir() || strings.TrimPrefix(filepath.Base(root), "airskills-conflicts-") == "" {
			continue
		}
		addPendingConflictDirs(out, root)
	}

	return out
}

func addPendingConflictDirs(out map[string][]string, root string) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(root, e.Name())
		if hasSkillManifest(dir) {
			out[e.Name()] = append(out[e.Name()], dir)
		}
	}
}

func hasSkillManifest(dir string) bool {
	info, err := os.Stat(filepath.Join(dir, "SKILL.md"))
	return err == nil && !info.IsDir()
}
