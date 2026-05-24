package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

type frontmatterValidationError struct {
	Path   string
	Detail string
	Fix    string
}

func (e *frontmatterValidationError) Error() string {
	if e == nil {
		return ""
	}
	if e.Fix == "" {
		return fmt.Sprintf("%s: %s", e.Path, e.Detail)
	}
	return fmt.Sprintf("%s: %s\nFix: %s", e.Path, e.Detail, e.Fix)
}

func validateSkillFiles(dir string, files map[string][]byte) error {
	skillMd, ok := files["SKILL.md"]
	if !ok {
		return &frontmatterValidationError{
			Path:   "SKILL.md",
			Detail: "missing required SKILL.md file",
			Fix:    "add a SKILL.md file to the skill directory before pushing",
		}
	}
	return validateSkillFrontmatter("SKILL.md", skillMd)
}

func validateSkillFrontmatter(path string, content []byte) error {
	frontmatter, err := extractFrontmatter(content)
	if err != nil {
		return &frontmatterValidationError{
			Path:   path,
			Detail: err.Error(),
			Fix:    "close the YAML frontmatter with a line containing only ---",
		}
	}
	if frontmatter == "" {
		return nil
	}

	var parsed map[string]any
	if err := yaml.Unmarshal([]byte(frontmatter), &parsed); err != nil {
		fix := "fix the YAML syntax in the frontmatter"
		if strings.Contains(frontmatter, "description:") {
			fix = "quote the whole description value, or use folded YAML with `description: >` if the text contains `:`, for example `playbook: /engagement`"
		}
		return &frontmatterValidationError{
			Path:   path,
			Detail: fmt.Sprintf("invalid YAML frontmatter (%v)", err),
			Fix:    fix,
		}
	}

	return nil
}

// fixSkillNameInContent rewrites the `name` field in SKILL.md frontmatter to
// match dirName if they differ. Returns the fixed content and true if changed.
// Used during push for new skills where a `cp` left the original name intact.
func fixSkillNameInContent(dirName string, content []byte) ([]byte, bool) {
	text := strings.ReplaceAll(string(content), "\r\n", "\n")
	if !strings.HasPrefix(text, "---\n") {
		return content, false
	}

	lines := strings.Split(text, "\n")
	closeIdx := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			closeIdx = i
			break
		}
	}
	if closeIdx < 0 {
		return content, false
	}

	frontmatter := strings.Join(lines[1:closeIdx], "\n")
	var parsed map[string]any
	if err := yaml.Unmarshal([]byte(frontmatter), &parsed); err != nil {
		return content, false
	}
	currentName, _ := parsed["name"].(string)
	if currentName == dirName {
		return content, false
	}

	nameFixed := false
	for i := 1; i < closeIdx; i++ {
		if strings.HasPrefix(lines[i], "name:") {
			lines[i] = "name: " + dirName
			nameFixed = true
			break
		}
	}
	if !nameFixed {
		newLines := make([]string, 0, len(lines)+1)
		newLines = append(newLines, lines[0])
		newLines = append(newLines, "name: "+dirName)
		newLines = append(newLines, lines[1:]...)
		lines = newLines
	}

	return []byte(strings.Join(lines, "\n")), true
}

// toolReferenceFields are the SKILL.md frontmatter keys that can name a file
// the skill needs at runtime. Per the agentskills.io spec, `allowed-tools`
// entries may wrap a path in tool syntax (e.g. `Bash(scripts/run.sh:*)`), and
// `script:`/`scripts:` carry path(s) directly.
var toolReferenceFields = []string{"allowed-tools", "allowedTools", "script", "scripts"}

// checkIgnoredToolReferences scans a skill's SKILL.md frontmatter for files
// declared in allowed-tools / script: that the ignore matcher would exclude
// from the push. Such a file ships missing and the skill breaks at use time,
// so push warns (it does NOT fail — the author may be uploading a partial
// preview, unlike the SKILL.md-ignored case which is always fatal).
//
// Only paths that actually exist as files in the skill dir are considered: a
// reference to a non-existent file is a separate problem, and bare tool names
// like "Bash" never match a real file so they can't produce false positives.
// Returns one message per ignored reference, sorted for stable output.
func checkIgnoredToolReferences(dir string, m *ignoreMatcher) []string {
	skillMd, err := os.ReadFile(filepath.Join(dir, skillFile))
	if err != nil {
		return nil
	}
	frontmatter, err := extractFrontmatter(skillMd)
	if err != nil || frontmatter == "" {
		return nil
	}
	var parsed map[string]any
	if err := yaml.Unmarshal([]byte(frontmatter), &parsed); err != nil {
		return nil
	}

	seen := map[string]bool{}
	var warnings []string
	for _, field := range toolReferenceFields {
		for _, frag := range pathFragmentsFromField(parsed[field]) {
			if seen[frag] {
				continue
			}
			seen[frag] = true
			// Only a real file in the skill dir can go missing on upload.
			info, statErr := os.Stat(filepath.Join(dir, frag))
			if statErr != nil || info.IsDir() {
				continue
			}
			if ignored, reason := m.Decide(frag, false); ignored {
				warnings = append(warnings, fmt.Sprintf(
					"%s is referenced by SKILL.md frontmatter but ignored (%s) — the skill won't work without it. Negate the rule (!%s) or remove the pattern.",
					frag, reason, frag))
			}
		}
	}
	sort.Strings(warnings)
	return warnings
}

// pathFragmentsFromField flattens a frontmatter value (string or list) and
// pulls out candidate file-path fragments. allowed-tools entries can wrap a
// path in tool syntax like `Bash(scripts/run.sh:*)`, so we split on the
// characters that delimit those wrappers and keep fragments that look like a
// relative path (contain a "/" or a dotted basename). The caller's existence
// check is the real filter — this only narrows the candidate set.
func pathFragmentsFromField(v any) []string {
	var raw []string
	switch t := v.(type) {
	case string:
		raw = append(raw, t)
	case []any:
		for _, e := range t {
			if s, ok := e.(string); ok {
				raw = append(raw, s)
			}
		}
	default:
		return nil
	}

	var out []string
	for _, entry := range raw {
		fields := strings.FieldsFunc(entry, func(r rune) bool {
			switch r {
			case '(', ')', '[', ']', '{', '}', ':', ',', ' ', '\t', '"', '\'', '*':
				return true
			}
			return false
		})
		for _, frag := range fields {
			frag = strings.TrimPrefix(strings.TrimSpace(frag), "./")
			if frag == "" {
				continue
			}
			if strings.Contains(frag, "/") || strings.Contains(filepath.Base(frag), ".") {
				out = append(out, filepath.ToSlash(frag))
			}
		}
	}
	return out
}

func extractFrontmatter(content []byte) (string, error) {
	text := strings.ReplaceAll(string(content), "\r\n", "\n")
	if !strings.HasPrefix(text, "---\n") {
		return "", nil
	}
	lines := strings.Split(text, "\n")
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			return strings.Join(lines[1:i], "\n"), nil
		}
	}
	return "", fmt.Errorf("frontmatter starts with --- but has no closing --- line")
}
