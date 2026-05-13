package cmd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// writeTestSkill creates a SKILL.md in dir with the given body and returns
// its merkle hash. Used by classifier tests to set up either "clean local"
// (hash == marker) or "edited local" (hash != marker) scenarios.
func writeTestSkill(t *testing.T, dir, body string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return computeMerkleHash(readSkillFiles(dir))
}

func TestClassifySkippedMarker(t *testing.T) {
	skillID := "11111111-1111-1111-1111-111111111111"

	// Common server: route by URL + status to simulate each kind.
	// Each subtest overrides the handler.
	cases := []struct {
		name       string
		handler    http.HandlerFunc
		bodyOnDisk string
		markerHash string // overrides clean hash if non-empty
		wantKind   skippedActionKind
		wantOwner  string
		wantSkill  string
	}{
		{
			name: "orphan + clean local → remove",
			handler: func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, `{"error":"not found"}`, 404)
			},
			bodyOnDisk: "---\nname: foo\ndescription: t\n---\nbody",
			wantKind:   actionOrphanRemove,
		},
		{
			name: "orphan + edited local → keep dir, drop marker",
			handler: func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, `{"error":"not found"}`, 404)
			},
			bodyOnDisk: "---\nname: foo\ndescription: t\n---\nbody",
			markerHash: "DELIBERATELY_DIFFERENT_HASH",
			wantKind:   actionOrphanKeep,
		},
		{
			name: "moved to org live skill detail shape → keep dir",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(map[string]interface{}{
					"id":             skillID,
					"owner_id":       nil,
					"org_id":         "22222222-2222-2222-2222-222222222222",
					"name":           "foo-new",
					"slug":           "foo-new",
					"visibility":     "private",
					"version":        "1.0.0",
					"content_hash":   "abc123",
					"archive_size":   123,
					"files":          []map[string]string{{"path": "SKILL.md"}},
					"dependencies":   []map[string]string{},
					"owner":          nil,
					"org":            map[string]string{"slug": "neworg", "name": "New Org"},
					"deleted_at":     nil,
					"forked_from":    nil,
					"head_commit_id": nil,
				})
			},
			bodyOnDisk: "---\nname: foo\ndescription: t\n---\nbody",
			wantKind:   actionMovedKeep,
			wantOwner:  "neworg",
			wantSkill:  "foo-new",
		},
		{
			name: "moved to different user → keep dir",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(map[string]interface{}{
					"slug":  "foo",
					"owner": map[string]string{"username": "alice"},
				})
			},
			bodyOnDisk: "---\nname: foo\ndescription: t\n---\nbody",
			wantKind:   actionMovedKeep,
			wantOwner:  "alice",
			wantSkill:  "foo",
		},
		{
			name: "successful detail without owner namespace → transient",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(map[string]interface{}{
					"slug": "foo",
				})
			},
			bodyOnDisk: "---\nname: foo\ndescription: t\n---\nbody",
			wantKind:   actionTransient,
		},
		{
			name: "successful detail without slug → transient",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(map[string]interface{}{
					"org": map[string]string{"slug": "neworg"},
				})
			},
			bodyOnDisk: "---\nname: foo\ndescription: t\n---\nbody",
			wantKind:   actionTransient,
		},
		{
			name: "server 500 → transient (no destructive action)",
			handler: func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, `{"error":"oops"}`, 500)
			},
			bodyOnDisk: "---\nname: foo\ndescription: t\n---\nbody",
			wantKind:   actionTransient,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(tc.handler)
			defer srv.Close()

			client := &apiClient{baseURL: srv.URL, token: "test", http: srv.Client()}

			dir := filepath.Join(t.TempDir(), "foo")
			cleanHash := writeTestSkill(t, dir, tc.bodyOnDisk)

			markerHash := cleanHash
			if tc.markerHash != "" {
				markerHash = tc.markerHash
			}
			marker := &SyncEntry{SkillID: skillID, ContentHash: markerHash}

			got := classifySkippedMarker(client, "foo", marker, dir)

			if got.kind != tc.wantKind {
				t.Errorf("kind: want %d, got %d", tc.wantKind, got.kind)
			}
			if got.name != "foo" {
				t.Errorf("name: want %q, got %q", "foo", got.name)
			}
			if got.localDir != dir {
				t.Errorf("localDir: want %q, got %q", dir, got.localDir)
			}
			if tc.wantOwner != "" && got.newOwnerSlug != tc.wantOwner {
				t.Errorf("newOwnerSlug: want %q, got %q", tc.wantOwner, got.newOwnerSlug)
			}
			if tc.wantSkill != "" && got.newSkillSlug != tc.wantSkill {
				t.Errorf("newSkillSlug: want %q, got %q", tc.wantSkill, got.newSkillSlug)
			}
		})
	}
}
