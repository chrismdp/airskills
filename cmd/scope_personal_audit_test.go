package cmd

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestPersonalScopeCallersAreOwnershipQueries(t *testing.T) {
	allowed := map[string]bool{
		"publish.go":  true,
		"transfer.go": true,
		// status.go uses listSkills("personal") to detect skills owned
		// server-side but not in the active skillset (the "in other
		// skillset" warning). That's a legitimate ownership query.
		"status.go": true,
		// push.go uses listSkills("personal") to adopt the caller's own
		// hidden backup fork on a slug collision (another device created
		// it). Backup rows are shadowed out of the effective listing, so
		// the ownership query is the only way to find them.
		"push.go": true,
		// doctor.go validates the overlay invariants against the caller's
		// own backup rows — same shadowed-rows rationale as push.go.
		"doctor.go": true,
	}
	callRe := regexp.MustCompile(`listSkills\("personal"\)`)
	defaultRe := regexp.MustCompile(`scope\s*=\s*"personal"`)

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("ReadDir cmd: %v", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		body, err := os.ReadFile(filepath.Join(".", entry.Name()))
		if err != nil {
			t.Fatalf("ReadFile %s: %v", entry.Name(), err)
		}
		for _, loc := range callRe.FindAllIndex(body, -1) {
			prefixStart := loc[0] - 220
			if prefixStart < 0 {
				prefixStart = 0
			}
			prefix := string(body[prefixStart:loc[0]])
			if !allowed[entry.Name()] || !strings.Contains(strings.ToLower(prefix), "ownership query") {
				t.Errorf("%s uses scope=personal without an ownership-query comment", entry.Name())
			}
		}
		if defaultRe.Match(body) {
			t.Errorf("%s defaults an unspecified skills listing to scope=personal", entry.Name())
		}
	}
}
