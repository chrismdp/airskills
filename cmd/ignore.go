package cmd

import (
	"path/filepath"
	"strings"
)

// Universal noise — never legitimately part of a skill. Matched as directory
// component anywhere in the relative path (so __pycache__ is skipped at any
// depth, etc.).
var ignoredDirNames = map[string]bool{
	"__pycache__":   true,
	".git":          true,
	".svn":          true,
	".hg":           true,
	"node_modules":  true,
	".venv":         true,
	"venv":          true,
	".pytest_cache": true,
	".mypy_cache":   true,
	".ruff_cache":   true,
	".tox":          true,
	".idea":         true,
	".vscode":       true,
}

var ignoredFileNames = map[string]bool{
	".DS_Store": true,
	"Thumbs.db": true,
}

var ignoredFileSuffixes = []string{
	".pyc",
	".pyo",
	".swp",
	".swo",
}

// shouldIgnoreFile reports whether a path (relative to the skill root) is
// universal dev noise that should never be part of a skill upload — Python
// bytecode caches, VCS metadata, IDE state, OS detritus.
//
// Per-skill overrides via .airskillsignore are not implemented yet; only the
// built-in list applies. See cmd/push.go for usage.
func shouldIgnoreFile(rel string) bool {
	rel = filepath.ToSlash(rel)
	for _, part := range strings.Split(rel, "/") {
		if ignoredDirNames[part] {
			return true
		}
	}
	base := filepath.Base(rel)
	if ignoredFileNames[base] {
		return true
	}
	for _, suf := range ignoredFileSuffixes {
		if strings.HasSuffix(base, suf) {
			return true
		}
	}
	return false
}
