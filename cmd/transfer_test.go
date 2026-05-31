package cmd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/chrismdp/airskills/config"
	"github.com/spf13/cobra"
)

// TestLookupCallerOrgIDMultiOrg verifies that lookupCallerOrgID works correctly
// for users who belong to multiple orgs, using the /api/v1/organizations endpoint.
func TestLookupCallerOrgIDMultiOrg(t *testing.T) {
	// Mock server only handles /api/v1/organizations (the multi-org endpoint).
	// Old code that calls /api/v1/organization will receive a 404 and fail.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/organizations" {
			http.Error(w, `{"error":"not found"}`, 404)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"organizations": []map[string]interface{}{
				{"id": "org-abc", "slug": "cherrypick"},
				{"id": "org-xyz", "slug": "loomery"},
			},
		})
	}))
	defer srv.Close()

	client := &apiClient{baseURL: srv.URL, token: "test-token", http: srv.Client()}

	// Success: first org found.
	id, err := lookupCallerOrgID(client, "cherrypick")
	if err != nil {
		t.Fatalf("unexpected error for cherrypick: %v", err)
	}
	if id != "org-abc" {
		t.Errorf("cherrypick: expected org-abc, got %s", id)
	}

	// Success: second org found — validates multi-org support.
	id, err = lookupCallerOrgID(client, "loomery")
	if err != nil {
		t.Fatalf("unexpected error for loomery: %v", err)
	}
	if id != "org-xyz" {
		t.Errorf("loomery: expected org-xyz, got %s", id)
	}

	// Error: slug not in the list.
	_, err = lookupCallerOrgID(client, "unknown-org")
	if err == nil {
		t.Fatal("expected error for unknown org, got nil")
	}
}

// TestLookupCallerOrgIDNotMember verifies the error message when the user
// belongs to no orgs at all.
func TestLookupCallerOrgIDNotMember(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/organizations" {
			http.Error(w, `{"error":"not found"}`, 404)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"organizations": []map[string]interface{}{},
		})
	}))
	defer srv.Close()

	client := &apiClient{baseURL: srv.URL, token: "test-token", http: srv.Client()}

	_, err := lookupCallerOrgID(client, "any-org")
	if err == nil {
		t.Fatal("expected error when not a member of any org")
	}
}

func TestTransferToOrgRepointsLocalTombstoneMarkerEndToEnd(t *testing.T) {
	oldID := testUUID("old-home").String()
	newID := testUUID("new-home").String()
	home := setupTransferCommandTest(t, transferCommandFixture{
		oldID: oldID,
		newID: newID,
	})
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

	runTransferCommand(t, "home", "parsons-home")

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
	if _, err := os.Stat(filepath.Join(home, ".config", "airskills", "sync.json")); err != nil {
		t.Fatalf("expected sync state written: %v", err)
	}
}

func TestTransferToOrgRepointsExistingLocalMarkerEndToEnd(t *testing.T) {
	oldID := testUUID("old-home").String()
	newID := testUUID("new-home").String()
	setupTransferCommandTest(t, transferCommandFixture{
		oldID: oldID,
		newID: newID,
	})
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

	runTransferCommand(t, "home", "parsons-home")

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

// TestTransferLinksUntrackedLocalSkillEndToEnd covers the transferring machine
// when the skill is present on disk but no sync marker matches it (the marker
// was never written, or an earlier sync cleared its skill_id). Before the fix
// updateLocalMarkerForTransfer silently no-op'd, leaving the local dir orphaned
// so the next sync saw the old skill_id archived and tombstoned it
// ("upstream archived"). Now the CLI links the local skill to the new org copy
// exactly as a pull would.
func TestTransferLinksUntrackedLocalSkillEndToEnd(t *testing.T) {
	oldID := testUUID("old-home").String()
	newID := testUUID("new-home").String()
	home := setupTransferCommandTest(t, transferCommandFixture{
		oldID: oldID,
		newID: newID,
	})

	// Skill exists on disk...
	localDir := filepath.Join(home, ".claude", "skills", "home")
	if err := os.MkdirAll(localDir, 0700); err != nil {
		t.Fatalf("MkdirAll local skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(localDir, "SKILL.md"), []byte("---\nname: home\ndescription: test\n---\n\nbody\n"), 0600); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
	// ...but nothing tracks it in the marker.
	if err := saveSyncState(&SyncState{Version: 1, Skills: map[string]*SyncEntry{}}); err != nil {
		t.Fatalf("saveSyncState: %v", err)
	}

	runTransferCommand(t, "home", "parsons-home")

	entry := loadSyncState().Skills["home"]
	if entry == nil {
		t.Fatal("transfer left the local skill untracked; expected it linked to the org copy")
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
	if entry.Tool != "claude-code" {
		t.Fatalf("Tool = %q, want claude-code", entry.Tool)
	}
	if entry.Deleted || entry.MovedTo != "" {
		t.Fatalf("marker should be a clean link, got %+v", entry)
	}
}

type transferCommandFixture struct {
	oldID string
	newID string
}

func setupTransferCommandTest(t *testing.T, f transferCommandFixture) string {
	t.Helper()

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/skills" && r.URL.Query().Get("scope") == "personal":
			json.NewEncoder(w).Encode(map[string]interface{}{
				"skills": []map[string]interface{}{
					{
						"id":           f.oldID,
						"name":         "home",
						"version":      "1.0.3",
						"content_hash": "oldhash",
					},
				},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/organizations":
			json.NewEncoder(w).Encode(map[string]interface{}{
				"organizations": []map[string]interface{}{
					{"id": "org-parsons-home", "slug": "parsons-home"},
				},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/skills/"+f.oldID+"/transfer":
			var payload struct {
				To struct {
					Kind string `json:"kind"`
					ID   string `json:"id"`
				} `json:"to"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode transfer payload: %v", err)
			}
			if payload.To.Kind != "org" || payload.To.ID != "org-parsons-home" {
				t.Fatalf("transfer target = %s/%s, want org/org-parsons-home", payload.To.Kind, payload.To.ID)
			}
			json.NewEncoder(w).Encode(map[string]interface{}{
				"id":           f.newID,
				"name":         "home",
				"version":      "1.0.4",
				"content_hash": "newhash",
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	cfgDir := filepath.Join(home, ".config", "airskills")
	if err := os.MkdirAll(cfgDir, 0700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	cfgData, _ := json.Marshal(config.Config{APIURL: srv.URL})
	if err := os.WriteFile(filepath.Join(cfgDir, "config.json"), cfgData, 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	tokenData, _ := json.Marshal(config.TokenData{
		AccessToken:  "x",
		RefreshToken: "y",
		ExpiresAt:    time.Now().Add(time.Hour).Unix(),
	})
	if err := os.WriteFile(filepath.Join(cfgDir, "token.json"), tokenData, 0600); err != nil {
		t.Fatalf("write token: %v", err)
	}
	return home
}

func runTransferCommand(t *testing.T, skillName, orgSlug string) {
	t.Helper()

	oldTransferToOrg := transferToOrg
	oldTransferSlug := transferSlug
	oldTransferYes := transferYes
	t.Cleanup(func() {
		transferToOrg = oldTransferToOrg
		transferSlug = oldTransferSlug
		transferYes = oldTransferYes
	})

	transferToOrg = orgSlug
	transferSlug = ""
	transferYes = true

	cmd := &cobra.Command{}
	cmd.Flags().Bool("to-user", false, "")
	_ = captureStdout(t, func() {
		if err := transferCmd.RunE(cmd, []string{skillName}); err != nil {
			t.Fatalf("transfer: %v", err)
		}
	})
}
