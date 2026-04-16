package cmd

import (
	"strings"
	"testing"
)

func TestFixSkillNameInContent(t *testing.T) {
	base := []byte("---\nname: old-name\ndescription: A skill.\n---\n\nBody text.\n")

	fixed, changed := fixSkillNameInContent("new-name", base)
	if !changed {
		t.Fatal("expected changed=true")
	}
	if !strings.Contains(string(fixed), "name: new-name") {
		t.Fatalf("fixed content missing new name:\n%s", fixed)
	}
	if strings.Contains(string(fixed), "name: old-name") {
		t.Fatalf("fixed content still has old name:\n%s", fixed)
	}
	if !strings.Contains(string(fixed), "description: A skill.") {
		t.Fatalf("fixed content lost description:\n%s", fixed)
	}
}

func TestFixSkillNameInContentNoChange(t *testing.T) {
	base := []byte("---\nname: correct-name\ndescription: A skill.\n---\n\nBody.\n")
	_, changed := fixSkillNameInContent("correct-name", base)
	if changed {
		t.Fatal("expected changed=false when name already matches")
	}
}

func TestFixSkillNameInContentNoFrontmatter(t *testing.T) {
	base := []byte("# No frontmatter\n\nJust body.\n")
	_, changed := fixSkillNameInContent("my-skill", base)
	if changed {
		t.Fatal("expected changed=false when no frontmatter")
	}
}

func TestValidateSkillFrontmatterAllowsValidYAML(t *testing.T) {
	content := []byte("---\nname: engagement\ndescription: >\n  Client engagement playbook for `playbook: /engagement`.\n---\n\n# Heading\n")
	if err := validateSkillFrontmatter("SKILL.md", content); err != nil {
		t.Fatalf("validateSkillFrontmatter returned error: %v", err)
	}
}

func TestValidateSkillFrontmatterRejectsInvalidYAML(t *testing.T) {
	content := []byte("---\nname: engagement\ndescription: Client engagement playbook for `playbook: /engagement`.\n---\n")
	err := validateSkillFrontmatter("SKILL.md", content)
	if err == nil {
		t.Fatal("expected invalid YAML error")
	}
	got := err.Error()
	for _, want := range []string{"invalid YAML frontmatter", "quote the whole description value", "description: >"} {
		if !strings.Contains(got, want) {
			t.Fatalf("error %q does not contain %q", got, want)
		}
	}
}

func TestValidateSkillFrontmatterRejectsUnclosedFrontmatter(t *testing.T) {
	content := []byte("---\nname: engagement\ndescription: quoted\n# Heading\n")
	err := validateSkillFrontmatter("SKILL.md", content)
	if err == nil {
		t.Fatal("expected unclosed frontmatter error")
	}
	got := err.Error()
	for _, want := range []string{"no closing --- line", "close the YAML frontmatter"} {
		if !strings.Contains(got, want) {
			t.Fatalf("error %q does not contain %q", got, want)
		}
	}
}
