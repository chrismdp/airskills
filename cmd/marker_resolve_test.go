package cmd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestDirNameForOwner(t *testing.T) {
	cases := []struct {
		name     string
		current  string
		oldSlug  string
		newSlug  string
		expected string
	}{
		{
			name:     "user to org adds prefix",
			current:  "chrismdp-deploy-check",
			oldSlug:  "chrismdp",
			newSlug:  "cherrypick",
			expected: "cherrypick-deploy-check",
		},
		{
			name:     "bare name to org adds prefix",
			current:  "deploy-check",
			oldSlug:  "",
			newSlug:  "cherrypick",
			expected: "cherrypick-deploy-check",
		},
		{
			name:     "org to user swaps prefix",
			current:  "cherrypick-deploy-check",
			oldSlug:  "cherrypick",
			newSlug:  "chrismdp",
			expected: "chrismdp-deploy-check",
		},
		{
			name:     "no change when slugs match",
			current:  "cherrypick-foo",
			oldSlug:  "cherrypick",
			newSlug:  "cherrypick",
			expected: "cherrypick-foo",
		},
		{
			name:     "old slug missing from name still strips nothing wrong",
			current:  "ops-foo",
			oldSlug:  "chrismdp",
			newSlug:  "newowner",
			expected: "newowner-ops-foo",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := dirNameForOwner(c.current, c.oldSlug, c.newSlug)
			if got != c.expected {
				t.Fatalf("got %q, want %q", got, c.expected)
			}
		})
	}
}

func TestRenameSkillDirAcrossAgents_NoOpWhenSame(t *testing.T) {
	if err := renameSkillDirAcrossAgents("foo", "foo"); err != nil {
		t.Fatalf("expected nil for same name, got %v", err)
	}
}

func TestRenameSkillDirAcrossAgents_RenamesExistingDir(t *testing.T) {
	// Real rename via temp HOME so we don't touch the user's actual config.
	tmp, err := os.MkdirTemp("", "airskills-rename-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmp)
	t.Setenv("HOME", tmp)
	t.Setenv("USERPROFILE", tmp)

	// Create a fake skill dir under each agent's global path.
	for _, a := range agents {
		globalPath := resolveGlobalDir(tmp, a.GlobalDir)
		oldDir := filepath.Join(globalPath, "chrismdp-foo")
		if err := os.MkdirAll(oldDir, 0755); err != nil {
			t.Fatalf("mkdir %s: %v", oldDir, err)
		}
		if err := os.WriteFile(filepath.Join(oldDir, "SKILL.md"), []byte("x"), 0644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}

	if err := renameSkillDirAcrossAgents("chrismdp-foo", "cherrypick-foo"); err != nil {
		t.Fatalf("rename failed: %v", err)
	}

	for _, a := range agents {
		globalPath := resolveGlobalDir(tmp, a.GlobalDir)
		newDir := filepath.Join(globalPath, "cherrypick-foo")
		if _, err := os.Stat(newDir); err != nil {
			t.Errorf("expected new dir at %s, got %v", newDir, err)
		}
		oldDir := filepath.Join(globalPath, "chrismdp-foo")
		if _, err := os.Stat(oldDir); !os.IsNotExist(err) {
			t.Errorf("expected old dir gone at %s, got %v", oldDir, err)
		}
	}
}

func TestUpdateLocalMarkerForTransferRepointsExistingMarker(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	oldID := testUUID("old-home").String()
	newID := testUUID("new-home").String()
	if err := saveSyncState(&SyncState{
		Version: 1,
		Skills: map[string]*SyncEntry{
			"home": {
				SkillID:     oldID,
				Version:     "1.0.3",
				ContentHash: "oldhash",
				OwnerKind:   "user",
				OwnerSlug:   "chrismdp",
			},
		},
	}); err != nil {
		t.Fatalf("saveSyncState: %v", err)
	}

	err := updateLocalMarkerForTransfer("home", oldID, newID, "org", "parsons-home", "home", "1.0.4", "newhash")
	if err != nil {
		t.Fatalf("updateLocalMarkerForTransfer: %v", err)
	}

	entry := loadSyncState().Skills["home"]
	if entry == nil {
		t.Fatal("missing home marker")
	}
	if entry.SkillID != newID {
		t.Fatalf("SkillID = %q, want %q", entry.SkillID, newID)
	}
	if entry.OwnerKind != "org" || entry.OwnerSlug != "parsons-home" {
		t.Fatalf("owner = %s/%s, want org/parsons-home", entry.OwnerKind, entry.OwnerSlug)
	}
	if entry.Version != "1.0.4" || entry.ContentHash != "newhash" {
		t.Fatalf("version/hash = %s/%s, want 1.0.4/newhash", entry.Version, entry.ContentHash)
	}
	if entry.Deleted || entry.MovedTo != "" {
		t.Fatalf("marker still tombstoned: %+v", entry)
	}
}

func TestUpdateLocalMarkerForTransferRepointsTransferTombstone(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	newID := testUUID("new-home").String()
	if err := saveSyncState(&SyncState{
		Version: 1,
		Skills: map[string]*SyncEntry{
			"home": {
				Deleted: true,
				MovedTo: "parsons-home/home",
			},
		},
	}); err != nil {
		t.Fatalf("saveSyncState: %v", err)
	}

	err := updateLocalMarkerForTransfer("home", testUUID("old-home").String(), newID, "org", "parsons-home", "home", "1.0.4", "newhash")
	if err != nil {
		t.Fatalf("updateLocalMarkerForTransfer: %v", err)
	}

	entry := loadSyncState().Skills["home"]
	if entry == nil {
		t.Fatal("missing home marker")
	}
	if entry.SkillID != newID {
		t.Fatalf("SkillID = %q, want %q", entry.SkillID, newID)
	}
	if entry.OwnerKind != "org" || entry.OwnerSlug != "parsons-home" {
		t.Fatalf("owner = %s/%s, want org/parsons-home", entry.OwnerKind, entry.OwnerSlug)
	}
	if entry.Version != "1.0.4" || entry.ContentHash != "newhash" {
		t.Fatalf("version/hash = %s/%s, want 1.0.4/newhash", entry.Version, entry.ContentHash)
	}
	if entry.Deleted || entry.MovedTo != "" {
		t.Fatalf("marker still tombstoned: %+v", entry)
	}
}

func TestRepairTransferTombstoneMarkersRepointsExactLocalMatch(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	localDir := filepath.Join(home, ".claude", "skills", "home")
	if err := os.MkdirAll(localDir, 0700); err != nil {
		t.Fatalf("MkdirAll local skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(localDir, "SKILL.md"), []byte("---\nname: home\ndescription: test\n---\n\nbody\n"), 0600); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
	localHash := computeMerkleHash(readSkillFiles(localDir))
	newID := testUUID("new-home").String()
	if err := saveSyncState(&SyncState{
		Version: 1,
		Skills: map[string]*SyncEntry{
			"home": {
				Deleted: true,
				MovedTo: "parsons-home/home",
			},
		},
	}); err != nil {
		t.Fatalf("saveSyncState: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/resolve/parsons-home/home" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"id":           newID,
			"version":      "1.0.4",
			"content_hash": localHash,
			"current_owner": map[string]string{
				"kind": "org",
				"slug": "parsons-home",
			},
		})
	}))
	defer srv.Close()

	client := &apiClient{baseURL: srv.URL, token: "test-token", http: srv.Client()}
	repaired := repairTransferTombstoneMarkers(client, map[string]string{"home": localDir}, loadSyncState())
	if repaired != 1 {
		t.Fatalf("repaired = %d, want 1", repaired)
	}

	entry := loadSyncState().Skills["home"]
	if entry == nil {
		t.Fatal("missing home marker")
	}
	if entry.SkillID != newID {
		t.Fatalf("SkillID = %q, want %q", entry.SkillID, newID)
	}
	if entry.OwnerKind != "org" || entry.OwnerSlug != "parsons-home" {
		t.Fatalf("owner = %s/%s, want org/parsons-home", entry.OwnerKind, entry.OwnerSlug)
	}
	if entry.Version != "1.0.4" || entry.ContentHash != localHash {
		t.Fatalf("version/hash = %s/%s, want 1.0.4/%s", entry.Version, entry.ContentHash, localHash)
	}
	if entry.Deleted || entry.MovedTo != "" {
		t.Fatalf("marker still tombstoned: %+v", entry)
	}
}

func TestRepairTransferTombstoneMarkersLeavesDivergedLocalTombstoned(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	localDir := filepath.Join(home, ".claude", "skills", "home")
	if err := os.MkdirAll(localDir, 0700); err != nil {
		t.Fatalf("MkdirAll local skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(localDir, "SKILL.md"), []byte("---\nname: home\ndescription: test\n---\n\nlocal edit\n"), 0600); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
	if err := saveSyncState(&SyncState{
		Version: 1,
		Skills: map[string]*SyncEntry{
			"home": {
				Deleted: true,
				MovedTo: "parsons-home/home",
			},
		},
	}); err != nil {
		t.Fatalf("saveSyncState: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/resolve/parsons-home/home" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"id":           testUUID("new-home").String(),
			"version":      "1.0.4",
			"content_hash": "remotehash",
			"current_owner": map[string]string{
				"kind": "org",
				"slug": "parsons-home",
			},
		})
	}))
	defer srv.Close()

	client := &apiClient{baseURL: srv.URL, token: "test-token", http: srv.Client()}
	repaired := repairTransferTombstoneMarkers(client, map[string]string{"home": localDir}, loadSyncState())
	if repaired != 0 {
		t.Fatalf("repaired = %d, want 0", repaired)
	}

	entry := loadSyncState().Skills["home"]
	if entry == nil {
		t.Fatal("missing home marker")
	}
	if !entry.Deleted || entry.MovedTo != "parsons-home/home" || entry.SkillID != "" {
		t.Fatalf("marker was unexpectedly repaired: %+v", entry)
	}
}
