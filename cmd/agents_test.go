package cmd

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func setTestHome(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
}

func TestInstallSkillToAgents(t *testing.T) {
	// Create fake agent directories in a temp home
	tmpHome := t.TempDir()
	setTestHome(t, tmpHome)

	// Create Claude Code and Cursor skill parent dirs
	os.MkdirAll(filepath.Join(tmpHome, ".claude", "skills"), 0755)
	os.MkdirAll(filepath.Join(tmpHome, ".cursor", "skills"), 0755)

	files := map[string][]byte{
		"SKILL.md":       []byte("# Test\nHello"),
		"scripts/run.sh": []byte("#!/bin/bash\necho hi"),
	}

	installed, err := installSkillToAgents("test-skill", files)
	if err != nil {
		t.Fatalf("installSkillToAgents: %v", err)
	}

	if len(installed) < 2 {
		t.Errorf("expected at least 2 agents, got %d: %v", len(installed), installed)
	}

	// Verify files exist in Claude Code
	content, err := os.ReadFile(filepath.Join(tmpHome, ".claude", "skills", "test-skill", "SKILL.md"))
	if err != nil {
		t.Fatalf("missing SKILL.md in Claude Code: %v", err)
	}
	if string(content) != "# Test\nHello" {
		t.Errorf("content = %q", string(content))
	}

	// Verify files exist in Cursor
	content, err = os.ReadFile(filepath.Join(tmpHome, ".cursor", "skills", "test-skill", "SKILL.md"))
	if err != nil {
		t.Fatalf("missing SKILL.md in Cursor: %v", err)
	}
	if string(content) != "# Test\nHello" {
		t.Errorf("cursor content = %q", string(content))
	}

	// Verify subdirectory files
	_, err = os.ReadFile(filepath.Join(tmpHome, ".claude", "skills", "test-skill", "scripts", "run.sh"))
	if err != nil {
		t.Error("missing scripts/run.sh in Claude Code")
	}
}

func TestInstallSkillPreservesExecutableBitOnShebangFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix mode bits don't apply on Windows")
	}

	tmpHome := t.TempDir()
	setTestHome(t, tmpHome)
	os.MkdirAll(filepath.Join(tmpHome, ".claude", "skills"), 0755)

	files := map[string][]byte{
		"SKILL.md":   []byte("# Test\nHello"),
		"run.sh":     []byte("#!/bin/bash\necho hi"),
		"helper.py":  []byte("#!/usr/bin/env python3\nprint('hi')"),
		"data.txt":   []byte("plain data"),
		"README.md":  []byte("# Plain markdown, no shebang"),
	}

	if _, err := installSkillToAgents("test-exec", files); err != nil {
		t.Fatalf("installSkillToAgents: %v", err)
	}

	skillDir := filepath.Join(tmpHome, ".claude", "skills", "test-exec")

	for _, name := range []string{"run.sh", "helper.py"} {
		info, err := os.Stat(filepath.Join(skillDir, name))
		if err != nil {
			t.Fatalf("stat %s: %v", name, err)
		}
		if info.Mode()&0111 == 0 {
			t.Errorf("expected %s to be executable, got mode %v", name, info.Mode())
		}
	}

	for _, name := range []string{"SKILL.md", "data.txt", "README.md"} {
		info, err := os.Stat(filepath.Join(skillDir, name))
		if err != nil {
			t.Fatalf("stat %s: %v", name, err)
		}
		if info.Mode()&0111 != 0 {
			t.Errorf("expected %s NOT to be executable, got mode %v", name, info.Mode())
		}
	}
}

func TestDetectInstalledAgents(t *testing.T) {
	tmpHome := t.TempDir()
	setTestHome(t, tmpHome)

	// No agent dirs — should return empty
	detected := detectInstalledAgents()
	if len(detected) != 0 {
		t.Errorf("expected 0 agents, got %d", len(detected))
	}

	// Create Claude Code dir
	os.MkdirAll(filepath.Join(tmpHome, ".claude"), 0755)
	detected = detectInstalledAgents()
	if len(detected) != 1 || detected[0].Key != "claude-code" {
		t.Errorf("expected [claude-code], got %v", detected)
	}
}

// writeSkillFile is a small test helper that creates parent dirs and writes a file.
func writeSkillFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// TestInstallSkillToOpenClaw verifies that airskills installs skills to
// OpenClaw's global skill directory (~/.openclaw/skills/) when it is detected.
func TestInstallSkillToOpenClaw(t *testing.T) {
	tmpHome := t.TempDir()
	setTestHome(t, tmpHome)

	// Only create the OpenClaw parent dir — no other agents installed.
	os.MkdirAll(filepath.Join(tmpHome, ".openclaw", "skills"), 0755)

	files := map[string][]byte{
		"SKILL.md": []byte("# OpenClaw Test\nHello from OpenClaw"),
	}

	installed, err := installSkillToAgents("test-skill", files)
	if err != nil {
		t.Fatalf("installSkillToAgents: %v", err)
	}

	if len(installed) == 0 {
		t.Fatalf("expected openclaw to be detected and skill installed, got 0 installs")
	}

	content, err := os.ReadFile(filepath.Join(tmpHome, ".openclaw", "skills", "test-skill", "SKILL.md"))
	if err != nil {
		t.Fatalf("missing SKILL.md in OpenClaw: %v", err)
	}
	if string(content) != "# OpenClaw Test\nHello from OpenClaw" {
		t.Errorf("openclaw content = %q", string(content))
	}
}

// TestDetectOpenClawAgent verifies that an ~/.openclaw directory triggers
// detection of the openclaw agent entry.
func TestDetectOpenClawAgent(t *testing.T) {
	tmpHome := t.TempDir()
	setTestHome(t, tmpHome)

	// No OpenClaw dir — should not detect.
	detected := detectInstalledAgents()
	for _, a := range detected {
		if a.Key == "openclaw" {
			t.Errorf("should not detect openclaw without ~/.openclaw present")
		}
	}

	// Create ~/.openclaw — detection should now find it.
	os.MkdirAll(filepath.Join(tmpHome, ".openclaw"), 0755)
	detected = detectInstalledAgents()
	found := false
	for _, a := range detected {
		if a.Key == "openclaw" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected openclaw to be detected after creating ~/.openclaw")
	}
}

// TestInstallSkillToProjectAgentsDirIfExists verifies that when the current
// working directory has a pre-existing .agents/skills/ folder (the standard
// agentskills.io repo-local path), installSkillToAgents mirrors the skill
// into it alongside the detected global agent dirs.
//
// The project dir is only written when it already exists — airskills must
// never create .agents/skills/ in a repo on its own.
func TestInstallSkillToProjectAgentsDirIfExists(t *testing.T) {
	tmpHome := t.TempDir()
	setTestHome(t, tmpHome)
	os.MkdirAll(filepath.Join(tmpHome, ".claude", "skills"), 0755)

	tmpCwd := t.TempDir()
	t.Chdir(tmpCwd)
	os.MkdirAll(filepath.Join(tmpCwd, ".agents", "skills"), 0755)

	files := map[string][]byte{"SKILL.md": []byte("# proj")}
	if _, err := installSkillToAgents("proj-skill", files); err != nil {
		t.Fatalf("installSkillToAgents: %v", err)
	}

	content, err := os.ReadFile(filepath.Join(tmpCwd, ".agents", "skills", "proj-skill", "SKILL.md"))
	if err != nil {
		t.Fatalf("expected skill written to project .agents/skills: %v", err)
	}
	if string(content) != "# proj" {
		t.Errorf("project content = %q", string(content))
	}
}

// TestInstallSkillSkipsProjectAgentsDirIfMissing verifies that airskills never
// creates .agents/skills/ in a repo that hasn't opted in by creating that
// directory itself.
func TestInstallSkillSkipsProjectAgentsDirIfMissing(t *testing.T) {
	tmpHome := t.TempDir()
	setTestHome(t, tmpHome)
	os.MkdirAll(filepath.Join(tmpHome, ".claude", "skills"), 0755)

	tmpCwd := t.TempDir()
	t.Chdir(tmpCwd)
	// Deliberately do NOT create .agents/skills.

	files := map[string][]byte{"SKILL.md": []byte("# proj")}
	if _, err := installSkillToAgents("proj-skill", files); err != nil {
		t.Fatalf("installSkillToAgents: %v", err)
	}

	if _, err := os.Stat(filepath.Join(tmpCwd, ".agents")); err == nil {
		t.Errorf("airskills must not create .agents/ in cwd when it did not already exist")
	}
}

// TestMirrorPropagatesEditFromProjectAgentsDir verifies that an edit to a
// skill in $CWD/.agents/skills/ is mirrored back into the global agent dirs
// the same way an edit in any other detected dir would be.
func TestMirrorPropagatesEditFromProjectAgentsDir(t *testing.T) {
	tmpHome := t.TempDir()
	setTestHome(t, tmpHome)

	tmpCwd := t.TempDir()
	t.Chdir(tmpCwd)

	claudePath := filepath.Join(tmpHome, ".claude", "skills", "foo", "SKILL.md")
	projectPath := filepath.Join(tmpCwd, ".agents", "skills", "foo", "SKILL.md")

	writeSkillFile(t, claudePath, "# old")
	writeSkillFile(t, projectPath, "# edited in project")

	markerHash := computeMerkleHash(map[string][]byte{"SKILL.md": []byte("# old")})
	state := &SyncState{
		Version: 1,
		Skills: map[string]*SyncEntry{
			"foo": {SkillID: testUUID("skill-1").String(), Version: "1.0.0", ContentHash: markerHash, Tool: "claude-code"},
		},
	}

	_, conflicts, _ := mirrorLocalSkills(state)
	if len(conflicts) != 0 {
		t.Fatalf("unexpected conflicts: %+v", conflicts)
	}

	claude, _ := os.ReadFile(claudePath)
	if string(claude) != "# edited in project" {
		t.Errorf("claude copy = %q, want '# edited in project'", string(claude))
	}
}

// TestInstallSkillToHermes verifies that airskills installs skills to
// the Hermes Agent's global skill directory (~/.hermes/skills/) when it is
// detected. Hermes ships from NousResearch and treats ~/.hermes/skills/ as
// the primary source of truth for skill definitions.
func TestInstallSkillToHermes(t *testing.T) {
	tmpHome := t.TempDir()
	setTestHome(t, tmpHome)

	// Only create the Hermes parent dir — no other agents installed.
	os.MkdirAll(filepath.Join(tmpHome, ".hermes", "skills"), 0755)

	files := map[string][]byte{
		"SKILL.md": []byte("# Hermes Test\nHello from Hermes"),
	}

	installed, err := installSkillToAgents("test-skill", files)
	if err != nil {
		t.Fatalf("installSkillToAgents: %v", err)
	}

	if len(installed) == 0 {
		t.Fatalf("expected hermes to be detected and skill installed, got 0 installs")
	}

	content, err := os.ReadFile(filepath.Join(tmpHome, ".hermes", "skills", "test-skill", "SKILL.md"))
	if err != nil {
		t.Fatalf("missing SKILL.md in Hermes: %v", err)
	}
	if string(content) != "# Hermes Test\nHello from Hermes" {
		t.Errorf("hermes content = %q", string(content))
	}
}

// TestDetectHermesAgent verifies that an ~/.hermes directory triggers
// detection of the hermes agent entry.
func TestDetectHermesAgent(t *testing.T) {
	tmpHome := t.TempDir()
	setTestHome(t, tmpHome)

	// No Hermes dir — should not detect.
	detected := detectInstalledAgents()
	for _, a := range detected {
		if a.Key == "hermes" {
			t.Errorf("should not detect hermes without ~/.hermes present")
		}
	}

	// Create ~/.hermes — detection should now find it.
	os.MkdirAll(filepath.Join(tmpHome, ".hermes"), 0755)
	detected = detectInstalledAgents()
	found := false
	for _, a := range detected {
		if a.Key == "hermes" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected hermes to be detected after creating ~/.hermes")
	}
}

// TestMirrorPropagatesEditFromNonFirstDir covers the core requirement: when a
// skill exists in two detected agent dirs and the user has edited the copy
// that isn't first in the agent registry, the edit still wins and is mirrored
// to the other copies.
func TestMirrorPropagatesEditFromNonFirstDir(t *testing.T) {
	tmpHome := t.TempDir()
	setTestHome(t, tmpHome)

	claudePath := filepath.Join(tmpHome, ".claude", "skills", "foo", "SKILL.md")
	cursorPath := filepath.Join(tmpHome, ".cursor", "skills", "foo", "SKILL.md")

	// Old (marker-matching) version lives in the first-found dir (.claude).
	writeSkillFile(t, claudePath, "# old")
	// Edited version lives in a later dir.
	writeSkillFile(t, cursorPath, "# edited")

	markerHash := computeMerkleHash(map[string][]byte{"SKILL.md": []byte("# old")})
	state := &SyncState{
		Version: 1,
		Skills: map[string]*SyncEntry{
			"foo": {SkillID: testUUID("skill-1").String(), Version: "1.0.0", ContentHash: markerHash, Tool: "claude-code"},
		},
	}

	_, conflicts, _ := mirrorLocalSkills(state)
	if len(conflicts) != 0 {
		t.Fatalf("unexpected conflicts: %+v", conflicts)
	}

	claude, _ := os.ReadFile(claudePath)
	if string(claude) != "# edited" {
		t.Errorf("claude copy = %q, want '# edited'", string(claude))
	}
	cursor, _ := os.ReadFile(cursorPath)
	if string(cursor) != "# edited" {
		t.Errorf("cursor copy = %q, want '# edited'", string(cursor))
	}
}

// TestMirrorCreatesInMissingDetectedDir verifies that sync mirrors a local
// skill into every *detected* agent dir, even if the skill doesn't exist
// there yet. The user explicitly asked for "all other" — not "only existing".
func TestMirrorCreatesInMissingDetectedDir(t *testing.T) {
	tmpHome := t.TempDir()
	setTestHome(t, tmpHome)

	// Both agents are installed (parent dirs exist)…
	os.MkdirAll(filepath.Join(tmpHome, ".claude", "skills"), 0755)
	os.MkdirAll(filepath.Join(tmpHome, ".cursor", "skills"), 0755)

	// …but the skill only lives in .claude.
	claudePath := filepath.Join(tmpHome, ".claude", "skills", "foo", "SKILL.md")
	writeSkillFile(t, claudePath, "# content")

	state := &SyncState{Version: 1, Skills: map[string]*SyncEntry{}}

	_, conflicts, _ := mirrorLocalSkills(state)
	if len(conflicts) != 0 {
		t.Fatalf("unexpected conflicts: %+v", conflicts)
	}

	cursorPath := filepath.Join(tmpHome, ".cursor", "skills", "foo", "SKILL.md")
	cursor, err := os.ReadFile(cursorPath)
	if err != nil {
		t.Fatalf("expected cursor copy to be created: %v", err)
	}
	if string(cursor) != "# content" {
		t.Errorf("cursor copy = %q, want '# content'", string(cursor))
	}
}

// TestMirrorStaleSecondaryCopyLosesToFreshPrimaryEdit covers the case that
// broke the platform e2e: a previous mirror fanned content out to a
// secondary agent dir, and the user has since edited the original in place.
// The marker is stale (pre-edit), neither copy matches it, and naively
// this looks like a conflict. Mirror must fall back to newest mtime so the
// fresh edit wins and overwrites the stale secondary.
func TestMirrorStaleSecondaryCopyLosesToFreshPrimaryEdit(t *testing.T) {
	tmpHome := t.TempDir()
	setTestHome(t, tmpHome)

	claudePath := filepath.Join(tmpHome, ".claude", "skills", "foo", "SKILL.md")
	cursorPath := filepath.Join(tmpHome, ".cursor", "skills", "foo", "SKILL.md")

	// The stale secondary copy is written first, then backdated so its
	// mtime is clearly older than the user's edit.
	writeSkillFile(t, cursorPath, "# stale mirror from last run")
	staleTime := time.Now().Add(-1 * time.Hour)
	if err := os.Chtimes(cursorPath, staleTime, staleTime); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	writeSkillFile(t, claudePath, "# fresh user edit")

	// Marker references a third, older content that doesn't match either copy.
	markerHash := computeMerkleHash(map[string][]byte{"SKILL.md": []byte("# original")})
	state := &SyncState{
		Version: 1,
		Skills: map[string]*SyncEntry{
			"foo": {SkillID: testUUID("skill-1").String(), Version: "1.0.0", ContentHash: markerHash, Tool: "claude-code"},
		},
	}

	_, conflicts, _ := mirrorLocalSkills(state)
	if len(conflicts) != 0 {
		t.Fatalf("unexpected conflicts: %+v", conflicts)
	}

	// Both copies should now hold the fresh user edit.
	claude, _ := os.ReadFile(claudePath)
	if string(claude) != "# fresh user edit" {
		t.Errorf("claude copy = %q, want fresh user edit", string(claude))
	}
	cursor, _ := os.ReadFile(cursorPath)
	if string(cursor) != "# fresh user edit" {
		t.Errorf("cursor copy = %q, want fresh user edit", string(cursor))
	}
}

// TestMirrorDoesNotRevertEditAfterPushAdvancedMarker reproduces the bug in
// cli-mirror-overwrites-edit-after-push: after push uploads the user's edit
// and optimistically advances the marker to the new hash, pull's mirror step
// runs against (claude=H_new edit, cursor=H_old stale, marker=H_new). The
// 2-group + marker heuristic in pickAuthoritativeHash returns the non-marker
// hash, which is now the stale group — and mirror silently overwrites the
// user's edit with the stale content.
//
// Correct behaviour: the edit must survive. Either H_new wins (preferred —
// mirror H_new into the stale dirs) or the case is reported as a conflict.
// What must NOT happen is silently flattening claude back to H_old.
func TestMirrorDoesNotRevertEditAfterPushAdvancedMarker(t *testing.T) {
	tmpHome := t.TempDir()
	setTestHome(t, tmpHome)

	claudePath := filepath.Join(tmpHome, ".claude", "skills", "foo", "SKILL.md")
	cursorPath := filepath.Join(tmpHome, ".cursor", "skills", "foo", "SKILL.md")

	// cursor stays at the old (stale) content; backdate its mtime so the
	// fresh edit in claude is unambiguously newer regardless of filesystem
	// mtime resolution.
	writeSkillFile(t, cursorPath, "# old")
	staleTime := time.Now().Add(-1 * time.Hour)
	if err := os.Chtimes(cursorPath, staleTime, staleTime); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	// claude holds the fresh edit. Push has just uploaded this hash and
	// optimistically advanced the marker to it.
	writeSkillFile(t, claudePath, "# edited")

	// Marker reflects the post-push state — it's been advanced to H_new
	// (the edit's hash), because push uploaded claude's contents and
	// optimistically updated the marker.
	editedHash := computeMerkleHash(map[string][]byte{"SKILL.md": []byte("# edited")})
	state := &SyncState{
		Version: 1,
		Skills: map[string]*SyncEntry{
			"foo": {SkillID: testUUID("skill-1").String(), Version: "1.0.0", ContentHash: editedHash, Tool: "claude-code"},
		},
	}

	_, conflicts, _ := mirrorLocalSkills(state)
	if len(conflicts) != 0 {
		t.Fatalf("unexpected conflicts: %+v", conflicts)
	}

	// The user's edit must still be on disk in claude.
	claude, _ := os.ReadFile(claudePath)
	if string(claude) != "# edited" {
		t.Errorf("claude copy = %q, want '# edited' (mirror silently reverted the edit)", string(claude))
	}
	// And the stale cursor copy should have been brought forward, not the
	// other way around.
	cursor, _ := os.ReadFile(cursorPath)
	if string(cursor) != "# edited" {
		t.Errorf("cursor copy = %q, want '# edited' (mirror failed to propagate edit)", string(cursor))
	}
}

// TestMirrorAllCopiesIdenticalNoOp verifies that when every copy already
// matches, mirror reports no changes (and no conflicts).
func TestMirrorAllCopiesIdenticalNoOp(t *testing.T) {
	tmpHome := t.TempDir()
	setTestHome(t, tmpHome)

	claudePath := filepath.Join(tmpHome, ".claude", "skills", "foo", "SKILL.md")
	cursorPath := filepath.Join(tmpHome, ".cursor", "skills", "foo", "SKILL.md")
	writeSkillFile(t, claudePath, "# same")
	writeSkillFile(t, cursorPath, "# same")

	state := &SyncState{Version: 1, Skills: map[string]*SyncEntry{}}

	changes, conflicts, _ := mirrorLocalSkills(state)
	if len(conflicts) != 0 {
		t.Fatalf("unexpected conflicts: %+v", conflicts)
	}
	for _, c := range changes {
		if len(c.written) != 0 {
			t.Errorf("expected no writes, got %+v", c)
		}
	}
}

// TestNamespacedSlug verifies the naming helper used by the add command.
func TestNamespacedSlug(t *testing.T) {
	tests := []struct {
		owner, slug, want string
	}{
		{"chrismdp", "my-skill", "chrismdp-my-skill"},
		{"acme-corp", "deploy", "acme-corp-deploy"},
		{"", "my-skill", "my-skill"}, // no owner → no prefix (local/personal skill)
	}
	for _, tt := range tests {
		got := namespacedSlug(tt.owner, tt.slug)
		if got != tt.want {
			t.Errorf("namespacedSlug(%q, %q) = %q, want %q", tt.owner, tt.slug, got, tt.want)
		}
	}
}

// TestMigrateToNamespacedDirsRenamesOldInstall verifies that a skill installed
// under the bare slug is renamed to the namespaced {owner}-{slug} format.
func TestMigrateToNamespacedDirsRenamesOldInstall(t *testing.T) {
	tmpHome := t.TempDir()
	setTestHome(t, tmpHome)

	// Set up Claude Code and Cursor with a skill under the old (bare) slug.
	for _, dir := range []string{".claude/skills/my-skill", ".cursor/skills/my-skill"} {
		writeSkillFile(t, filepath.Join(tmpHome, dir, "SKILL.md"), "# my skill")
	}

	syncState := &SyncState{
		Version: 1,
		Skills: map[string]*SyncEntry{
			"my-skill": {
				SkillID: testUUID("skill-123").String(),
				Version: "1.0.0",
				Tool:    "claude-code",
				Source: &skillSource{
					Owner: "chrismdp",
					Slug:  "my-skill",
					ID:    "skill-123",
				},
			},
		},
	}

	migrateToNamespacedDirs(syncState)

	// Old dirs should be gone
	if _, err := os.Stat(filepath.Join(tmpHome, ".claude", "skills", "my-skill")); !os.IsNotExist(err) {
		t.Error("old .claude/skills/my-skill should have been removed")
	}
	if _, err := os.Stat(filepath.Join(tmpHome, ".cursor", "skills", "my-skill")); !os.IsNotExist(err) {
		t.Error("old .cursor/skills/my-skill should have been removed")
	}

	// New namespaced dirs should exist with correct content
	data, err := os.ReadFile(filepath.Join(tmpHome, ".claude", "skills", "chrismdp-my-skill", "SKILL.md"))
	if err != nil {
		t.Fatalf("expected chrismdp-my-skill to exist in .claude: %v", err)
	}
	if string(data) != "# my skill" {
		t.Errorf("content = %q", string(data))
	}

	// Sync state key should be updated
	if _, ok := syncState.Skills["my-skill"]; ok {
		t.Error("old sync state key 'my-skill' should have been removed")
	}
	if _, ok := syncState.Skills["chrismdp-my-skill"]; !ok {
		t.Error("new sync state key 'chrismdp-my-skill' should exist")
	}
}

// TestMigrateToNamespacedDirsNoop verifies that already-namespaced skills are
// left untouched.
func TestMigrateToNamespacedDirsNoop(t *testing.T) {
	tmpHome := t.TempDir()
	setTestHome(t, tmpHome)

	writeSkillFile(t, filepath.Join(tmpHome, ".claude", "skills", "chrismdp-my-skill", "SKILL.md"), "# content")

	syncState := &SyncState{
		Version: 1,
		Skills: map[string]*SyncEntry{
			"chrismdp-my-skill": {
				SkillID: testUUID("skill-123").String(),
				Version: "1.0.0",
				Tool:    "claude-code",
				Source: &skillSource{
					Owner: "chrismdp",
					Slug:  "my-skill",
					ID:    "skill-123",
				},
			},
		},
	}

	migrateToNamespacedDirs(syncState)

	// Key should still be present and unchanged
	if _, ok := syncState.Skills["chrismdp-my-skill"]; !ok {
		t.Error("already-namespaced key should still exist")
	}
	if _, ok := syncState.Skills["my-skill"]; ok {
		t.Error("bare key should not appear after noop migration")
	}
}

// TestMigrateToNamespacedDirsLeavesLocalSkillsAlone verifies that skills
// without a Source (user-created, not installed via add) are not renamed.
func TestMigrateToNamespacedDirsLeavesLocalSkillsAlone(t *testing.T) {
	tmpHome := t.TempDir()
	setTestHome(t, tmpHome)

	writeSkillFile(t, filepath.Join(tmpHome, ".claude", "skills", "my-local-skill", "SKILL.md"), "# local")

	syncState := &SyncState{
		Version: 1,
		Skills: map[string]*SyncEntry{
			"my-local-skill": {
				SkillID: testUUID("skill-456").String(),
				Version: "1.0.0",
				Tool:    "claude-code",
				// No Source — this is a user-created skill
			},
		},
	}

	migrateToNamespacedDirs(syncState)

	// Local skill dir should be untouched
	if _, err := os.Stat(filepath.Join(tmpHome, ".claude", "skills", "my-local-skill")); err != nil {
		t.Errorf("local skill should not be moved: %v", err)
	}
	if _, ok := syncState.Skills["my-local-skill"]; !ok {
		t.Error("local skill sync state key should be unchanged")
	}
}

// TestMirrorRemovesStaleFilesInTarget verifies that mirror performs a true
// replace: files present in the target but absent from the authoritative
// source are deleted, so both copies end up byte-identical.
func TestMirrorRemovesStaleFilesInTarget(t *testing.T) {
	tmpHome := t.TempDir()
	setTestHome(t, tmpHome)

	claudeDir := filepath.Join(tmpHome, ".claude", "skills", "foo")
	cursorDir := filepath.Join(tmpHome, ".cursor", "skills", "foo")

	// Target (cursor) holds the stale marker-matching version. Backdate
	// it so the edit in claude is unambiguously newer regardless of how
	// quickly the filesystem applies mtimes — mirror's 2-group + marker
	// branch now uses newest mtime to disambiguate.
	writeSkillFile(t, filepath.Join(cursorDir, "SKILL.md"), "# old")
	writeSkillFile(t, filepath.Join(cursorDir, "helper.sh"), "#!/bin/sh\n")
	staleTime := time.Now().Add(-1 * time.Hour)
	for _, f := range []string{"SKILL.md", "helper.sh"} {
		if err := os.Chtimes(filepath.Join(cursorDir, f), staleTime, staleTime); err != nil {
			t.Fatalf("chtimes: %v", err)
		}
	}
	// Source (claude) has only SKILL.md — the fresh edit.
	writeSkillFile(t, filepath.Join(claudeDir, "SKILL.md"), "# new")

	markerHash := computeMerkleHash(map[string][]byte{
		"SKILL.md":  []byte("# old"),
		"helper.sh": []byte("#!/bin/sh\n"),
	})
	state := &SyncState{
		Version: 1,
		Skills: map[string]*SyncEntry{
			"foo": {SkillID: testUUID("skill-1").String(), Version: "1.0.0", ContentHash: markerHash, Tool: "claude-code"},
		},
	}

	_, conflicts, _ := mirrorLocalSkills(state)
	if len(conflicts) != 0 {
		t.Fatalf("unexpected conflicts: %+v", conflicts)
	}

	if _, err := os.Stat(filepath.Join(cursorDir, "helper.sh")); !os.IsNotExist(err) {
		t.Errorf("stale helper.sh should have been removed from cursor, err=%v", err)
	}
	cursorSkill, _ := os.ReadFile(filepath.Join(cursorDir, "SKILL.md"))
	if string(cursorSkill) != "# new" {
		t.Errorf("cursor SKILL.md = %q, want '# new'", string(cursorSkill))
	}
}

// TestContextSlug verifies that contextSlug returns the skillset slug for org
// skills and the owner username for personal skills.
func TestContextSlug(t *testing.T) {
	tests := []struct {
		name   string
		source skillSource
		want   string
	}{
		{
			name:   "personal skill uses owner",
			source: skillSource{Owner: "chrismdp", Slug: "retro"},
			want:   "chrismdp",
		},
		{
			name:   "org skillset uses skillset slug",
			source: skillSource{Owner: "acme-corp", Slug: "retro", SkillsetSlug: "acme-dev"},
			want:   "acme-dev",
		},
		{
			name:   "default org skillset uses org slug",
			source: skillSource{Owner: "acme-corp", Slug: "retro", SkillsetSlug: "acme-corp"},
			want:   "acme-corp",
		},
	}
	for _, tt := range tests {
		got := contextSlug(&tt.source)
		if got != tt.want {
			t.Errorf("[%s] contextSlug(%+v) = %q, want %q", tt.name, tt.source, got, tt.want)
		}
	}
}

// TestMigrateToNamespacedDirsOrgSkill verifies that an org-distributed skill
// (with SkillsetSlug set) is renamed to "{skillset-slug}-{slug}", not "{owner}-{slug}".
func TestMigrateToNamespacedDirsOrgSkill(t *testing.T) {
	tmpHome := t.TempDir()
	setTestHome(t, tmpHome)

	// Org skill installed under bare slug before namespacing was introduced.
	for _, dir := range []string{".claude/skills/deploy", ".cursor/skills/deploy"} {
		writeSkillFile(t, filepath.Join(tmpHome, dir, "SKILL.md"), "# deploy skill")
	}

	syncState := &SyncState{
		Version: 1,
		Skills: map[string]*SyncEntry{
			"deploy": {
				SkillID: testUUID("skill-org-1").String(),
				Version: "1.0.0",
				Tool:    "claude-code",
				Source: &skillSource{
					Owner:        "acme-corp",
					Slug:         "deploy",
					ID:           "skill-org-1",
					SkillsetSlug: "acme-dev",
				},
			},
		},
	}

	migrateToNamespacedDirs(syncState)

	// Old bare-slug dirs should be gone.
	if _, err := os.Stat(filepath.Join(tmpHome, ".claude", "skills", "deploy")); !os.IsNotExist(err) {
		t.Error("old .claude/skills/deploy should have been removed")
	}

	// New dir should use skillset slug, not owner slug.
	data, err := os.ReadFile(filepath.Join(tmpHome, ".claude", "skills", "acme-dev-deploy", "SKILL.md"))
	if err != nil {
		t.Fatalf("expected acme-dev-deploy to exist in .claude: %v", err)
	}
	if string(data) != "# deploy skill" {
		t.Errorf("content = %q", string(data))
	}

	// Sync state key should be the skillset-namespaced key.
	if _, ok := syncState.Skills["deploy"]; ok {
		t.Error("old sync state key 'deploy' should have been removed")
	}
	if _, ok := syncState.Skills["acme-dev-deploy"]; !ok {
		t.Error("new sync state key 'acme-dev-deploy' should exist")
	}

	// Must not have created an owner-namespaced key.
	if _, ok := syncState.Skills["acme-corp-deploy"]; ok {
		t.Error("owner-namespaced key 'acme-corp-deploy' should not exist — skillset takes precedence")
	}
}

// resetMirrorHintMemo clears the package-level in-process and warned-slug
// caches so tests don't leak state into each other.
func resetMirrorHintMemo() {
	for k := range mirrorRestoreHintShownSlugs {
		delete(mirrorRestoreHintShownSlugs, k)
	}
	for k := range mirrorWarnedSlugs {
		delete(mirrorWarnedSlugs, k)
	}
}

// TestMirrorHandRmRestoresAndQueuesHint covers the spec's primary
// restore-and-educate case: a slug installed in two agent dirs, the
// user hand-`rm`s one, sync runs mirror — the deleted dir is restored
// (the existing fan-out behaviour) AND a hint fires steering the user
// to airskills rm.
func TestMirrorHandRmRestoresAndQueuesHint(t *testing.T) {
	resetMirrorHintMemo()
	tmpHome := t.TempDir()
	setTestHome(t, tmpHome)

	cursorPath := filepath.Join(tmpHome, ".cursor", "skills", "foo", "SKILL.md")
	writeSkillFile(t, cursorPath, "# content")

	// .claude/skills/ parent exists (detected agent) but the skill dir
	// itself is missing — this is the post-hand-rm state.
	os.MkdirAll(filepath.Join(tmpHome, ".claude", "skills"), 0755)

	markerHash := computeMerkleHash(map[string][]byte{"SKILL.md": []byte("# content")})
	state := &SyncState{
		Version: 1,
		Skills: map[string]*SyncEntry{
			"foo": {SkillID: testUUID("skill-1").String(), Version: "1.0.0", ContentHash: markerHash, Tool: "claude-code"},
		},
	}

	_, conflicts, hints := mirrorLocalSkills(state)
	if len(conflicts) != 0 {
		t.Fatalf("unexpected conflicts: %+v", conflicts)
	}
	if len(hints) != 1 || hints[0].slug != "foo" {
		t.Fatalf("expected one restore hint for foo, got %+v", hints)
	}
	if hints[0].isNonFork {
		t.Errorf("owned-skill marker should not flag isNonFork")
	}

	claudePath := filepath.Join(tmpHome, ".claude", "skills", "foo", "SKILL.md")
	if _, err := os.Stat(claudePath); err != nil {
		t.Errorf("expected claude copy restored: %v", err)
	}

	if !state.Skills["foo"].RestoreHintShown {
		t.Errorf("expected marker.RestoreHintShown=true after first hint")
	}
}

// TestMirrorEditFanOutEmitsNoHint covers the contrasting case: slug in
// two dirs, one edited, mirror fans the edit forward — but the target
// already had content (it wasn't empty), so no restore hint fires.
func TestMirrorEditFanOutEmitsNoHint(t *testing.T) {
	resetMirrorHintMemo()
	tmpHome := t.TempDir()
	setTestHome(t, tmpHome)

	claudePath := filepath.Join(tmpHome, ".claude", "skills", "foo", "SKILL.md")
	cursorPath := filepath.Join(tmpHome, ".cursor", "skills", "foo", "SKILL.md")
	writeSkillFile(t, cursorPath, "# stale")
	staleTime := time.Now().Add(-1 * time.Hour)
	_ = os.Chtimes(cursorPath, staleTime, staleTime)
	writeSkillFile(t, claudePath, "# fresh edit")

	markerHash := computeMerkleHash(map[string][]byte{"SKILL.md": []byte("# stale")})
	state := &SyncState{
		Version: 1,
		Skills: map[string]*SyncEntry{
			"foo": {SkillID: testUUID("skill-1").String(), Version: "1.0.0", ContentHash: markerHash, Tool: "claude-code"},
		},
	}

	_, _, hints := mirrorLocalSkills(state)
	if len(hints) != 0 {
		t.Errorf("expected no hints when fanning edit over stale content, got %+v", hints)
	}
	if state.Skills["foo"].RestoreHintShown {
		t.Errorf("RestoreHintShown must not flip in edit-fan-out case")
	}
}

// TestMirrorComboHandRmAndEditOnSiblingQueuesHint: hand-rm one dir AND
// edit a sibling at the same time. The fresh edit wins by mtime and
// fans into the deleted dir's empty slot — hint should fire.
func TestMirrorComboHandRmAndEditOnSiblingQueuesHint(t *testing.T) {
	resetMirrorHintMemo()
	tmpHome := t.TempDir()
	setTestHome(t, tmpHome)

	cursorPath := filepath.Join(tmpHome, ".cursor", "skills", "foo", "SKILL.md")
	writeSkillFile(t, cursorPath, "# fresh edit on cursor")
	os.MkdirAll(filepath.Join(tmpHome, ".claude", "skills"), 0755)

	// Marker references the pre-edit baseline; cursor is the edit;
	// claude is hand-rm'd (no SKILL.md present).
	markerHash := computeMerkleHash(map[string][]byte{"SKILL.md": []byte("# pre-edit baseline")})
	state := &SyncState{
		Version: 1,
		Skills: map[string]*SyncEntry{
			"foo": {SkillID: testUUID("skill-1").String(), Version: "1.0.0", ContentHash: markerHash, Tool: "claude-code"},
		},
	}

	_, conflicts, hints := mirrorLocalSkills(state)
	if len(conflicts) != 0 {
		t.Fatalf("unexpected conflicts: %+v", conflicts)
	}
	if len(hints) != 1 || hints[0].slug != "foo" {
		t.Fatalf("expected one hint for foo, got %+v", hints)
	}
	claude, err := os.ReadFile(filepath.Join(tmpHome, ".claude", "skills", "foo", "SKILL.md"))
	if err != nil {
		t.Fatalf("expected claude restored: %v", err)
	}
	if string(claude) != "# fresh edit on cursor" {
		t.Errorf("claude got %q, want fresh edit", string(claude))
	}
}

// TestMirrorMarkerPersistedHintMemoisation: the hint fires once, then
// stays quiet on the next mirror run even when the same restore
// condition still holds. After airskills rm drops the marker, a re-add
// + re-hand-rm correctly re-fires.
func TestMirrorMarkerPersistedHintMemoisation(t *testing.T) {
	resetMirrorHintMemo()
	tmpHome := t.TempDir()
	setTestHome(t, tmpHome)

	cursorPath := filepath.Join(tmpHome, ".cursor", "skills", "foo", "SKILL.md")
	writeSkillFile(t, cursorPath, "# content")
	os.MkdirAll(filepath.Join(tmpHome, ".claude", "skills"), 0755)

	markerHash := computeMerkleHash(map[string][]byte{"SKILL.md": []byte("# content")})
	state := &SyncState{
		Version: 1,
		Skills: map[string]*SyncEntry{
			"foo": {SkillID: testUUID("skill-1").String(), Version: "1.0.0", ContentHash: markerHash, Tool: "claude-code"},
		},
	}

	_, _, hints := mirrorLocalSkills(state)
	if len(hints) != 1 {
		t.Fatalf("first run: want 1 hint, got %d", len(hints))
	}

	// Simulate the second sync: hand-rm again (claude exists from the
	// restore, so re-rm it to recreate the empty-target condition).
	os.RemoveAll(filepath.Join(tmpHome, ".claude", "skills", "foo"))
	_, _, hints = mirrorLocalSkills(state)
	if len(hints) != 0 {
		t.Errorf("second run: want 0 hints (marker memo), got %d: %+v", len(hints), hints)
	}

	// User runs `airskills rm foo` — marker dropped. Re-add + re-hand-rm.
	delete(state.Skills, "foo")
	state.Skills["foo"] = &SyncEntry{SkillID: testUUID("skill-1").String(), Version: "1.0.0", ContentHash: markerHash, Tool: "claude-code"}
	os.RemoveAll(filepath.Join(tmpHome, ".claude", "skills", "foo"))
	_, _, hints = mirrorLocalSkills(state)
	if len(hints) != 1 {
		t.Errorf("post-rm re-add: want 1 hint again, got %d", len(hints))
	}
}

// TestMirrorInProcessHintMemoisationForUntrackedSlugs: hand-created
// slug with no marker — the in-process map memoises so the hint fires
// once per process even though there's nothing to persist.
func TestMirrorInProcessHintMemoisationForUntrackedSlugs(t *testing.T) {
	resetMirrorHintMemo()
	tmpHome := t.TempDir()
	setTestHome(t, tmpHome)

	cursorPath := filepath.Join(tmpHome, ".cursor", "skills", "bar", "SKILL.md")
	writeSkillFile(t, cursorPath, "# content")
	os.MkdirAll(filepath.Join(tmpHome, ".claude", "skills"), 0755)

	state := &SyncState{Version: 1, Skills: map[string]*SyncEntry{}}

	_, _, hints := mirrorLocalSkills(state)
	if len(hints) != 1 || hints[0].slug != "bar" {
		t.Fatalf("first run: want 1 hint for bar, got %+v", hints)
	}

	// Re-run without re-rming — second run sees the previously-empty
	// dir is now filled, so it won't trigger again regardless of memo.
	// Force the empty-target condition again to test the memo only.
	os.RemoveAll(filepath.Join(tmpHome, ".claude", "skills", "bar"))
	_, _, hints = mirrorLocalSkills(state)
	if len(hints) != 0 {
		t.Errorf("second run: in-process memo should suppress, got %+v", hints)
	}
}

// TestMirrorNonForkRestoreHintUsesKeepRemote: a marker whose SkillID
// matches its Source.ID (plain `airskills add`, never forked) should
// yield a hint with isNonFork=true so the printer routes to
// `--keep-remote` wording.
func TestMirrorNonForkRestoreHintUsesKeepRemote(t *testing.T) {
	resetMirrorHintMemo()
	tmpHome := t.TempDir()
	setTestHome(t, tmpHome)

	cursorPath := filepath.Join(tmpHome, ".cursor", "skills", "upstream-foo", "SKILL.md")
	writeSkillFile(t, cursorPath, "# upstream")
	os.MkdirAll(filepath.Join(tmpHome, ".claude", "skills"), 0755)

	upstreamID := testUUID("upstream-foo").String()
	markerHash := computeMerkleHash(map[string][]byte{"SKILL.md": []byte("# upstream")})
	state := &SyncState{
		Version: 1,
		Skills: map[string]*SyncEntry{
			"upstream-foo": {
				SkillID:     upstreamID,
				Version:     "1.0.0",
				ContentHash: markerHash,
				Tool:        "claude-code",
				Source: &skillSource{
					Owner: "alice",
					Slug:  "foo",
					ID:    upstreamID,
				},
			},
		},
	}

	_, _, hints := mirrorLocalSkills(state)
	if len(hints) != 1 {
		t.Fatalf("want 1 hint, got %d", len(hints))
	}
	if !hints[0].isNonFork {
		t.Errorf("non-fork sourced skill should set isNonFork=true")
	}
}

// TestDecidePullActionsReturnsDivergedSlugs: when a tracked skill has
// both sides changed (diverged) or an untracked local conflict, the
// slug name appears in the third return value so callers can register
// it in the shared sync conflict set BEFORE the download loop runs.
func TestDecidePullActionsReturnsDivergedSlugs(t *testing.T) {
	tmpHome := t.TempDir()
	setTestHome(t, tmpHome)

	divergedPath := filepath.Join(tmpHome, ".claude", "skills", "diverged-skill")
	writeSkillFile(t, filepath.Join(divergedPath, "SKILL.md"), "# local edit")

	untrackedPath := filepath.Join(tmpHome, ".claude", "skills", "untracked-skill")
	writeSkillFile(t, filepath.Join(untrackedPath, "SKILL.md"), "# local untracked")

	local := map[string]string{
		"diverged-skill":  divergedPath,
		"untracked-skill": untrackedPath,
	}

	divergedID := testUUID("diverged-skill").String()
	untrackedID := testUUID("untracked-skill").String()
	remoteDivergedHash := "remote-diverged-hash"
	remoteUntrackedHash := "remote-untracked-hash"
	remote := []apiSkill{
		{Id: testUUID("diverged-skill"), Name: "diverged-skill", Version: "2", ContentHash: &remoteDivergedHash},
		{Id: testUUID("untracked-skill"), Name: "untracked-skill", Version: "1", ContentHash: &remoteUntrackedHash},
	}

	state := &SyncState{
		Version: 1,
		Skills: map[string]*SyncEntry{
			"diverged-skill": {SkillID: divergedID, Version: "1", ContentHash: "original-marker-hash", Tool: "claude-code"},
		},
	}
	_ = untrackedID

	_, _, divergedSlugs := decidePullActions(remote, local, state)
	wantSet := map[string]bool{"diverged-skill": true, "untracked-skill": true}
	gotSet := map[string]bool{}
	for _, s := range divergedSlugs {
		gotSet[s] = true
	}
	for k := range wantSet {
		if !gotSet[k] {
			t.Errorf("divergedSlugs missing %q (got %v)", k, divergedSlugs)
		}
	}
}
