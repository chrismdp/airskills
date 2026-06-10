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
		"SKILL.md":  []byte("# Test\nHello"),
		"run.sh":    []byte("#!/bin/bash\necho hi"),
		"helper.py": []byte("#!/usr/bin/env python3\nprint('hi')"),
		"data.txt":  []byte("plain data"),
		"README.md": []byte("# Plain markdown, no shebang"),
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

	// Ledger baseline "# old" for both copies; the project copy has moved.
	state := &SyncState{
		Version: 1,
		Skills:  map[string]*SyncEntry{"foo": ledgerEntry(hash1("# old"), filepath.Dir(claudePath), filepath.Dir(projectPath))},
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

	// Baseline "# old" in the first-found dir (.claude); the edit lives in a
	// later dir. The ledger knows both copies at the baseline.
	writeSkillFile(t, claudePath, "# old")
	writeSkillFile(t, cursorPath, "# edited")

	state := &SyncState{
		Version: 1,
		Skills:  map[string]*SyncEntry{"foo": perCopyMarker(tmpHome, "foo", hash1("# old"), ".claude", ".cursor")},
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

// TestMirrorStaleSecondaryCopyLosesToFreshPrimaryEdit: a previous mirror
// converged both copies and recorded that baseline; the user has since edited
// one copy in place. The per-copy ledger shows exactly one copy moved, so the
// fresh edit wins and the stale secondary is brought forward — deterministically,
// with no reliance on file mtime.
func TestMirrorStaleSecondaryCopyLosesToFreshPrimaryEdit(t *testing.T) {
	tmpHome := t.TempDir()
	setTestHome(t, tmpHome)

	claudePath := filepath.Join(tmpHome, ".claude", "skills", "foo", "SKILL.md")
	cursorPath := filepath.Join(tmpHome, ".cursor", "skills", "foo", "SKILL.md")

	// Both copies were last reconciled to this content (ledger baseline).
	writeSkillFile(t, cursorPath, "# stale mirror from last run")
	writeSkillFile(t, claudePath, "# fresh user edit") // claude edited since

	state := &SyncState{
		Version: 1,
		Skills:  map[string]*SyncEntry{"foo": perCopyMarker(tmpHome, "foo", hash1("# stale mirror from last run"), ".claude", ".cursor")},
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

// TestMirrorDoesNotRevertEditAfterPushAdvancedMarker guards the bug in
// cli-mirror-overwrites-edit-after-push: an edit in one copy must never be
// reverted by a stale sibling. The per-copy ledger records the pre-edit
// baseline for both copies; claude has moved off it, cursor has not, so the
// edit is authoritative and propagates — no mtime, no revert.
func TestMirrorDoesNotRevertEditAfterPushAdvancedMarker(t *testing.T) {
	tmpHome := t.TempDir()
	setTestHome(t, tmpHome)

	claudePath := filepath.Join(tmpHome, ".claude", "skills", "foo", "SKILL.md")
	cursorPath := filepath.Join(tmpHome, ".cursor", "skills", "foo", "SKILL.md")

	// Both copies were last reconciled at "# old" (ledger baseline).
	writeSkillFile(t, cursorPath, "# old")    // stale sibling, unchanged
	writeSkillFile(t, claudePath, "# edited") // the user's edit

	state := &SyncState{
		Version: 1,
		Skills:  map[string]*SyncEntry{"foo": perCopyMarker(tmpHome, "foo", hash1("# old"), ".claude", ".cursor")},
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

// TestMirrorPreservesEditToNonSkillMdFile guards a real production bug class:
// an edit that touches only a non-SKILL.md file (a script, a reference) must
// still win and propagate. The per-copy ledger compares whole-skill content,
// not SKILL.md timestamps, so the copy whose content moved off its baseline is
// authoritative regardless of which file changed.
func TestMirrorPreservesEditToNonSkillMdFile(t *testing.T) {
	tmpHome := t.TempDir()
	setTestHome(t, tmpHome)

	claudeDir := filepath.Join(tmpHome, ".claude", "skills", "foo")
	cursorDir := filepath.Join(tmpHome, ".cursor", "skills", "foo")

	// Both copies were last reconciled with the "echo old" script (the ledger
	// baseline). The edit lives in a non-SKILL.md file in claude, leaving
	// SKILL.md byte-identical across copies — the case the pre-fix mtime code
	// got wrong by only looking at SKILL.md timestamps.
	baseFiles := map[string][]byte{
		"SKILL.md":      []byte("# foo\n"),
		"scripts/do.sh": []byte("#!/bin/sh\necho old\n"),
	}
	baseHash := computeMerkleHash(baseFiles)

	writeSkillFile(t, filepath.Join(claudeDir, "SKILL.md"), "# foo\n")
	writeSkillFile(t, filepath.Join(claudeDir, "scripts", "do.sh"), "#!/bin/sh\necho edited\n") // edit
	writeSkillFile(t, filepath.Join(cursorDir, "SKILL.md"), "# foo\n")
	writeSkillFile(t, filepath.Join(cursorDir, "scripts", "do.sh"), "#!/bin/sh\necho old\n") // baseline

	state := &SyncState{
		Version: 1,
		Skills:  map[string]*SyncEntry{"foo": perCopyMarker(tmpHome, "foo", baseHash, ".claude", ".cursor")},
	}

	_, conflicts, _ := mirrorLocalSkills(state)
	if len(conflicts) != 0 {
		t.Fatalf("unexpected conflicts: %+v", conflicts)
	}

	// The edit must survive in both dirs.
	got, _ := os.ReadFile(filepath.Join(claudeDir, "scripts", "do.sh"))
	if string(got) != "#!/bin/sh\necho edited\n" {
		t.Errorf("claude script reverted to %q — mirror overwrote the edit", string(got))
	}
	cursorScript, _ := os.ReadFile(filepath.Join(cursorDir, "scripts", "do.sh"))
	if string(cursorScript) != "#!/bin/sh\necho edited\n" {
		t.Errorf("cursor script = %q, want edit propagated from claude", string(cursorScript))
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

	// cursor holds the baseline version (SKILL.md + helper.sh); claude has the
	// fresh edit that drops helper.sh. Ledger baseline = the two-file version.
	writeSkillFile(t, filepath.Join(cursorDir, "SKILL.md"), "# old")
	writeSkillFile(t, filepath.Join(cursorDir, "helper.sh"), "#!/bin/sh\n")
	writeSkillFile(t, filepath.Join(claudeDir, "SKILL.md"), "# new")

	baseHash := computeMerkleHash(map[string][]byte{
		"SKILL.md":  []byte("# old"),
		"helper.sh": []byte("#!/bin/sh\n"),
	})
	state := &SyncState{
		Version: 1,
		Skills:  map[string]*SyncEntry{"foo": ledgerEntry(baseHash, claudeDir, cursorDir)},
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

// resetMirrorHintMemo clears the package-level warned-slug cache so
// tests don't leak state into each other. Kept as a single hook even
// though mirror no longer memoises restore hints (the cache for
// mirrorConflict warnings still matters across tests).
func resetMirrorHintMemo() {
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
	writeSkillFile(t, cursorPath, "# stale") // ledger baseline
	writeSkillFile(t, claudePath, "# fresh edit")

	state := &SyncState{
		Version: 1,
		Skills:  map[string]*SyncEntry{"foo": perCopyMarker(tmpHome, "foo", hash1("# stale"), ".claude", ".cursor")},
	}

	_, conflicts, hints := mirrorLocalSkills(state)
	if len(conflicts) != 0 {
		t.Fatalf("unexpected conflicts: %+v", conflicts)
	}
	if len(hints) != 0 {
		t.Errorf("expected no hints when fanning edit over stale content, got %+v", hints)
	}
}

// TestMirrorComboHandRmAndEditOnSiblingQueuesHint: hand-rm one dir AND
// edit a sibling at the same time. The fresh edit is the only remaining copy,
// so it fans into the deleted dir's empty slot — hint should fire.
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

// TestMirrorRestoreHintFiresEveryTime: the hint is deliberately not
// memoised. Each hand-`rm`-then-sync cycle re-emits it. Repeated
// reminders are the price of the simpler model — the hint stays
// noisy until the user runs `airskills rm`, which removes both
// copies and drops the marker.
func TestMirrorRestoreHintFiresEveryTime(t *testing.T) {
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

	// Hand-`rm` again — hint should re-fire, not be suppressed.
	os.RemoveAll(filepath.Join(tmpHome, ".claude", "skills", "foo"))
	_, _, hints = mirrorLocalSkills(state)
	if len(hints) != 1 {
		t.Errorf("second run: want 1 hint (no memoisation), got %d", len(hints))
	}
}

// TestMirrorRestoreHintFiresForUntrackedSlug: hand-created slugs (no
// marker) get the same treatment as tracked ones. Untracked-slug
// hint has isNonFork=false (markerIsNonFork returns false for nil
// marker).
func TestMirrorRestoreHintFiresForUntrackedSlug(t *testing.T) {
	resetMirrorHintMemo()
	tmpHome := t.TempDir()
	setTestHome(t, tmpHome)

	cursorPath := filepath.Join(tmpHome, ".cursor", "skills", "bar", "SKILL.md")
	writeSkillFile(t, cursorPath, "# content")
	os.MkdirAll(filepath.Join(tmpHome, ".claude", "skills"), 0755)

	state := &SyncState{Version: 1, Skills: map[string]*SyncEntry{}}

	_, _, hints := mirrorLocalSkills(state)
	if len(hints) != 1 || hints[0].slug != "bar" {
		t.Fatalf("want 1 hint for bar, got %+v", hints)
	}
	if hints[0].isNonFork {
		t.Errorf("untracked slug (nil marker) should not flag isNonFork")
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

	_, _, divergedSlugs := decidePullActions(remote, local, state, nil)
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

// --- Per-copy baseline divergence (cli-per-copy-skill-divergence.md) ---

// hash1 is the Merkle hash of a single-SKILL.md skill with the given body.
func hash1(body string) string {
	return computeMerkleHash(map[string][]byte{"SKILL.md": []byte(body)})
}

// perCopyMarker builds a tracked marker whose per-copy ledger records the
// given baseline hash for each skill-dir under tmpHome.
func perCopyMarker(tmpHome, slug, baseHash string, agentSubdirs ...string) *SyncEntry {
	dirs := make([]string, len(agentSubdirs))
	for i, sub := range agentSubdirs {
		dirs[i] = filepath.Join(tmpHome, sub, "skills", slug)
	}
	return ledgerEntry(baseHash, dirs...)
}

// ledgerEntry builds a tracked marker whose per-copy ledger records baseHash
// for each given absolute skill directory. Use when the copies don't all sit
// under the standard ~/<agent>/skills/<slug> layout (e.g. a project dir).
func ledgerEntry(baseHash string, skillDirs ...string) *SyncEntry {
	copies := map[string]CopyState{}
	for _, d := range skillDirs {
		copies[d] = CopyState{Hash: baseHash}
	}
	return &SyncEntry{
		SkillID:     testUUID("skill-1").String(),
		Version:     "1.0.0",
		ContentHash: baseHash,
		Tool:        "claude-code",
		Copies:      copies,
	}
}

// TestMirrorPerCopyBaselineSingleEditWinsOverNewerSibling is the reported bug:
// one copy is edited, the other still matches its baseline, and the UNEDITED
// sibling has the newer file mtime. The legacy mtime heuristic would pick the
// marker-matching (unedited) copy and silently revert the edit — which is what
// made `push` report "all unchanged" after the mirror clobbered the change.
// With per-copy baselines the edit is unambiguous and must win.
func TestMirrorPerCopyBaselineSingleEditWinsOverNewerSibling(t *testing.T) {
	tmpHome := t.TempDir()
	setTestHome(t, tmpHome)

	claudePath := filepath.Join(tmpHome, ".claude", "skills", "foo", "SKILL.md")
	cursorPath := filepath.Join(tmpHome, ".cursor", "skills", "foo", "SKILL.md")

	// claude holds the user's edit; cursor still holds the baseline but has a
	// NEWER mtime (e.g. it was touched by a prior mirror/pull).
	writeSkillFile(t, claudePath, "# edited")
	editTime := time.Now().Add(-1 * time.Hour)
	if err := os.Chtimes(claudePath, editTime, editTime); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	writeSkillFile(t, cursorPath, "# base")

	state := &SyncState{
		Version: 1,
		Skills:  map[string]*SyncEntry{"foo": perCopyMarker(tmpHome, "foo", hash1("# base"), ".claude", ".cursor")},
	}

	_, conflicts, _ := mirrorLocalSkills(state)
	if len(conflicts) != 0 {
		t.Fatalf("unexpected conflicts: %+v", conflicts)
	}

	claude, _ := os.ReadFile(claudePath)
	if string(claude) != "# edited" {
		t.Errorf("claude copy = %q, want '# edited' (mtime clobbered the edit)", string(claude))
	}
	cursor, _ := os.ReadFile(cursorPath)
	if string(cursor) != "# edited" {
		t.Errorf("cursor copy = %q, want '# edited' (edit not fanned out)", string(cursor))
	}
}

// TestMirrorPerCopyBaselineDualEditDifferentIsConflict: both copies edited to
// DIFFERENT content from their shared baseline. That is a genuine local fork —
// the mirror must report it and leave both copies untouched, never flatten one.
func TestMirrorPerCopyBaselineDualEditDifferentIsConflict(t *testing.T) {
	tmpHome := t.TempDir()
	setTestHome(t, tmpHome)

	claudePath := filepath.Join(tmpHome, ".claude", "skills", "foo", "SKILL.md")
	cursorPath := filepath.Join(tmpHome, ".cursor", "skills", "foo", "SKILL.md")
	writeSkillFile(t, claudePath, "# edited in claude")
	writeSkillFile(t, cursorPath, "# edited in cursor")

	state := &SyncState{
		Version: 1,
		Skills:  map[string]*SyncEntry{"foo": perCopyMarker(tmpHome, "foo", hash1("# base"), ".claude", ".cursor")},
	}

	_, conflicts, _ := mirrorLocalSkills(state)
	if len(conflicts) != 1 || conflicts[0].slug != "foo" {
		t.Fatalf("want a single conflict for foo, got %+v", conflicts)
	}

	// Neither copy may be overwritten by the other.
	claude, _ := os.ReadFile(claudePath)
	if string(claude) != "# edited in claude" {
		t.Errorf("claude copy = %q, want untouched", string(claude))
	}
	cursor, _ := os.ReadFile(cursorPath)
	if string(cursor) != "# edited in cursor" {
		t.Errorf("cursor copy = %q, want untouched", string(cursor))
	}
}

// TestMirrorPerCopyBaselineDualEditSameConverges: both copies edited to the
// SAME new content. Not a fork — they already agree, so converge and push.
func TestMirrorPerCopyBaselineDualEditSameConverges(t *testing.T) {
	tmpHome := t.TempDir()
	setTestHome(t, tmpHome)

	claudePath := filepath.Join(tmpHome, ".claude", "skills", "foo", "SKILL.md")
	cursorPath := filepath.Join(tmpHome, ".cursor", "skills", "foo", "SKILL.md")
	writeSkillFile(t, claudePath, "# both edited the same")
	writeSkillFile(t, cursorPath, "# both edited the same")

	state := &SyncState{
		Version: 1,
		Skills:  map[string]*SyncEntry{"foo": perCopyMarker(tmpHome, "foo", hash1("# base"), ".claude", ".cursor")},
	}

	_, conflicts, _ := mirrorLocalSkills(state)
	if len(conflicts) != 0 {
		t.Fatalf("unexpected conflicts: %+v", conflicts)
	}
	claude, _ := os.ReadFile(claudePath)
	if string(claude) != "# both edited the same" {
		t.Errorf("claude copy = %q", string(claude))
	}
}

// TestMirrorRecordsPerCopyBaselines: after a clean (non-conflict) pass the
// ledger is stamped with the authoritative hash for every present copy, so the
// next run can tell which copy moved.
func TestMirrorRecordsPerCopyBaselines(t *testing.T) {
	tmpHome := t.TempDir()
	setTestHome(t, tmpHome)

	claudePath := filepath.Join(tmpHome, ".claude", "skills", "foo", "SKILL.md")
	cursorPath := filepath.Join(tmpHome, ".cursor", "skills", "foo", "SKILL.md")
	writeSkillFile(t, claudePath, "# v1")
	writeSkillFile(t, cursorPath, "# v1")

	entry := &SyncEntry{SkillID: testUUID("skill-1").String(), Version: "1.0.0", ContentHash: hash1("# v1"), Tool: "claude-code"}
	state := &SyncState{Version: 1, Skills: map[string]*SyncEntry{"foo": entry}}

	if _, conflicts, _ := mirrorLocalSkills(state); len(conflicts) != 0 {
		t.Fatalf("unexpected conflicts: %+v", conflicts)
	}

	want := hash1("# v1")
	for _, sub := range []string{".claude", ".cursor"} {
		key := filepath.Join(tmpHome, sub, "skills", "foo")
		cs, ok := entry.Copies[key]
		if !ok {
			t.Errorf("ledger missing baseline for %s", key)
			continue
		}
		if cs.Hash != want {
			t.Errorf("baseline[%s].Hash = %q, want %q", key, cs.Hash, want)
		}
	}
}

// TestMirrorExistingMirrorsSeedLedgerThenResolve answers the migration
// question: a user who ALREADY has a skill mirrored across two agent dirs
// upgrades to per-copy baselines. Their marker has a ContentHash but no
// `copies` ledger. First run must not disrupt them — identical copies seed the
// ledger with zero conflicts and zero file changes — and a subsequent edit then
// resolves deterministically off that freshly-seeded ledger.
func TestMirrorExistingMirrorsSeedLedgerThenResolve(t *testing.T) {
	tmpHome := t.TempDir()
	setTestHome(t, tmpHome)

	claudePath := filepath.Join(tmpHome, ".claude", "skills", "foo", "SKILL.md")
	cursorPath := filepath.Join(tmpHome, ".cursor", "skills", "foo", "SKILL.md")
	writeSkillFile(t, claudePath, "# v0")
	writeSkillFile(t, cursorPath, "# v0")

	// Legacy marker: ContentHash set, NO Copies ledger (pre-upgrade state).
	entry := &SyncEntry{SkillID: testUUID("skill-1").String(), Version: "1.0.0", ContentHash: hash1("# v0"), Tool: "claude-code"}
	state := &SyncState{Version: 1, Skills: map[string]*SyncEntry{"foo": entry}}

	// First run: no conflict, no rewrites, ledger seeded for both copies.
	changes, conflicts, _ := mirrorLocalSkills(state)
	if len(conflicts) != 0 {
		t.Fatalf("first run should not conflict for already-synced mirrors: %+v", conflicts)
	}
	for _, c := range changes {
		if len(c.written) != 0 {
			t.Errorf("first run rewrote files for identical copies: %+v", c.written)
		}
	}
	if len(entry.Copies) != 2 {
		t.Fatalf("ledger not seeded: %+v", entry.Copies)
	}

	// Now the user edits one copy. The seeded ledger makes the edit
	// unambiguous — it wins and fans out, no mtime involved.
	writeSkillFile(t, claudePath, "# v1")
	if _, conflicts, _ := mirrorLocalSkills(state); len(conflicts) != 0 {
		t.Fatalf("edit after seeding should resolve, not conflict: %+v", conflicts)
	}
	cursor, _ := os.ReadFile(cursorPath)
	if string(cursor) != "# v1" {
		t.Errorf("cursor = %q, want '# v1' (edit not propagated post-seed)", string(cursor))
	}
}

// TestMirrorColdStartAdvancedMarkerDoesNotClobber locks the data-loss bug a
// code review caught: on the FIRST run after upgrade (no ledger yet) where the
// marker's ContentHash was optimistically advanced to the EDIT (so it matches
// the edited copy, not the pre-edit baseline), the old cold-start logic would
// have treated the stale sibling as "the edit" and reverted the real edit.
// With ledger-only resolution this is surfaced as a conflict and NOTHING is
// overwritten.
func TestMirrorColdStartAdvancedMarkerDoesNotClobber(t *testing.T) {
	tmpHome := t.TempDir()
	setTestHome(t, tmpHome)

	claudePath := filepath.Join(tmpHome, ".claude", "skills", "foo", "SKILL.md")
	cursorPath := filepath.Join(tmpHome, ".cursor", "skills", "foo", "SKILL.md")
	writeSkillFile(t, claudePath, "# edited") // the real edit
	writeSkillFile(t, cursorPath, "# old")    // stale sibling

	// Marker advanced to the edit hash; NO per-copy ledger (legacy state).
	state := &SyncState{
		Version: 1,
		Skills: map[string]*SyncEntry{
			"foo": {SkillID: testUUID("skill-1").String(), Version: "1.0.0", ContentHash: hash1("# edited"), Tool: "claude-code"},
		},
	}

	_, conflicts, _ := mirrorLocalSkills(state)
	if len(conflicts) != 1 {
		t.Fatalf("want a conflict (no trustworthy history), got %+v", conflicts)
	}
	// Crucially, the edit must be untouched — never reverted to the stale copy.
	claude, _ := os.ReadFile(claudePath)
	if string(claude) != "# edited" {
		t.Errorf("claude = %q, want '# edited' — the edit was clobbered", string(claude))
	}
	cursor, _ := os.ReadFile(cursorPath)
	if string(cursor) != "# old" {
		t.Errorf("cursor = %q, want '# old' untouched", string(cursor))
	}
}

// TestMirrorPartialLedgerNewAgentDirConflicts locks the partial-ledger finding:
// a freshly-installed agent dir holding DIFFERENT content for a skill that is
// otherwise in sync must not let an incomplete ledger silently flatten things.
// The two synced copies are left intact and the rogue copy is surfaced, not
// propagated over them.
func TestMirrorPartialLedgerNewAgentDirConflicts(t *testing.T) {
	tmpHome := t.TempDir()
	setTestHome(t, tmpHome)

	claudePath := filepath.Join(tmpHome, ".claude", "skills", "foo", "SKILL.md")
	cursorPath := filepath.Join(tmpHome, ".cursor", "skills", "foo", "SKILL.md")
	windsurfPath := filepath.Join(tmpHome, ".codeium", "windsurf", "skills", "foo", "SKILL.md")
	writeSkillFile(t, claudePath, "# synced")
	writeSkillFile(t, cursorPath, "# synced")
	writeSkillFile(t, windsurfPath, "# rogue new-agent content")

	// Ledger knows only the two long-standing copies, not the new windsurf dir.
	state := &SyncState{
		Version: 1,
		Skills:  map[string]*SyncEntry{"foo": perCopyMarker(tmpHome, "foo", hash1("# synced"), ".claude", ".cursor")},
	}

	_, conflicts, _ := mirrorLocalSkills(state)
	if len(conflicts) != 1 {
		t.Fatalf("want a conflict for the incomplete ledger, got %+v", conflicts)
	}
	// Nothing overwritten in either direction.
	for path, want := range map[string]string{claudePath: "# synced", cursorPath: "# synced", windsurfPath: "# rogue new-agent content"} {
		got, _ := os.ReadFile(path)
		if string(got) != want {
			t.Errorf("%s = %q, want %q (no copy should be flattened)", path, string(got), want)
		}
	}
}

// TestMirrorUntrackedDivergentCopiesConflict: a never-synced skill (no marker)
// present with different content in two dirs has no baseline at all, so the
// mirror cannot know which is the edit. It surfaces a conflict rather than
// guess by mtime.
func TestMirrorUntrackedDivergentCopiesConflict(t *testing.T) {
	tmpHome := t.TempDir()
	setTestHome(t, tmpHome)

	claudePath := filepath.Join(tmpHome, ".claude", "skills", "foo", "SKILL.md")
	cursorPath := filepath.Join(tmpHome, ".cursor", "skills", "foo", "SKILL.md")
	writeSkillFile(t, claudePath, "# version a")
	writeSkillFile(t, cursorPath, "# version b")

	state := &SyncState{Version: 1, Skills: map[string]*SyncEntry{}} // untracked

	_, conflicts, _ := mirrorLocalSkills(state)
	if len(conflicts) != 1 {
		t.Fatalf("want a conflict for untracked divergent copies, got %+v", conflicts)
	}
	claude, _ := os.ReadFile(claudePath)
	cursor, _ := os.ReadFile(cursorPath)
	if string(claude) != "# version a" || string(cursor) != "# version b" {
		t.Errorf("copies must be untouched: claude=%q cursor=%q", string(claude), string(cursor))
	}
}
