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
	seen := map[string]bool{}

	addRoot := filepath.Join(os.TempDir(), "airskills-conflicts")
	if entries, err := os.ReadDir(addRoot); err == nil {
		for _, e := range entries {
			if e.IsDir() && hasSkillManifest(filepath.Join(addRoot, e.Name())) {
				seen[e.Name()] = true
			}
		}
	}

	matches, _ := filepath.Glob(filepath.Join(os.TempDir(), "airskills-conflicts-*"))
	for _, root := range matches {
		info, err := os.Stat(root)
		if err != nil || !info.IsDir() || strings.TrimPrefix(filepath.Base(root), "airskills-conflicts-") == "" {
			continue
		}
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() && hasSkillManifest(filepath.Join(root, e.Name())) {
				seen[e.Name()] = true
			}
		}
	}

	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func hasSkillManifest(dir string) bool {
	info, err := os.Stat(filepath.Join(dir, "SKILL.md"))
	return err == nil && !info.IsDir()
}
