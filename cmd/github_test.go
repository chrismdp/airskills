package cmd

import (
	"sort"
	"strings"
	"testing"
)

// helper: find a foundSkill in slice by leaf name
func findByLeaf(skills []foundSkill, leafName string) *foundSkill {
	for i := range skills {
		if skills[i].LeafName == leafName {
			return &skills[i]
		}
	}
	return nil
}

// helper: collect leaf names from slice, sorted
func leafNames(skills []foundSkill) []string {
	names := make([]string, 0, len(skills))
	for _, s := range skills {
		names = append(names, s.LeafName)
	}
	sort.Strings(names)
	return names
}

// TestFindSkillsInFiles_RootLevel: single SKILL.md at repo root
func TestFindSkillsInFiles_RootLevel(t *testing.T) {
	allFiles := map[string][]byte{
		"SKILL.md":  []byte("---\nname: myskill\n---"),
		"README.md": []byte("# readme"),
	}
	skills, err := findSkillsInFiles(allFiles)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(skills) != 1 {
		t.Fatalf("expected 1 skill, got %d", len(skills))
	}
	s := skills[0]
	if s.LeafName != "" {
		t.Errorf("expected empty leaf name, got %q", s.LeafName)
	}
	if s.FullPath != "" {
		t.Errorf("expected empty full path, got %q", s.FullPath)
	}
	// Root-level files should be included
	if _, ok := s.Files["SKILL.md"]; !ok {
		t.Error("expected SKILL.md in root skill files")
	}
	if _, ok := s.Files["README.md"]; !ok {
		t.Error("expected README.md in root skill files")
	}
}

// TestFindSkillsInFiles_OneLevelNesting: existing behaviour — SKILL.md one level deep
func TestFindSkillsInFiles_OneLevelNesting(t *testing.T) {
	allFiles := map[string][]byte{
		"foo/SKILL.md":        []byte("---\nname: foo\n---"),
		"foo/references/a.md": []byte("content"),
	}
	skills, err := findSkillsInFiles(allFiles)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(skills) != 1 {
		t.Fatalf("expected 1 skill, got %d", len(skills))
	}
	s := findByLeaf(skills, "foo")
	if s == nil {
		t.Fatal("skill 'foo' not found")
	}
	if s.FullPath != "foo" {
		t.Errorf("expected full path 'foo', got %q", s.FullPath)
	}
	if _, ok := s.Files["SKILL.md"]; !ok {
		t.Error("expected SKILL.md in skill files")
	}
	if _, ok := s.Files["references/a.md"]; !ok {
		t.Error("expected references/a.md in skill files")
	}
}

// TestFindSkillsInFiles_DeepNesting: SKILL.md nested more than one dir deep
func TestFindSkillsInFiles_DeepNesting(t *testing.T) {
	allFiles := map[string][]byte{
		"a/b/c/foo/SKILL.md": []byte("---\nname: foo\n---"),
		"a/b/c/foo/bar.md":   []byte("content"),
		"a/b/c/README.md":    []byte("# repo readme — not part of skill"),
	}
	skills, err := findSkillsInFiles(allFiles)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(skills) != 1 {
		t.Fatalf("expected 1 skill, got %d", len(skills))
	}
	s := findByLeaf(skills, "foo")
	if s == nil {
		t.Fatal("skill 'foo' not found")
	}
	if s.FullPath != "a/b/c/foo" {
		t.Errorf("expected full path 'a/b/c/foo', got %q", s.FullPath)
	}
	if _, ok := s.Files["SKILL.md"]; !ok {
		t.Error("expected SKILL.md in skill files")
	}
	if _, ok := s.Files["bar.md"]; !ok {
		t.Error("expected bar.md in skill files")
	}
	// Ancestor files should NOT be included
	if _, ok := s.Files["a/b/c/README.md"]; ok {
		t.Error("ancestor README.md should not be in skill files")
	}
}

// TestFindSkillsInFiles_PluginLayout: four SKILL.md files under a plugins/ prefix
func TestFindSkillsInFiles_PluginLayout(t *testing.T) {
	allFiles := map[string][]byte{
		"plugins/mcp-apps/skills/convert-web-app/SKILL.md":      []byte("name: convert"),
		"plugins/mcp-apps/skills/create-mcp-app/SKILL.md":       []byte("name: create"),
		"plugins/mcp-apps/skills/migrate-oai-app/SKILL.md":      []byte("name: migrate"),
		"plugins/mcp-apps/skills/add-app-to-server/SKILL.md":    []byte("name: add"),
		"plugins/mcp-apps/skills/convert-web-app/references.md": []byte("ref"),
		"plugins/README.md": []byte("# not a skill"),
	}
	skills, err := findSkillsInFiles(allFiles)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(skills) != 4 {
		t.Fatalf("expected 4 skills, got %d: %v", len(skills), leafNames(skills))
	}
	want := []string{"add-app-to-server", "convert-web-app", "create-mcp-app", "migrate-oai-app"}
	got := leafNames(skills)
	for i, w := range want {
		if i >= len(got) || got[i] != w {
			t.Errorf("expected skill %q at index %d, got %v", w, i, got)
		}
	}
	// Reference file assigned to correct skill
	s := findByLeaf(skills, "convert-web-app")
	if s == nil {
		t.Fatal("skill 'convert-web-app' not found")
	}
	if _, ok := s.Files["references.md"]; !ok {
		t.Error("expected references.md in convert-web-app files")
	}
}

// TestFindSkillsInFiles_LeafCollision: two SKILL.md with same leaf name → error
func TestFindSkillsInFiles_LeafCollision(t *testing.T) {
	allFiles := map[string][]byte{
		"a/foo/SKILL.md": []byte("name: foo"),
		"b/foo/SKILL.md": []byte("name: foo"),
	}
	_, err := findSkillsInFiles(allFiles)
	if err == nil {
		t.Fatal("expected collision error, got nil")
	}
	// Error should mention both paths
	errStr := err.Error()
	if !strings.Contains(errStr, "a/foo") {
		t.Errorf("expected error to mention 'a/foo', got: %s", errStr)
	}
	if !strings.Contains(errStr, "b/foo") {
		t.Errorf("expected error to mention 'b/foo', got: %s", errStr)
	}
}

// TestFindSkillsInFiles_NestedFileAssignment: files assigned to nearest ancestor skill
func TestFindSkillsInFiles_NestedFileAssignment(t *testing.T) {
	allFiles := map[string][]byte{
		"plugins/p/skills/foo/SKILL.md":             []byte("name: foo"),
		"plugins/p/skills/foo/references/bar.md":    []byte("ref content"),
		"plugins/p/skills/foo/sub/deep/nested.txt":  []byte("deep"),
		"plugins/p/README.md":                       []byte("# not in skill"),
	}
	skills, err := findSkillsInFiles(allFiles)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(skills) != 1 {
		t.Fatalf("expected 1 skill, got %d", len(skills))
	}
	s := findByLeaf(skills, "foo")
	if s == nil {
		t.Fatal("skill 'foo' not found")
	}
	if _, ok := s.Files["references/bar.md"]; !ok {
		t.Error("expected references/bar.md in foo files")
	}
	if _, ok := s.Files["sub/deep/nested.txt"]; !ok {
		t.Error("expected sub/deep/nested.txt in foo files")
	}
	// Ancestor files should not appear
	if _, ok := s.Files["plugins/p/README.md"]; ok {
		t.Error("ancestor README.md should not be in foo files")
	}
}

// TestFindSkillsInFiles_Empty: no SKILL.md → empty result, no error
func TestFindSkillsInFiles_Empty(t *testing.T) {
	allFiles := map[string][]byte{
		"README.md": []byte("no skills here"),
	}
	skills, err := findSkillsInFiles(allFiles)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(skills) != 0 {
		t.Errorf("expected 0 skills, got %d", len(skills))
	}
}

