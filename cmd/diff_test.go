package cmd

import (
	"strings"
	"testing"
)

// When the mirrored agent copies of a skill disagree with EACH OTHER, a
// per-copy diff against the server can look arbitrary and backwards (field
// report 2026-06-11, bug 3: a stale .codex copy showed the user's local
// additions as deletions). diff must say so up front.
func TestDiffCopiesDisagreeWarning(t *testing.T) {
	hash := stubHasher(map[string]string{
		"/h/.claude/skills/home": "hash-a",
		"/h/.codex/skills/home":  "hash-b",
		"/h/.hermes/skills/home": "hash-a",
	})

	out := diffCopiesDisagreeWarning("home",
		[]string{"/h/.claude/skills/home", "/h/.codex/skills/home", "/h/.hermes/skills/home"}, hash)
	if out == "" {
		t.Fatal("expected a warning when copies disagree")
	}
	if !strings.Contains(out, "airskills sync") {
		t.Errorf("warning should point at sync to reconcile, got %q", out)
	}

	agree := diffCopiesDisagreeWarning("home",
		[]string{"/h/.claude/skills/home", "/h/.hermes/skills/home"},
		stubHasher(map[string]string{
			"/h/.claude/skills/home": "hash-a",
			"/h/.hermes/skills/home": "hash-a",
		}))
	if agree != "" {
		t.Errorf("no warning when all copies match, got %q", agree)
	}

	if single := diffCopiesDisagreeWarning("home", []string{"/h/.claude/skills/home"}, hash); single != "" {
		t.Errorf("no warning for a single copy, got %q", single)
	}
}
