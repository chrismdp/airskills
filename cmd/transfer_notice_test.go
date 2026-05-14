package cmd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCollectMovedSourceNotices(t *testing.T) {
	oldID := testUUID("old-transfer-id").String()
	newID := testUUID("new-transfer-id")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/resolve/alice/widgets":
			w.WriteHeader(http.StatusGone)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"error": "skill transferred",
				"moved_to": map[string]string{
					"kind":       "org",
					"slug":       "acme-co",
					"skill_slug": "widgets",
					"skill_id":   newID.String(),
				},
			})
		case "/api/v1/resolve/alice/private-widgets":
			w.WriteHeader(http.StatusGone)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "skill transferred"})
		default:
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]string{"id": oldID})
		}
	}))
	defer srv.Close()

	client := &apiClient{baseURL: srv.URL, token: "test", http: srv.Client()}
	state := &SyncState{Version: 1, Skills: map[string]*SyncEntry{
		"widgets": {
			SkillID: oldID,
			Source:  &skillSource{Owner: "alice", Slug: "widgets", ID: oldID},
		},
		"private-widgets": {
			SkillID: oldID,
			Source:  &skillSource{Owner: "alice", Slug: "private-widgets", ID: oldID},
		},
	}}
	remote := []apiSkill{{Id: newID, Name: "widgets", Slug: "widgets"}}

	notices := collectMovedSourceNotices(client, state, remote)
	if len(notices) != 2 {
		t.Fatalf("expected 2 notices, got %d: %+v", len(notices), notices)
	}
	if !notices[0].inSyncSurface {
		t.Fatalf("expected first notice to identify the new skill in the sync surface")
	}
	if got := formatMovedSourceNotice(notices[0], true); !strings.Contains(got, "airskills rm widgets") || !strings.Contains(got, "alice/widgets") || !strings.Contains(got, "acme-co/widgets") {
		t.Fatalf("sync-surface notice did not name old/new paths and rm action:\n%s", got)
	}
	if got := formatMovedSourceNotice(notices[1], true); !strings.Contains(got, "upstream archived") || strings.Contains(got, "acme-co") {
		t.Fatalf("private moved notice should not leak destination:\n%s", got)
	}
}
