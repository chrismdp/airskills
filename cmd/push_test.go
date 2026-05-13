package cmd

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chrismdp/airskills/config"
	"github.com/spf13/cobra"
)

func TestCreateTarGz(t *testing.T) {
	// Create a temp skill directory
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "test-skill")
	os.MkdirAll(skillDir, 0755)
	os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("# Test skill\nHello"), 0644)
	os.WriteFile(filepath.Join(skillDir, "helper.sh"), []byte("#!/bin/bash\necho hi"), 0755)
	os.MkdirAll(filepath.Join(skillDir, "scripts"), 0755)
	os.WriteFile(filepath.Join(skillDir, "scripts", "run.py"), []byte("print('hi')"), 0644)
	// Marker should be excluded
	os.WriteFile(filepath.Join(skillDir, ".airskills"), []byte(`{"skill_id":"x"}`), 0644)

	// Universal noise should be excluded
	os.MkdirAll(filepath.Join(skillDir, "scripts", "__pycache__"), 0755)
	os.WriteFile(filepath.Join(skillDir, "scripts", "__pycache__", "run.cpython-312.pyc"), []byte("noise"), 0644)
	os.WriteFile(filepath.Join(skillDir, ".DS_Store"), []byte("noise"), 0644)

	data, err := createTarGz(skillDir)
	if err != nil {
		t.Fatalf("createTarGz failed: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("archive is empty")
	}

	// Read back and verify contents
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("gzip.NewReader: %v", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	files := map[string]bool{}
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("tar read error: %v", err)
		}
		files[header.Name] = true
	}

	if !files["test-skill/SKILL.md"] {
		t.Error("missing SKILL.md in archive")
	}
	if !files["test-skill/helper.sh"] {
		t.Error("missing helper.sh in archive")
	}
	if !files["test-skill/scripts/run.py"] {
		t.Error("missing scripts/run.py in archive")
	}
	if files["test-skill/.airskills"] {
		t.Error(".airskills marker should be excluded from archive")
	}
	if files["test-skill/scripts/__pycache__/run.cpython-312.pyc"] {
		t.Error("__pycache__ contents should be excluded from archive")
	}
	if files["test-skill/.DS_Store"] {
		t.Error(".DS_Store should be excluded from archive")
	}
}

func TestReadSkillFilesIgnoresNoise(t *testing.T) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "noisy-skill")
	os.MkdirAll(skillDir, 0755)
	os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("# x"), 0644)
	os.MkdirAll(filepath.Join(skillDir, "scripts", "__pycache__"), 0755)
	os.WriteFile(filepath.Join(skillDir, "scripts", "__pycache__", "run.cpython-312.pyc"), []byte("noise"), 0644)
	os.WriteFile(filepath.Join(skillDir, "scripts", "run.py"), []byte("print('hi')"), 0644)
	os.WriteFile(filepath.Join(skillDir, ".DS_Store"), []byte("noise"), 0644)

	got := readSkillFiles(skillDir)

	if _, ok := got["SKILL.md"]; !ok {
		t.Error("SKILL.md missing")
	}
	if _, ok := got["scripts/run.py"]; !ok {
		t.Error("scripts/run.py missing")
	}
	for k := range got {
		if filepath.Ext(k) == ".pyc" {
			t.Errorf("expected .pyc to be ignored, got %q", k)
		}
		if filepath.Base(k) == ".DS_Store" {
			t.Errorf("expected .DS_Store to be ignored, got %q", k)
		}
	}
}

func TestExtractTarGzToMap(t *testing.T) {
	srcDir := t.TempDir()
	skillDir := filepath.Join(srcDir, "my-skill")
	os.MkdirAll(skillDir, 0755)
	os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("# My skill"), 0644)
	os.WriteFile(filepath.Join(skillDir, "data.json"), []byte(`{"key":"value"}`), 0644)

	data, err := createTarGz(skillDir)
	if err != nil {
		t.Fatalf("createTarGz: %v", err)
	}

	files, err := extractTarGzToMap(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("extractTarGzToMap: %v", err)
	}

	if string(files["SKILL.md"]) != "# My skill" {
		t.Errorf("SKILL.md = %q, want %q", string(files["SKILL.md"]), "# My skill")
	}
	if string(files["data.json"]) != `{"key":"value"}` {
		t.Errorf("data.json = %q", string(files["data.json"]))
	}
}

func TestPushSuppressesConflictBlockInsideSync(t *testing.T) {
	output := runPushConflictScenario(t, "sync", nil)

	if strings.Contains(output, "Conflict: borrowed") {
		t.Fatalf("push inside sync should leave conflict block emission to pull, got:\n%s", output)
	}
}

func TestPushStandaloneConflictIncludesSourcedCaveat(t *testing.T) {
	output := runPushConflictScenario(t, "push", &skillSource{Owner: "alice", Slug: "borrowed-original"})

	if !strings.Contains(output, "Conflict: borrowed") {
		t.Fatalf("standalone push should show the conflict block, got:\n%s", output)
	}
	if !strings.Contains(output, "sourced from alice/borrowed-original") {
		t.Fatalf("standalone push should include sourced-skill caveat, got:\n%s", output)
	}
}

func runPushConflictScenario(t *testing.T, cmdName string, source *skillSource) string {
	t.Helper()

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	oldIsTTY := isTTY
	isTTY = false
	t.Cleanup(func() { isTTY = oldIsTTY })

	skillID := "11111111-1111-1111-1111-111111111111"
	skillDir := filepath.Join(home, ".claude", "skills", "borrowed")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: borrowed\ndescription: test\n---\n\nlocal body\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	state := &SyncState{Version: 1, Skills: map[string]*SyncEntry{
		"borrowed": {
			SkillID:     skillID,
			Version:     "1.0.0",
			ContentHash: "old-marker-hash",
			Tool:        "claude-code",
			Source:      source,
		},
	}}
	if err := saveSyncState(state); err != nil {
		t.Fatalf("save sync state: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/skills":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"skills":[{"id":%q,"name":"borrowed","slug":"borrowed","version":"1.0.0","content_hash":"server-hash","tool_formats":["claude-code"],"visibility":"private","dependency_count":0}]}`, skillID)
		case r.Method == http.MethodPut && r.URL.Path == "/api/v1/skills/"+skillID+"/archive":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			fmt.Fprint(w, `{"remote_content_hash":"server-hash"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/skills/"+skillID+"/raw":
			w.Header().Set("Content-Type", "text/plain")
			fmt.Fprint(w, "---\nname: borrowed\ndescription: test\n---\n\nremote body\n")
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	}))
	defer srv.Close()

	writeTestConfigAndToken(t, home, srv.URL)

	cmd := &cobra.Command{Use: cmdName}
	return captureStdout(t, func() {
		if err := pushCmd.RunE(cmd, nil); err != nil {
			t.Fatalf("push RunE: %v", err)
		}
	})
}

func writeTestConfigAndToken(t *testing.T, home, apiURL string) {
	t.Helper()

	cfgDir := filepath.Join(home, ".config", "airskills")
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatal(err)
	}
	cfgData, err := json.Marshal(config.Config{APIURL: apiURL})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "config.json"), cfgData, 0o600); err != nil {
		t.Fatal(err)
	}
	tokenData, err := json.Marshal(config.TokenData{
		AccessToken:  "test-token",
		RefreshToken: "refresh-token",
		ExpiresAt:    time.Now().Add(time.Hour).Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "token.json"), tokenData, 0o600); err != nil {
		t.Fatal(err)
	}
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()

	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	fn()
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	os.Stdout = old
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func TestSyncState(t *testing.T) {
	// Override HOME so sync state goes to temp dir
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)

	state := loadSyncState()
	if len(state.Skills) != 0 {
		t.Errorf("fresh sync state should be empty, got %d entries", len(state.Skills))
	}

	state.Skills["test-skill"] = &SyncEntry{SkillID: testUUID("abc-123").String(), Version: "1.0.0", Tool: "claude-code"}
	if err := saveSyncState(state); err != nil {
		t.Fatalf("save sync state: %v", err)
	}

	loaded := loadSyncState()
	entry := loaded.Skills["test-skill"]
	if entry == nil {
		t.Fatal("test-skill not found in loaded sync state")
	}
	if entry.SkillID != testUUID("abc-123").String() || entry.Version != "1.0.0" || entry.Tool != "claude-code" {
		t.Errorf("entry = %+v, want abc-123/1.0.0/claude-code", entry)
	}
}

// TestPropagatePartialRenames verifies that a manual rename in one agent
// dir is detected via content hash and propagated to other agent dirs that
// still hold the old name. Without this, push would silently create a
// duplicate skill on the server.
func TestPropagatePartialRenames(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	// Two agent dirs. Skill is at "today" in claude-code (representing the
	// renamed-already case) and at "old-name" in cursor (the dir that was
	// not renamed). Same content in both.
	claudeDir := filepath.Join(home, ".claude/skills")
	cursorDir := filepath.Join(home, ".cursor/skills")
	for _, d := range []string{claudeDir, cursorDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	skillContent := []byte("---\nname: old-name\ndescription: x\n---\nbody\n")
	if err := os.MkdirAll(filepath.Join(claudeDir, "today"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(claudeDir, "today", "SKILL.md"), skillContent, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(cursorDir, "old-name"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cursorDir, "old-name", "SKILL.md"), skillContent, 0o644); err != nil {
		t.Fatal(err)
	}

	// Compute the hash so the marker matches.
	hash := computeMerkleHash(readSkillFiles(filepath.Join(claudeDir, "today")))

	syncState := &SyncState{Skills: map[string]*SyncEntry{
		"old-name": {SkillID: testUUID("uuid-1").String(), Version: "1.0.0", ContentHash: hash, Tool: "claude-code"},
	}}
	localSkills := map[string]string{
		"today":    filepath.Join(claudeDir, "today"),
		"old-name": filepath.Join(cursorDir, "old-name"),
	}

	propagatePartialRenames(localSkills, syncState)

	if _, still := localSkills["old-name"]; still {
		t.Errorf("old-name should be removed from localSkills after propagation; got %v", localSkills)
	}
	if _, ok := localSkills["today"]; !ok {
		t.Errorf("today should remain in localSkills; got %v", localSkills)
	}
	if _, err := os.Stat(filepath.Join(cursorDir, "old-name")); !os.IsNotExist(err) {
		t.Errorf("cursor/old-name should be gone after rename, err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(cursorDir, "today")); err != nil {
		t.Errorf("cursor/today should exist after rename, err=%v", err)
	}
}

// TestRenameLocalSkillRewritesNameField verifies that `airskills mv` not
// only moves the directory but also rewrites the `name:` field inside
// SKILL.md. Without this rewrite, the next content edit + push fails
// server-side with name_slug_mismatch.
func TestRenameLocalSkillRewritesNameField(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	claudeDir := filepath.Join(home, ".claude/skills/old-name")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	skillMd := []byte("---\nname: old-name\ndescription: x\n---\n\nbody\n")
	if err := os.WriteFile(filepath.Join(claudeDir, "SKILL.md"), skillMd, 0o644); err != nil {
		t.Fatal(err)
	}

	moves, err := renameLocalSkill("old-name", "new-name")
	if err != nil {
		t.Fatalf("rename failed: %v", err)
	}
	if len(moves) == 0 {
		t.Fatal("expected at least one move")
	}

	newPath := filepath.Join(home, ".claude/skills/new-name/SKILL.md")
	got, err := os.ReadFile(newPath)
	if err != nil {
		t.Fatalf("reading post-rename SKILL.md: %v", err)
	}
	if !bytes.Contains(got, []byte("name: new-name")) {
		t.Errorf("SKILL.md `name:` field not rewritten; got: %s", got)
	}
	if bytes.Contains(got, []byte("name: old-name")) {
		t.Errorf("SKILL.md still has old name field; got: %s", got)
	}
}

// TestPushDoesNotCreatePhantomAfterMovedKept covers the regression where
// pushing a local dir whose marker had been classified as "moved to a
// different owner" would, on the next push, silently create a NEW
// personal skill from the dir — because the marker was fully deleted,
// leaving the orphan dir indistinguishable from an untracked one.
//
// After the fix, moved-kept entries persist as a tombstone marker
// (Deleted: true, MovedTo set) so subsequent pushes leave the dir alone
// instead of auto-publishing a duplicate.
func TestPushDoesNotCreatePhantomAfterMovedKept(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	oldIsTTY := isTTY
	isTTY = false
	t.Cleanup(func() { isTTY = oldIsTTY })

	skillID := "22222222-2222-2222-2222-222222222222"
	skillDir := filepath.Join(home, ".claude", "skills", "stale-mover")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	skillBody := []byte("---\nname: stale-mover\ndescription: test\n---\n\nbody\n")
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), skillBody, 0o644); err != nil {
		t.Fatal(err)
	}
	hash := computeMerkleHash(readSkillFiles(skillDir))

	state := &SyncState{Version: 1, Skills: map[string]*SyncEntry{
		"stale-mover": {
			SkillID:     skillID,
			Version:     "1.0.0",
			ContentHash: hash,
			Tool:        "claude-code",
		},
	}}
	if err := saveSyncState(state); err != nil {
		t.Fatalf("save sync state: %v", err)
	}

	var createPosts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/skills":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"skills":[]}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/skills/"+skillID:
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"slug":"stale-mover","org":{"slug":"chrismdp-ltd"}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/skills":
			createPosts++
			w.WriteHeader(http.StatusCreated)
			fmt.Fprintf(w, `{"id":%q,"name":"stale-mover","slug":"stale-mover","version":"1.0.0"}`, "33333333-3333-3333-3333-333333333333")
		default:
			t.Logf("unexpected request: %s %s", r.Method, r.URL.String())
			http.Error(w, "not found", 404)
		}
	}))
	defer srv.Close()

	writeTestConfigAndToken(t, home, srv.URL)

	cmd := &cobra.Command{Use: "push"}

	_ = captureStdout(t, func() {
		if err := pushCmd.RunE(cmd, nil); err != nil {
			t.Fatalf("push pass 1: %v", err)
		}
	})
	if createPosts != 0 {
		t.Fatalf("pass 1: expected 0 POST /api/v1/skills, got %d", createPosts)
	}

	_ = captureStdout(t, func() {
		if err := pushCmd.RunE(cmd, nil); err != nil {
			t.Fatalf("push pass 2: %v", err)
		}
	})
	if createPosts != 0 {
		t.Fatalf("pass 2: expected 0 POST /api/v1/skills, got %d (phantom personal skill was created)", createPosts)
	}

	final := loadSyncState()
	entry := final.Skills["stale-mover"]
	if entry == nil {
		t.Fatalf("expected stale-mover marker to be retained as a tombstone")
	}
	if !entry.Deleted {
		t.Errorf("expected Deleted=true on tombstone marker, got %+v", entry)
	}
	if entry.MovedTo == "" {
		t.Errorf("expected MovedTo to record the new owner/slug, got empty")
	}
	if entry.SkillID != "" {
		t.Errorf("expected SkillID cleared on tombstone marker, got %q", entry.SkillID)
	}
}

// TestPushNoMovedWarningForOrgMembershipSkill verifies the headline bug:
// for a skill the caller is a member of via an org skillset (but does NOT
// personally own), every sync was emitting a spurious "moved (re-link
// needed)" warning. The classifier used scope=personal which excludes
// org-membership skills, so a marker for one looked like it pointed at a
// transferred-away skill.
//
// After the fix push lists the caller's effective skillset, not their
// personal scope, so the org skill is in the "skills I can reach" set and
// the marker is treated as plain synced. No misclassification, no warning.
func TestPushNoMovedWarningForOrgMembershipSkill(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	oldIsTTY := isTTY
	isTTY = false
	t.Cleanup(func() { isTTY = oldIsTTY })

	skillID := "44444444-4444-4444-4444-444444444444"
	skillDir := filepath.Join(home, ".claude", "skills", "org-member-skill")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	skillBody := []byte("---\nname: org-member-skill\ndescription: test\n---\n\nbody\n")
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), skillBody, 0o644); err != nil {
		t.Fatal(err)
	}
	hash := computeMerkleHash(readSkillFiles(skillDir))

	state := &SyncState{Version: 1, Skills: map[string]*SyncEntry{
		"org-member-skill": {
			SkillID:     skillID,
			Version:     "1.0.0",
			ContentHash: hash,
			Tool:        "claude-code",
			// owner_kind/owner_slug intentionally absent — verification (5):
			// backfill-tolerant. Existing markers without these fields should
			// still classify correctly.
		},
	}}
	if err := saveSyncState(state); err != nil {
		t.Fatalf("save sync state: %v", err)
	}

	var personalScopeHits, effectiveSkillsetHits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/skills":
			scope := r.URL.Query().Get("scope")
			skillset := r.URL.Query().Has("skillset")
			isEffective := scope == "" && (skillset || r.URL.RawQuery == "")
			w.Header().Set("Content-Type", "application/json")
			if scope == "personal" {
				personalScopeHits++
				// Caller does NOT personally own this skill — it's org-owned.
				fmt.Fprint(w, `{"skills":[]}`)
			} else if isEffective {
				effectiveSkillsetHits++
				// The caller's effective skillset DOES include the org skill.
				fmt.Fprintf(w, `{"skillset":{"slug":"default","name":"Default"},"skills":[{"id":%q,"name":"org-member-skill","slug":"org-member-skill","version":"1.0.0","content_hash":%q,"tool_formats":["claude-code"],"owner_id":null,"org_id":"55555555-5555-5555-5555-555555555555","visibility":"private","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z","deleted_at":null,"deletion_reason":null,"description":null,"head_commit_id":null,"forked_from":null,"upstream_content_hash":null,"dependency_count":0,"archive_size":null,"pinned_archive_path":null}]}`, skillID, hash)
			} else {
				fmt.Fprint(w, `{"skills":[]}`)
			}
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/skills":
			t.Errorf("unexpected POST /api/v1/skills — push should not create anything")
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{"id":"00000000-0000-0000-0000-000000000000"}`)
		default:
			// Permit any other introspection (skill detail GETs) with a
			// generic shape so the classifier (which we should NOT invoke)
			// doesn't crash if reached.
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{}`)
		}
	}))
	defer srv.Close()

	writeTestConfigAndToken(t, home, srv.URL)

	cmd := &cobra.Command{Use: "push"}
	out := captureStdout(t, func() {
		if err := pushCmd.RunE(cmd, nil); err != nil {
			t.Fatalf("push: %v", err)
		}
	})

	if effectiveSkillsetHits == 0 {
		t.Errorf("expected push to fetch the effective skillset listing, but it never did. Hits: scope=personal %d, effective %d.\nOutput:\n%s",
			personalScopeHits, effectiveSkillsetHits, out)
	}
	if strings.Contains(out, "moved") || strings.Contains(out, "re-link needed") {
		t.Errorf("expected NO 'moved' warning for org-membership skill, got output:\n%s", out)
	}

	final := loadSyncState()
	entry := final.Skills["org-member-skill"]
	if entry == nil {
		t.Fatalf("marker missing after push")
	}
	if entry.Deleted {
		t.Errorf("expected marker NOT to be tombstoned, got Deleted=true")
	}
	if entry.SkillID != skillID {
		t.Errorf("expected SkillID preserved, got %q", entry.SkillID)
	}
}

// TestPropagatePartialRenames_FullOrphan covers the case where the marker
// has no surviving dirs anywhere. The function should leave it alone (the
// existing orphan-hash detector in push handles those).
func TestPropagatePartialRenames_FullOrphan(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	claudeDir := filepath.Join(home, ".claude/skills")
	if err := os.MkdirAll(filepath.Join(claudeDir, "today"), 0o755); err != nil {
		t.Fatal(err)
	}
	skillContent := []byte("---\nname: old-name\ndescription: x\n---\nbody\n")
	if err := os.WriteFile(filepath.Join(claudeDir, "today", "SKILL.md"), skillContent, 0o644); err != nil {
		t.Fatal(err)
	}
	hash := computeMerkleHash(readSkillFiles(filepath.Join(claudeDir, "today")))

	syncState := &SyncState{Skills: map[string]*SyncEntry{
		"old-name": {SkillID: testUUID("uuid-1").String(), Version: "1.0.0", ContentHash: hash, Tool: "claude-code"},
	}}
	localSkills := map[string]string{
		"today": filepath.Join(claudeDir, "today"),
		// old-name is NOT in localSkills — full orphan, should be left alone
	}

	propagatePartialRenames(localSkills, syncState)

	// localSkills should be unchanged — full-orphan rename is not this
	// function's job.
	if _, still := localSkills["today"]; !still {
		t.Errorf("today should still be in localSkills")
	}
	if len(localSkills) != 1 {
		t.Errorf("localSkills should have exactly one entry, got %v", localSkills)
	}
}

// TestPushScopesToPositionalArg verifies that `airskills push <name>`
// uploads only the named skill, leaving other dirty skills untouched.
//
// Before the fix, the positional arg was silently ignored and push always
// operated on the full effective set — see
// doc/changes/cli-push-positional-arg-silently-ignored.md (platform repo).
func TestPushScopesToPositionalArg(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	oldIsTTY := isTTY
	isTTY = false
	t.Cleanup(func() { isTTY = oldIsTTY })

	alphaID := "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	betaID := "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"

	for name := range map[string]string{"alpha": alphaID, "beta": betaID} {
		dir := filepath.Join(home, ".claude", "skills", name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		body := fmt.Sprintf("---\nname: %s\ndescription: test\n---\n\nbody %s\n", name, name)
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	state := &SyncState{Version: 1, Skills: map[string]*SyncEntry{
		"alpha": {SkillID: alphaID, Version: "1.0.0", ContentHash: "stale-alpha-hash", Tool: "claude-code"},
		"beta":  {SkillID: betaID, Version: "1.0.0", ContentHash: "stale-beta-hash", Tool: "claude-code"},
	}}
	if err := saveSyncState(state); err != nil {
		t.Fatalf("save sync state: %v", err)
	}

	uploads := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/skills":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"skills":[
				{"id":%q,"name":"alpha","slug":"alpha","version":"1.0.0","content_hash":"server-alpha","tool_formats":["claude-code"],"visibility":"private","dependency_count":0},
				{"id":%q,"name":"beta","slug":"beta","version":"1.0.0","content_hash":"server-beta","tool_formats":["claude-code"],"visibility":"private","dependency_count":0}
			]}`, alphaID, betaID)
		case r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/archive"):
			id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/v1/skills/"), "/archive")
			uploads[id]++
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"id":%q,"version":"1.0.1","content_hash":"new-hash"}`, id)
		default:
			t.Logf("unexpected request: %s %s", r.Method, r.URL.String())
			http.Error(w, "not handled", 404)
		}
	}))
	defer srv.Close()

	writeTestConfigAndToken(t, home, srv.URL)

	cmd := &cobra.Command{Use: "push"}
	_ = captureStdout(t, func() {
		if err := pushCmd.RunE(cmd, []string{"alpha"}); err != nil {
			t.Fatalf("push RunE: %v", err)
		}
	})

	if uploads[alphaID] != 1 {
		t.Errorf("alpha uploads = %d, want 1", uploads[alphaID])
	}
	if uploads[betaID] != 0 {
		t.Errorf("beta uploads = %d, want 0 (scoped push should not touch beta)", uploads[betaID])
	}
}

// TestPushRejectsUnknownSkill verifies that a positional arg that doesn't
// match any local skill directory errors out before any work is done.
func TestPushRejectsUnknownSkill(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	oldIsTTY := isTTY
	isTTY = false
	t.Cleanup(func() { isTTY = oldIsTTY })

	skillDir := filepath.Join(home, ".claude", "skills", "alpha")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"),
		[]byte("---\nname: alpha\ndescription: test\n---\n\nbody\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Logf("server received %s %s", r.Method, r.URL.String())
		http.Error(w, "should not reach server", 500)
	}))
	defer srv.Close()

	writeTestConfigAndToken(t, home, srv.URL)

	cmd := &cobra.Command{Use: "push"}
	var err error
	_ = captureStdout(t, func() {
		err = pushCmd.RunE(cmd, []string{"does-not-exist"})
	})
	if err == nil {
		t.Fatal("expected error for unknown skill, got nil")
	}
	if !strings.Contains(err.Error(), "does-not-exist") {
		t.Errorf("error %q should name the unknown skill", err.Error())
	}
}
