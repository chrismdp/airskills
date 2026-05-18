package cmd

import (
	"fmt"
	"strings"
)

// resolveCommitID expands a user-supplied commit ID (typically the
// 8-char short form printed by `airskills log`) into the full UUID the
// server requires on the archive endpoint.
//
// Background: `airskills log` displays `commit.Id[:8]` for readability,
// then suggests `--commit <id>` in its tip text. The server's archive
// GET (`?commit=<id>`) does an exact UUID match — short IDs return 404
// "commit not found for this skill". This helper bridges the two by
// fetching the skill's version history and prefix-matching client-side.
//
// Ambiguous matches (multiple commits share the prefix) are rejected
// rather than silently picking one, so the caller gets the same answer
// every run.
func resolveCommitID(c *apiClient, skillID, partial string) (string, error) {
	if partial == "" {
		return "", fmt.Errorf("empty commit ID")
	}
	commits, err := c.getVersionHistory(skillID)
	if err != nil {
		return "", fmt.Errorf("fetching version history: %w", err)
	}
	if len(commits) == 0 {
		return "", fmt.Errorf("no versions found for this skill — push something first")
	}

	var matches []string
	for _, c := range commits {
		id := c.Id.String()
		if strings.HasPrefix(id, partial) {
			matches = append(matches, id)
		}
	}

	switch len(matches) {
	case 0:
		return "", fmt.Errorf("commit %q not found — run `airskills log <skill>` to list known commits", partial)
	case 1:
		return matches[0], nil
	default:
		short := make([]string, len(matches))
		for i, m := range matches {
			short[i] = m[:8]
		}
		return "", fmt.Errorf("commit %q is ambiguous — matches %s; pass a longer prefix", partial, strings.Join(short, ", "))
	}
}
