package cmd

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// serveRemoteSkill stands up a mock server exposing the two endpoints the
// resolver uses: GET /api/v1/skills/<id> returns the file manifest (paths +
// sizes, no bodies — the cheap baseline), and GET /api/v1/skills/<id>/archive
// returns a tar.gz with the bodies (only fetched when restoring). Returns an
// apiClient pointed at it.
func serveRemoteSkill(t *testing.T, skillID string, files map[string]string) *apiClient {
	t.Helper()
	remoteDir := filepath.Join(t.TempDir(), "remote")
	writeFileTree(t, remoteDir, files)
	archive, err := createTarGz(remoteDir)
	if err != nil {
		t.Fatalf("createTarGz: %v", err)
	}
	manifest := `{"id":"` + skillID + `","files":[`
	first := true
	for path, content := range files {
		if !first {
			manifest += ","
		}
		first = false
		manifest += fmt.Sprintf(`{"path":%q,"size":%d}`, path, len(content))
	}
	manifest += `]}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case fmt.Sprintf("/api/v1/skills/%s", skillID):
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, manifest)
		case fmt.Sprintf("/api/v1/skills/%s/archive", skillID):
			w.Write(archive)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return &apiClient{baseURL: srv.URL, token: "t", http: srv.Client()}
}

// TestResolveIntraSkillDeletionsKeepRestores: a file deleted from one agent
// copy but present in another and on the remote. The headless default
// (deletionKeep) must restore it everywhere — never silently destroy.
func TestResolveIntraSkillDeletionsKeepRestores(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	claude := filepath.Join(home, ".claude", "skills", "triage")
	cursor := filepath.Join(home, ".cursor", "skills", "triage")
	writeFileTree(t, claude, map[string]string{"SKILL.md": "# triage", "scripts/a.sh": "a"})                      // b.sh deleted here
	writeFileTree(t, cursor, map[string]string{"SKILL.md": "# triage", "scripts/a.sh": "a", "scripts/b.sh": "b"}) // intact

	id := testUUID("triage").String()
	state := loadSyncState()
	state.Skills["triage"] = &SyncEntry{SkillID: id, ContentHash: "stale", Tool: "claude-code"}
	saveSyncState(state)

	client := serveRemoteSkill(t, id, map[string]string{
		"SKILL.md": "# triage", "scripts/a.sh": "a", "scripts/b.sh": "b",
	})

	_ = captureStdout(t, func() {
		resolveIntraSkillDeletions(client, loadSyncState(), deletionKeep, nil)
	})

	if _, err := os.Stat(filepath.Join(claude, "scripts", "b.sh")); err != nil {
		t.Errorf("keep must restore the deleted file into .claude: %v", err)
	}
}

// TestResolveIntraSkillDeletionsRemoveDeletesEverywhere: --force
// (deletionRemove) must delete the file from every copy so the next push
// drops it server-side.
func TestResolveIntraSkillDeletionsRemoveDeletesEverywhere(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	claude := filepath.Join(home, ".claude", "skills", "triage")
	cursor := filepath.Join(home, ".cursor", "skills", "triage")
	writeFileTree(t, claude, map[string]string{"SKILL.md": "# triage", "scripts/a.sh": "a"})                      // b.sh deleted here
	writeFileTree(t, cursor, map[string]string{"SKILL.md": "# triage", "scripts/a.sh": "a", "scripts/b.sh": "b"}) // still has it

	id := testUUID("triage").String()
	state := loadSyncState()
	state.Skills["triage"] = &SyncEntry{SkillID: id, ContentHash: "stale", Tool: "claude-code"}
	saveSyncState(state)

	client := serveRemoteSkill(t, id, map[string]string{
		"SKILL.md": "# triage", "scripts/a.sh": "a", "scripts/b.sh": "b",
	})

	_ = captureStdout(t, func() {
		resolveIntraSkillDeletions(client, loadSyncState(), deletionRemove, nil)
	})

	for _, d := range []string{claude, cursor} {
		if _, err := os.Stat(filepath.Join(d, "scripts", "b.sh")); !os.IsNotExist(err) {
			t.Errorf("force-remove must delete b.sh from %s", d)
		}
	}
}

// A scoped push (`airskills push triage`) must confine deletion handling to
// the named skills — `push triage --force` must never remove files from an
// unrelated skill that also happens to have a local deletion.
func TestResolveIntraSkillDeletionsRespectsScope(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	// Two tracked skills, each with a file deleted from its .claude copy.
	triage := filepath.Join(home, ".claude", "skills", "triage")
	other := filepath.Join(home, ".claude", "skills", "other")
	writeFileTree(t, triage, map[string]string{"SKILL.md": "# triage"}) // scripts/t.sh deleted
	writeFileTree(t, other, map[string]string{"SKILL.md": "# other", "keep.sh": "k"})

	tID := testUUID("triage").String()
	oID := testUUID("other").String()
	state := loadSyncState()
	state.Skills["triage"] = &SyncEntry{SkillID: tID, ContentHash: "stale", Tool: "claude-code"}
	state.Skills["other"] = &SyncEntry{SkillID: oID, ContentHash: "stale", Tool: "claude-code"}
	saveSyncState(state)

	// Mock serving both skills' manifests; `other` still lists a file the
	// local copy lacks, so without scoping --force would delete it too.
	triageDir := filepath.Join(t.TempDir(), "triage-remote")
	writeFileTree(t, triageDir, map[string]string{"SKILL.md": "# triage", "scripts/t.sh": "t"})
	otherDir := filepath.Join(t.TempDir(), "other-remote")
	writeFileTree(t, otherDir, map[string]string{"SKILL.md": "# other", "keep.sh": "k", "gone.sh": "g"})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/skills/" + tID:
			fmt.Fprintf(w, `{"id":%q,"files":[{"path":"SKILL.md","size":8},{"path":"scripts/t.sh","size":1}]}`, tID)
		case "/api/v1/skills/" + oID:
			t.Errorf("scoped push to triage must not fetch the manifest for 'other'")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	client := &apiClient{baseURL: srv.URL, token: "t", http: srv.Client()}

	_ = captureStdout(t, func() {
		resolveIntraSkillDeletions(client, loadSyncState(), deletionRemove, []string{"triage"})
	})

	// 'other' is untouched: its existing file survives.
	if _, err := os.Stat(filepath.Join(other, "keep.sh")); err != nil {
		t.Errorf("scoped push must not touch unrelated skill 'other': %v", err)
	}
}

// The suppress guard (set by `rm <skill>/<path>`'s internal push) must make
// the resolver a no-op so it doesn't restore a file the user just deleted.
func TestResolveIntraSkillDeletionsSuppressed(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	claude := filepath.Join(home, ".claude", "skills", "triage")
	cursor := filepath.Join(home, ".cursor", "skills", "triage")
	writeFileTree(t, claude, map[string]string{"SKILL.md": "# triage"})
	writeFileTree(t, cursor, map[string]string{"SKILL.md": "# triage", "scripts/b.sh": "b"})

	id := testUUID("triage").String()
	state := loadSyncState()
	state.Skills["triage"] = &SyncEntry{SkillID: id, ContentHash: "stale", Tool: "claude-code"}
	saveSyncState(state)

	client := serveRemoteSkill(t, id, map[string]string{"SKILL.md": "# triage", "scripts/b.sh": "b"})

	suppressDeletionPrompt = true
	t.Cleanup(func() { suppressDeletionPrompt = false })

	resolveIntraSkillDeletions(client, loadSyncState(), deletionKeep, nil)

	if _, err := os.Stat(filepath.Join(claude, "scripts", "b.sh")); !os.IsNotExist(err) {
		t.Error("suppressed resolver must not restore the deleted file")
	}
}

func writeFileTree(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDetectDeletedSkillFiles(t *testing.T) {
	dir := t.TempDir()
	claude := filepath.Join(dir, ".claude", "skills", "triage")
	cursor := filepath.Join(dir, ".cursor", "skills", "triage")
	// .claude is missing scripts/b.sh (user hand-deleted it there); .cursor
	// still has the full set. The remote baseline has everything.
	writeFileTree(t, claude, map[string]string{
		"SKILL.md":     "# triage",
		"scripts/a.sh": "a",
	})
	writeFileTree(t, cursor, map[string]string{
		"SKILL.md":     "# triage",
		"scripts/a.sh": "a",
		"scripts/b.sh": "b",
	})
	remote := []string{"SKILL.md", "scripts/a.sh", "scripts/b.sh"}

	got := detectDeletedSkillFiles([]string{claude, cursor}, remote)
	want := []string{"scripts/b.sh"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("detectDeletedSkillFiles = %v, want %v", got, want)
	}
}

func TestDetectDeletedSkillFilesNeverFlagsManifest(t *testing.T) {
	dir := t.TempDir()
	claude := filepath.Join(dir, ".claude", "skills", "triage")
	// Even if SKILL.md were somehow missing locally, it must never be a
	// deletion candidate — removing it would break the skill.
	writeFileTree(t, claude, map[string]string{"scripts/a.sh": "a"})
	remote := []string{"SKILL.md", "scripts/a.sh"}

	for _, f := range detectDeletedSkillFiles([]string{claude}, remote) {
		if f == "SKILL.md" {
			t.Errorf("SKILL.md must never be flagged as a deletion candidate")
		}
	}
}

func TestDetectDeletedSkillFilesSkipsIgnored(t *testing.T) {
	dir := t.TempDir()
	claude := filepath.Join(dir, ".claude", "skills", "triage")
	// The user keeps state/ local-only via .askignore. The remote happens to
	// still carry an old copy of state/run.log — it must NOT be flagged as a
	// deletion (the user deliberately excludes it, didn't delete it).
	writeFileTree(t, claude, map[string]string{
		"SKILL.md":   "# triage",
		".askignore": "state/\n",
	})
	remote := []string{"SKILL.md", "state/run.log"}

	if got := detectDeletedSkillFiles([]string{claude}, remote); len(got) != 0 {
		t.Errorf("ignored remote files must not be flagged, got %v", got)
	}
}

func TestSkillChangedVsMarkerGate(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a", "triage")
	writeFileTree(t, a, map[string]string{"SKILL.md": "x", "scripts/a.sh": "a"})

	hash := computeMerkleHash(readSkillFiles(a))

	// Matching the marker hash → unchanged → no fetch.
	if skillChangedVsMarker([]string{a}, hash) {
		t.Error("a copy matching the marker hash must be reported unchanged")
	}

	// Delete a file → hash differs → changed → manifest fetch warranted.
	os.Remove(filepath.Join(a, "scripts", "a.sh"))
	if !skillChangedVsMarker([]string{a}, hash) {
		t.Error("a copy with a deleted file must be reported changed")
	}
}

func TestRemoveMissingDeletesEverywhere(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
	claude := filepath.Join(dir, ".claude", "skills", "triage")
	cursor := filepath.Join(dir, ".cursor", "skills", "triage")
	for _, d := range []string{claude, cursor} {
		writeFileTree(t, d, map[string]string{"SKILL.md": "# triage", "scripts/b.sh": "b"})
	}

	removeMissing("triage", []string{"scripts/b.sh"})

	for _, d := range []string{claude, cursor} {
		if _, err := os.Stat(filepath.Join(d, "scripts", "b.sh")); !os.IsNotExist(err) {
			t.Errorf("scripts/b.sh should be gone from %s", d)
		}
	}
}

func TestRestoreMissingRewritesFromRemote(t *testing.T) {
	dir := t.TempDir()
	claude := filepath.Join(dir, ".claude", "skills", "triage")
	cursor := filepath.Join(dir, ".cursor", "skills", "triage")
	writeFileTree(t, claude, map[string]string{"SKILL.md": "# triage"})                      // missing b.sh
	writeFileTree(t, cursor, map[string]string{"SKILL.md": "# triage", "scripts/b.sh": "b"}) // has it

	id := testUUID("triage").String()
	client := serveRemoteSkill(t, id, map[string]string{"SKILL.md": "# triage", "scripts/b.sh": "b-from-remote"})

	restoreMissing(client, id, "triage", []string{claude, cursor}, []string{"scripts/b.sh"})

	data, err := os.ReadFile(filepath.Join(claude, "scripts", "b.sh"))
	if err != nil {
		t.Fatalf("b.sh should have been restored into .claude: %v", err)
	}
	if string(data) != "b-from-remote" {
		t.Errorf("restored content = %q, want remote content", string(data))
	}
}
