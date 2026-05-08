package cmd

import "testing"

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
