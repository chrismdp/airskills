package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// agentForDir returns an agentDef whose GlobalDir resolves to dir after
// filepath.Join(home, GlobalDir). It computes the relative path from home to dir.
func agentForDir(t *testing.T, dir string) agentDef {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	rel, err := filepath.Rel(home, dir)
	if err != nil {
		t.Fatal(err)
	}
	return agentDef{Name: "test", GlobalDir: rel}
}

// TestExtractRefSlugsBasic verifies that /slug tokens in SKILL.md body are extracted.
func TestExtractRefSlugsBasic(t *testing.T) {
	text := `---
name: my-skill
---
Use /deploy-check for deployments and /retro for retrospectives.
Also see /plan-skill.
`
	slugs := extractRefSlugs(text)
	want := map[string]bool{"deploy-check": true, "retro": true, "plan-skill": true}
	if len(slugs) != len(want) {
		t.Fatalf("expected %d slugs, got %d: %v", len(want), len(slugs), slugs)
	}
	for _, s := range slugs {
		if !want[s] {
			t.Errorf("unexpected slug: %q", s)
		}
	}
}

// TestExtractRefSlugsStripsURLs verifies that https:// paths are not extracted.
func TestExtractRefSlugsStripsURLs(t *testing.T) {
	text := "Load /good-skill. See https://example.com/path/to/thing for details."
	slugs := extractRefSlugs(text)
	if len(slugs) != 1 || slugs[0] != "good-skill" {
		t.Errorf("expected [good-skill], got %v", slugs)
	}
}

// TestExtractRefSlugsStripsFrontmatter verifies that /name in frontmatter is not matched.
func TestExtractRefSlugsStripsFrontmatter(t *testing.T) {
	text := `---
name: my-skill
description: use /other-skill
---
Body: /other-skill is great.
`
	slugs := extractRefSlugs(text)
	// /other-skill appears in both frontmatter and body; after stripping frontmatter
	// it should appear exactly once (deduped).
	if len(slugs) != 1 || slugs[0] != "other-skill" {
		t.Errorf("expected [other-skill], got %v", slugs)
	}
}

// TestExtractRefSlugsNoDuplicates verifies deduplication.
func TestExtractRefSlugsNoDuplicates(t *testing.T) {
	text := "Use /foo and then /foo again and /foo once more."
	slugs := extractRefSlugs(text)
	if len(slugs) != 1 || slugs[0] != "foo" {
		t.Errorf("expected [foo], got %v", slugs)
	}
}

// TestExtractRefSlugsNoFrontmatter verifies operation when there is no frontmatter.
func TestExtractRefSlugsNoFrontmatter(t *testing.T) {
	text := "Run /skill-a and /skill-b."
	slugs := extractRefSlugs(text)
	want := map[string]bool{"skill-a": true, "skill-b": true}
	if len(slugs) != 2 {
		t.Fatalf("expected 2 slugs, got %d: %v", len(slugs), slugs)
	}
	for _, s := range slugs {
		if !want[s] {
			t.Errorf("unexpected slug: %q", s)
		}
	}
}

// TestWalkBrokenRefsNoInstalled verifies that walkBrokenRefs returns nil when no
// skills are installed (nothing to scan).
func TestWalkBrokenRefsNoInstalled(t *testing.T) {
	// Override the agents list to point at an empty temp dir so the scanner
	// finds no skills without touching the real home dir.
	tmp := t.TempDir()
	skillsDir := filepath.Join(tmp, "skills")
	os.MkdirAll(skillsDir, 0755)

	origAgents := agents
	agents = []agentDef{agentForDir(t, skillsDir)}
	defer func() { agents = origAgents }()

	issues, err := walkBrokenRefs()
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 0 {
		t.Errorf("expected no issues for empty install, got %d", len(issues))
	}
}

// TestWalkBrokenRefsAllKnown verifies that refs satisfied by other local skills
// are not reported as broken.
func TestWalkBrokenRefsAllKnown(t *testing.T) {
	tmp := t.TempDir()
	skillsDir := filepath.Join(tmp, "skills")

	writeSkill := func(name, body string) {
		dir := filepath.Join(skillsDir, name)
		os.MkdirAll(dir, 0755)
		os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0644)
	}

	writeSkill("skill-a", "---\nname: skill-a\n---\nUse /skill-b.\n")
	writeSkill("skill-b", "---\nname: skill-b\n---\nNo refs here.\n")

	origAgents := agents
	agents = []agentDef{agentForDir(t, skillsDir)}
	defer func() { agents = origAgents }()

	issues, err := walkBrokenRefs()
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 0 {
		t.Errorf("expected no issues when refs are satisfied locally, got %d: %v", len(issues), issues)
	}
}

// TestWalkBrokenRefsDetectsUnknown verifies that a ref not satisfied locally is
// flagged as unknown when there is no sync state (offline/untracked).
func TestWalkBrokenRefsDetectsUnknown(t *testing.T) {
	tmp := t.TempDir()
	skillsDir := filepath.Join(tmp, "skills")
	skillDir := filepath.Join(skillsDir, "my-skill")
	os.MkdirAll(skillDir, 0755)
	os.WriteFile(filepath.Join(skillDir, "SKILL.md"),
		[]byte("---\nname: my-skill\n---\nLoad /gone-skill.\n"), 0644)

	origAgents := agents
	agents = []agentDef{agentForDir(t, skillsDir)}
	defer func() { agents = origAgents }()

	issues, err := walkBrokenRefs()
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 1 {
		t.Fatalf("expected 1 issue, got %d: %v", len(issues), issues)
	}
	if issues[0].refSlug != "gone-skill" {
		t.Errorf("expected refSlug=gone-skill, got %q", issues[0].refSlug)
	}
	if issues[0].status != "unknown" {
		t.Errorf("expected status=unknown, got %q", issues[0].status)
	}
}

func TestRenderSyncStateReportSummarisesSynced(t *testing.T) {
	var buf strings.Builder
	renderSyncStateReport(&buf, []SkillStateInfo{
		{Name: "a", State: StateSynced},
		{Name: "b", State: StateSynced},
	})
	out := buf.String()
	if !strings.Contains(out, "2 synced") {
		t.Errorf("expected '2 synced' summary, got: %q", out)
	}
}

func TestRenderSyncStateReportSurfacesNotableStates(t *testing.T) {
	var buf strings.Builder
	renderSyncStateReport(&buf, []SkillStateInfo{
		{Name: "owned-edited", State: StateModified},
		{Name: "sourced-pending", State: StateModifiedPending},
		{Name: "drive-by", State: StateUntracked},
		{Name: "matches-server", State: StateLinked},
		{Name: "name-collides", State: StateUntrackedConflict},
		{Name: "elsewhere", State: StateNotLocal},
	})
	out := buf.String()
	for _, want := range []string{
		"owned-edited", "modified locally",
		"sourced-pending", "airskills resolve sourced-pending",
		"drive-by", "untracked",
		"matches-server", "link silently",
		"name-collides", "conflict",
		"elsewhere", "not installed",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("expected output to mention %q, got:\n%s", want, out)
		}
	}
}

func TestRenderSyncStateReportEmpty(t *testing.T) {
	var buf strings.Builder
	renderSyncStateReport(&buf, nil)
	out := buf.String()
	if !strings.Contains(out, "no skills tracked") {
		t.Errorf("expected empty-state message, got: %q", out)
	}
}
