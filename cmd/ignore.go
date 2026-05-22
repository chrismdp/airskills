package cmd

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	gitignore "github.com/sabhiram/go-gitignore"
)

// Universal noise — never legitimately part of a skill. Matched as directory
// component anywhere in the relative path (so __pycache__ is skipped at any
// depth, etc.). Belt-and-braces: applied even when the skill has no
// .gitignore / .askignore, and a `!negation` in a per-skill file cannot
// re-include these.
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

// Per-skill ignore-file basenames.
const (
	askIgnoreFile = ".askignore"
	gitIgnoreFile = ".gitignore"
)

// SkillFile is the only file that may never be ignored — without it the
// skill is meaningless. If a rule (even a typo like `*.md`) would exclude
// it, push must hard-fail so the author sees and fixes the rule.
const skillFile = "SKILL.md"

// shouldIgnoreFile reports whether a path (relative to the skill root) is
// universal dev noise. This rule is non-overridable — even an explicit
// `!__pycache__` in a per-skill .askignore cannot bring these back.
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

// ignoreLevel is one .gitignore-style file loaded at a specific directory
// within the skill tree. Multiple levels stack the same way git does:
// outer levels apply broadly, deeper levels can override them (including
// re-including via `!negation`).
//
// We keep two compiled matchers per file. `full` has every pattern (including
// negations) and gives the correct within-file last-wins answer when the
// path is touched by anything in this file. `negations` has just the
// negation patterns (with the leading ! stripped, so they're positives) —
// we use it to detect cross-file re-includes, which the lib's MatchesPathHow
// can't surface from `full` when no positive pattern in the same file fired.
type ignoreLevel struct {
	dir       string               // directory holding the file, relative to skill root ("" = root)
	src       string               // path of the source file relative to skill root (for reporting)
	full      *gitignore.GitIgnore // every pattern — gives within-file semantics
	negations *gitignore.GitIgnore // negations only, with `!` stripped — used to spot cross-file re-includes
	negLines  []string             // raw `!pattern` lines, for reporting which line re-included
}

// ignoreMatcher combines the built-in noise list with all per-skill
// .askignore / .gitignore files found within the skill tree. The two
// formats are interchangeable: both use gitignore-style syntax and both
// are loaded when present. At a single directory level the rules MERGE,
// with .askignore loaded second so its negations (`!pattern`) can
// re-include something a .gitignore in the same directory excluded.
//
// The names exist mainly as user signal: pick whichever convention fits.
// A skill that ships alongside a git repo will naturally reuse its
// .gitignore; a skill that wants airskills-only rules can use .askignore.
type ignoreMatcher struct {
	levels []ignoreLevel
}

// newIgnoreMatcher pre-walks skillDir to collect every .gitignore and
// .askignore at any depth. Same-directory order: .gitignore first, then
// .askignore, so .askignore has the final word at a given level while
// still composing with .gitignore via negation patterns.
func newIgnoreMatcher(skillDir string) *ignoreMatcher {
	m := &ignoreMatcher{}

	filepath.Walk(skillDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil {
			return nil
		}
		// Stop descending into universal noise — don't try to load .gitignore
		// from inside node_modules etc.
		if info.IsDir() && path != skillDir {
			rel, _ := filepath.Rel(skillDir, path)
			if shouldIgnoreFile(rel) {
				return filepath.SkipDir
			}
		}
		if info.IsDir() {
			return nil
		}
		name := info.Name()
		if name != gitIgnoreFile && name != askIgnoreFile {
			return nil
		}
		dir, _ := filepath.Rel(skillDir, filepath.Dir(path))
		if dir == "." {
			dir = ""
		}
		src, _ := filepath.Rel(skillDir, path)
		lvl, lerr := compileIgnoreLevel(path, filepath.ToSlash(dir), filepath.ToSlash(src))
		if lerr != nil || lvl == nil {
			return nil
		}
		m.levels = append(m.levels, *lvl)
		return nil
	})

	// Order: outer dirs first (so nested levels layer on top), then within a
	// directory .gitignore before .askignore (so askignore wins last).
	sort.SliceStable(m.levels, func(i, j int) bool {
		di := strings.Count(m.levels[i].dir, "/")
		dj := strings.Count(m.levels[j].dir, "/")
		if m.levels[i].dir == "" {
			di = -1
		}
		if m.levels[j].dir == "" {
			dj = -1
		}
		if di != dj {
			return di < dj
		}
		if m.levels[i].dir != m.levels[j].dir {
			return m.levels[i].dir < m.levels[j].dir
		}
		// Same dir: .gitignore before .askignore so askignore wins last
		return filepath.Base(m.levels[i].src) == gitIgnoreFile &&
			filepath.Base(m.levels[j].src) == askIgnoreFile
	})

	return m
}

// Decide reports whether the relative path should be ignored, and returns
// a short human-readable reason ("built-in: __pycache__", ".askignore: state/",
// "scripts/.gitignore: !keep.log") suitable for --verbose logging or the
// diff `# ignored` section. `isDir` must be true for directory entries so
// directory-only patterns (`state/`) match correctly.
func (m *ignoreMatcher) Decide(rel string, isDir bool) (bool, string) {
	if rel == "" || rel == "." {
		return false, ""
	}
	rel = filepath.ToSlash(rel)

	// Built-in noise: non-overridable.
	if shouldIgnoreFile(rel) {
		return true, "built-in: " + rel
	}

	// .gitignore and .askignore are PART of the skill — they configure how
	// future pushes from any machine should behave. Upload them unless a
	// user rule explicitly hides them (handled by the level loop below).

	ignored := false
	reason := ""
	for _, lvl := range m.levels {
		// A nested level applies only to files at-or-below its dir.
		if lvl.dir != "" && !pathIsDescendantOf(rel, lvl.dir) {
			continue
		}
		var candidate string
		if lvl.dir == "" {
			candidate = rel
		} else {
			candidate = strings.TrimPrefix(rel, lvl.dir+"/")
		}

		// First check the file's full matcher — this gives correct
		// within-file last-wins behaviour when the file has any opinion.
		matched, pat := matchPath(lvl.full, candidate, isDir)
		if pat != nil {
			ignored = matched
			if matched {
				reason = lvl.src + ": " + pat.Line
			} else {
				reason = lvl.src + ": " + pat.Line + " (re-included)"
			}
			continue
		}

		// `full` had no positive opinion. The lib quirk: a negation in a
		// file with no positives shows up as (false, nil) — so we check
		// the negations-only view to detect cross-file re-includes.
		negMatched, negPat := matchPath(lvl.negations, candidate, isDir)
		if negPat != nil && negMatched && ignored {
			ignored = false
			reason = lvl.src + ": !" + negPat.Line + " (re-included)"
		}
	}
	return ignored, reason
}

// matchPath calls MatchesPathHow with a fallback for directory entries
// — directory-only patterns (`state/`) only match when the candidate has a
// trailing slash, so we try both forms.
func matchPath(gi *gitignore.GitIgnore, candidate string, isDir bool) (bool, *gitignore.IgnorePattern) {
	if gi == nil {
		return false, nil
	}
	matched, pat := gi.MatchesPathHow(candidate)
	if pat == nil && isDir {
		matched, pat = gi.MatchesPathHow(candidate + "/")
	}
	return matched, pat
}

// compileIgnoreLevel loads an ignore file and prepares both the full and
// negations-only matchers. Returns nil on read errors so the caller can
// skip silently.
func compileIgnoreLevel(path, dir, src string) (*ignoreLevel, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	full := gitignore.CompileIgnoreLines(strings.Split(string(data), "\n")...)
	if full == nil {
		return nil, nil
	}
	var negs []string
	for _, line := range strings.Split(string(data), "\n") {
		// Mirror gitignore line handling: strip trailing CR, skip blanks
		// and comments. `\` escapes a leading `!` or `#` — preserve as-is
		// (those won't be negations either way).
		line = strings.TrimRight(line, "\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.HasPrefix(trimmed, "!") {
			negs = append(negs, strings.TrimPrefix(trimmed, "!"))
		}
	}
	negations := gitignore.CompileIgnoreLines(negs...)
	return &ignoreLevel{
		dir:       dir,
		src:       src,
		full:      full,
		negations: negations,
		negLines:  negs,
	}, nil
}

// CanIgnoreSkillFile returns an error if the matcher would exclude
// SKILL.md — the skill is meaningless without it, so push must fail loudly
// rather than silently shipping a broken skill.
func (m *ignoreMatcher) CheckSkillFile() error {
	if ignored, reason := m.Decide(skillFile, false); ignored {
		return &skillFileIgnoredError{Reason: reason}
	}
	return nil
}

type skillFileIgnoredError struct {
	Reason string
}

func (e *skillFileIgnoredError) Error() string {
	return "SKILL.md cannot be ignored (matched by " + e.Reason +
		") — the skill is meaningless without it. Remove or negate the rule."
}

// ignoredEntry pairs a path with a short reason — used by --verbose push
// output and the diff `# ignored` section so users can see why each file
// was excluded.
type ignoredEntry struct {
	Path   string
	Reason string
}

// listIgnoredFiles walks skillDir and returns every entry the matcher
// excludes, sorted by path. Directory matches collapse to a single entry
// (the directory itself); contents underneath are not enumerated.
func listIgnoredFiles(skillDir string) []ignoredEntry {
	m := newIgnoreMatcher(skillDir)
	var out []ignoredEntry
	filepath.Walk(skillDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil {
			return nil
		}
		if info.Name() == ".airskills" {
			return nil
		}
		rel, _ := filepath.Rel(skillDir, path)
		if rel == "." {
			return nil
		}
		ignored, reason := m.Decide(rel, info.IsDir())
		if ignored {
			out = append(out, ignoredEntry{
				Path:   filepath.ToSlash(rel),
				Reason: reason,
			})
			if info.IsDir() {
				return filepath.SkipDir
			}
		}
		return nil
	})
	sort.SliceStable(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// pathIsDescendantOf reports whether `rel` lives under `dir` (where both are
// forward-slash relative paths; dir is "" for the root, but callers should
// short-circuit that case before calling here).
func pathIsDescendantOf(rel, dir string) bool {
	if dir == "" {
		return true
	}
	if rel == dir {
		return true
	}
	return strings.HasPrefix(rel, dir+"/")
}
