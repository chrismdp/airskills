package cmd

import (
	"os"
	"path/filepath"
	"testing"
)

func TestShouldIgnoreFile(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		// Kept
		{"SKILL.md", false},
		{"scripts/run.py", false},
		{"references/notes.md", false},
		{"data/sample.json", false},

		// Python bytecode caches at any depth
		{"scripts/__pycache__/run.cpython-312.pyc", true},
		{"scripts/__pycache__", true},
		{"deeply/nested/__pycache__/x.pyc", true},
		{"scripts/run.pyc", true},
		{"scripts/run.pyo", true},

		// VCS / IDE / OS noise
		{".git/HEAD", true},
		{".vscode/settings.json", true},
		{".idea/workspace.xml", true},
		{".DS_Store", true},
		{"subdir/.DS_Store", true},
		{"Thumbs.db", true},

		// Vendored deps
		{"node_modules/foo/index.js", true},
		{".venv/bin/python", true},
		{"venv/lib/site-packages/x.py", true},

		// Test caches
		{".pytest_cache/v/cache/lastfailed", true},
		{".mypy_cache/3.12/builtins.data.json", true},
		{".ruff_cache/0.1.0/x", true},

		// Editor swap files
		{"SKILL.md.swp", true},
		{"scripts/run.py.swo", true},
	}
	for _, tc := range cases {
		got := shouldIgnoreFile(tc.path)
		if got != tc.want {
			t.Errorf("shouldIgnoreFile(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

// With no .askignore or .gitignore, only built-in noise is excluded.
func TestIgnoreMatcher_NoFile(t *testing.T) {
	dir := t.TempDir()
	m := newIgnoreMatcher(dir)
	if len(m.levels) != 0 {
		t.Errorf("expected no levels, got %d", len(m.levels))
	}
	if ignored, _ := m.Decide("SKILL.md", false); ignored {
		t.Error("SKILL.md should not be ignored without an ignore file")
	}
	if ignored, _ := m.Decide(".git/HEAD", false); !ignored {
		t.Error("built-in noise should still be ignored")
	}
	if ignored, _ := m.Decide(".", true); ignored {
		t.Error(". should not be ignored")
	}
}

// .askignore patterns match gitignore-style — directories (trailing /),
// nested paths, negation (!), comments.
func TestIgnoreMatcher_AskIgnore(t *testing.T) {
	dir := t.TempDir()
	contents := []byte("# personal cron wrapper\nscripts/run.sh\nstate/\n*.log\n!keep.log\n")
	if err := os.WriteFile(filepath.Join(dir, ".askignore"), contents, 0644); err != nil {
		t.Fatal(err)
	}

	m := newIgnoreMatcher(dir)
	if len(m.levels) != 1 || m.levels[0].src != ".askignore" {
		t.Fatalf("expected one .askignore level, got %+v", m.levels)
	}

	cases := []struct {
		rel   string
		isDir bool
		want  bool
	}{
		{"SKILL.md", false, false},
		{"scripts/run.sh", false, true},
		{"scripts/other.sh", false, false},
		{"state/snapshot.json", false, true},
		{"state", true, true}, // directory entry needs isDir=true to match `state/`
		{"debug.log", false, true},
		{"nested/debug.log", false, true},
		{"keep.log", false, false},
		{".askignore", false, false}, // the ignore file IS part of the skill setup — uploaded
	}
	for _, tc := range cases {
		got, reason := m.Decide(tc.rel, tc.isDir)
		if got != tc.want {
			t.Errorf("Decide(%q, isDir=%v) = %v (%s), want %v", tc.rel, tc.isDir, got, reason, tc.want)
		}
	}
}

// .gitignore is honoured even on its own.
func TestIgnoreMatcher_GitIgnoreOnly(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("secrets/\n"), 0644); err != nil {
		t.Fatal(err)
	}

	m := newIgnoreMatcher(dir)
	if len(m.levels) != 1 || m.levels[0].src != ".gitignore" {
		t.Fatalf("expected one .gitignore level, got %+v", m.levels)
	}
	if ig, _ := m.Decide("secrets/api-key", false); !ig {
		t.Error("secrets/api-key should be ignored via .gitignore")
	}
	if ig, _ := m.Decide(".gitignore", false); ig {
		t.Error(".gitignore should be uploaded as part of the skill setup")
	}
}

// Both files at the same level MERGE — patterns from both apply, and
// .askignore (loaded last) can re-include via `!` what .gitignore excluded.
// The two formats are interchangeable; both being present is fine.
func TestIgnoreMatcher_MergeRootLevel(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("secrets/\n*.log\n"), 0644); err != nil {
		t.Fatal(err)
	}
	// askignore both adds its own rules AND re-includes a file gitignore excluded.
	if err := os.WriteFile(filepath.Join(dir, ".askignore"), []byte("state/\n!important.log\n"), 0644); err != nil {
		t.Fatal(err)
	}

	m := newIgnoreMatcher(dir)
	if len(m.levels) != 2 {
		t.Fatalf("expected two levels (gitignore + askignore), got %d", len(m.levels))
	}

	cases := []struct {
		rel   string
		isDir bool
		want  bool
		why   string
	}{
		{"secrets/x", false, true, "gitignore rule applies"},
		{"state/x", false, true, "askignore rule applies"},
		{"debug.log", false, true, "gitignore *.log"},
		{"important.log", false, false, "askignore !important.log re-includes"},
		{".gitignore", false, false, "ignore-file is part of the skill — uploaded"},
		{".askignore", false, false, "ignore-file is part of the skill — uploaded"},
	}
	for _, tc := range cases {
		got, reason := m.Decide(tc.rel, tc.isDir)
		if got != tc.want {
			t.Errorf("%s: Decide(%q) = %v (%s), want %v", tc.why, tc.rel, got, reason, tc.want)
		}
	}
}

// Nested .gitignore files layer on top of the skill-root one — patterns
// in scripts/.gitignore apply only under scripts/, and a !negation can
// re-include something the root would have ignored.
func TestIgnoreMatcher_NestedGitignore(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("*.tmp\n"), 0644)
	os.MkdirAll(filepath.Join(dir, "scripts"), 0755)
	os.WriteFile(filepath.Join(dir, "scripts", ".gitignore"), []byte("private.sh\n!important.tmp\n"), 0644)

	m := newIgnoreMatcher(dir)
	if len(m.levels) != 2 {
		t.Fatalf("expected root + nested level, got %d", len(m.levels))
	}

	cases := []struct {
		rel  string
		want bool
		why  string
	}{
		{"foo.tmp", true, "root *.tmp"},
		{"scripts/cleanup.tmp", true, "root *.tmp applies under scripts too"},
		{"scripts/important.tmp", false, "nested !important.tmp re-includes"},
		{"scripts/private.sh", true, "nested private.sh"},
		{"other/private.sh", false, "nested rule does NOT apply outside scripts/"},
	}
	for _, tc := range cases {
		got, reason := m.Decide(tc.rel, false)
		if got != tc.want {
			t.Errorf("%s: Decide(%q) = %v (%s), want %v", tc.why, tc.rel, got, reason, tc.want)
		}
	}
}

// Built-in noise (node_modules, __pycache__, etc.) is non-overridable — a
// `!node_modules` in .askignore cannot bring it back. This keeps beginner
// skills clean even if they accidentally try to push everything.
func TestIgnoreMatcher_BuiltinUnoverridable(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, ".askignore"), []byte("!node_modules/\n!__pycache__/\n"), 0644)

	m := newIgnoreMatcher(dir)
	cases := []string{
		"node_modules/foo/index.js",
		"scripts/__pycache__/x.pyc",
		".DS_Store",
	}
	for _, rel := range cases {
		got, reason := m.Decide(rel, false)
		if !got {
			t.Errorf("built-in noise %q must remain ignored (got %v %q)", rel, got, reason)
		}
	}
}

// SKILL.md is unignorable. CheckSkillFile must return an error if a rule
// matches it so push fails loudly rather than silently shipping a broken skill.
func TestIgnoreMatcher_SkillFileProtected(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, ".askignore"), []byte("*.md\n"), 0644)

	m := newIgnoreMatcher(dir)
	if err := m.CheckSkillFile(); err == nil {
		t.Fatal("expected error — *.md should have matched SKILL.md")
	} else if _, ok := err.(*skillFileIgnoredError); !ok {
		t.Errorf("expected skillFileIgnoredError, got %T: %v", err, err)
	}
}

// A user with no rule for SKILL.md passes the check. The built-in swap-file
// suffix `.swp` matches `SKILL.md.swp`, not `SKILL.md` itself — the guard
// should not false-positive on it.
func TestIgnoreMatcher_SkillFileFine(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, ".askignore"), []byte("state/\n*.swp\n"), 0644)
	m := newIgnoreMatcher(dir)
	if err := m.CheckSkillFile(); err != nil {
		t.Errorf("unexpected SKILL.md error: %v", err)
	}
	// And the swap-file pattern still matches the actual swap file.
	if ig, _ := m.Decide("SKILL.md.swp", false); !ig {
		t.Error("SKILL.md.swp should be ignored")
	}
}

// Leading slash anchors a pattern to the directory holding the ignore file —
// `/state` matches `state` at the skill root but not `scripts/state` further
// down. A bare `state` (no slash) matches anywhere.
func TestIgnoreMatcher_LeadingSlashAnchors(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, ".askignore"), []byte("/state\n"), 0644)

	m := newIgnoreMatcher(dir)
	if ig, _ := m.Decide("state", true); !ig {
		t.Error("/state should match root-level state dir")
	}
	if ig, _ := m.Decide("scripts/state", true); ig {
		t.Error("/state should NOT match nested scripts/state")
	}
}

// `**` matches arbitrary path depth. Both `**/x.sh` (anywhere) and
// `logs/**` (anything under logs) work.
func TestIgnoreMatcher_DoubleStar(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, ".askignore"), []byte("**/secret.key\nlogs/**\n"), 0644)
	m := newIgnoreMatcher(dir)
	cases := []struct {
		rel  string
		want bool
	}{
		{"secret.key", true},
		{"scripts/secret.key", true},
		{"a/b/c/secret.key", true},
		{"logs/today.log", true},
		{"logs/by-date/2026-05-21.log", true},
		{"unrelated.txt", false},
	}
	for _, tc := range cases {
		if ig, reason := m.Decide(tc.rel, false); ig != tc.want {
			t.Errorf("Decide(%q) = %v (%s), want %v", tc.rel, ig, reason, tc.want)
		}
	}
}

// CRLF line endings (Windows) shouldn't break pattern parsing — go-gitignore
// strips \r itself, and our negation extractor uses TrimSpace which also
// handles it.
func TestIgnoreMatcher_CRLF(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, ".askignore"), []byte("state/\r\n*.log\r\n!keep.log\r\n"), 0644)
	m := newIgnoreMatcher(dir)
	if ig, _ := m.Decide("state/x", false); !ig {
		t.Error("state/x should be ignored under CRLF .askignore")
	}
	if ig, _ := m.Decide("debug.log", false); !ig {
		t.Error("debug.log should be ignored under CRLF .askignore")
	}
	if ig, _ := m.Decide("keep.log", false); ig {
		t.Error("keep.log should be re-included by within-file negation under CRLF")
	}
}

// An empty / comments-only file is a valid no-op — the level is registered
// but no rules apply. The file itself is still uploaded (it's part of the
// skill setup), in case the user adds rules later.
func TestIgnoreMatcher_EmptyOrCommentsOnly(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, ".askignore"), []byte("# just a comment\n\n   \n"), 0644)
	m := newIgnoreMatcher(dir)
	if ig, _ := m.Decide("anything.txt", false); ig {
		t.Error("nothing should be ignored under comment-only .askignore")
	}
	if ig, _ := m.Decide(".askignore", false); ig {
		t.Error(".askignore should be uploaded as part of the skill setup")
	}
}

// `*` should not match path components like `.git` — built-in noise must
// stay non-overridable, even if the user wrote `!` re-includes.
func TestIgnoreMatcher_BuiltinBeatsBareStar(t *testing.T) {
	dir := t.TempDir()
	// A rule that would otherwise sweep everything in, but built-in still wins.
	os.WriteFile(filepath.Join(dir, ".askignore"), []byte("!.git\n!node_modules\n"), 0644)
	m := newIgnoreMatcher(dir)
	if ig, _ := m.Decide(".git/HEAD", false); !ig {
		t.Error(".git noise must remain ignored")
	}
	if ig, _ := m.Decide("node_modules/foo", false); !ig {
		t.Error("node_modules noise must remain ignored")
	}
}

// Three-level merging: skill-root .gitignore + skill-root .askignore +
// nested scripts/.gitignore. All apply; same-dir merges with askignore last,
// nested layers add on top of root.
func TestIgnoreMatcher_ThreeLevelMerge(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("*.log\n"), 0644)
	os.WriteFile(filepath.Join(dir, ".askignore"), []byte("private/\n"), 0644)
	os.MkdirAll(filepath.Join(dir, "scripts"), 0755)
	os.WriteFile(filepath.Join(dir, "scripts", ".gitignore"), []byte("!keep.log\n"), 0644)

	m := newIgnoreMatcher(dir)
	if len(m.levels) != 3 {
		t.Fatalf("expected 3 levels, got %d", len(m.levels))
	}

	cases := []struct {
		rel  string
		want bool
		why  string
	}{
		{"scripts/keep.log", false, "nested !keep.log re-includes"},
		{"scripts/debug.log", true, "root *.log applies under scripts/"},
		{"keep.log", true, "nested scope doesn't reach root"},
		{"private/secret", true, "askignore private/"},
	}
	for _, tc := range cases {
		got, reason := m.Decide(tc.rel, false)
		if got != tc.want {
			t.Errorf("%s: Decide(%q) = %v (%s), want %v", tc.why, tc.rel, got, reason, tc.want)
		}
	}
}

// .askignore inside a built-in noise dir (e.g. node_modules/.askignore) must
// NOT be loaded — the pre-walk should stop at noise dirs.
func TestIgnoreMatcher_IgnoresInNoiseDirs(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "node_modules"), 0755)
	os.WriteFile(filepath.Join(dir, "node_modules", ".askignore"), []byte("# malicious\n!node_modules\n"), 0644)

	m := newIgnoreMatcher(dir)
	if len(m.levels) != 0 {
		t.Errorf("expected no levels (noise dirs not descended), got %+v", m.levels)
	}
	if ig, _ := m.Decide("node_modules/x", false); !ig {
		t.Error("node_modules must still be ignored")
	}
}
