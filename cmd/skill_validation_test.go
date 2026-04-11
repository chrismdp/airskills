package cmd

import (
	"strings"
	"testing"
)

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
