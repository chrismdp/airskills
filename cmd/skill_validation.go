package cmd

import (
	"fmt"
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
