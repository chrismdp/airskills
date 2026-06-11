package cmd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// shadowForkMarker returns the marker shape an org member has after syncing
// an org skill: tracking the upstream directly, with a Source pointer.
func shadowForkMarker(upstreamID string) *SyncState {
	return &SyncState{Version: 1, Skills: map[string]*SyncEntry{
		"shared-skill": {
			SkillID:     upstreamID,
			Version:     "1.0.0",
			ContentHash: "upstream-baseline-hash",
			Tool:        "claude-code",
			OwnerKind:   "org",
			OwnerSlug:   "upstream-org",
			Source: &skillSource{
				Owner:       "upstream-org",
				Slug:        "shared-skill",
				ID:          upstreamID,
				ContentHash: "upstream-baseline-hash",
			},
		},
	}}
}

// When the marker's upstream id is stale (the org skill was transferred, so
// the tracked id is a tombstone), the server resolves forked_from to the
// successor and returns it on the created fork. The CLI must adopt that
// resolved id: the suggestion goes to the successor, and the marker's Source
// is rewritten so future syncs follow the live row.
func TestPushShadowForkAdoptsServerResolvedUpstream(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	oldIsTTY := isTTY
	isTTY = false
	oldForceSuggest := pushForceSuggest
	pushForceSuggest = true // suggestion assertions below opt in explicitly
	t.Cleanup(func() {
		isTTY = oldIsTTY
		pushForceSuggest = oldForceSuggest
	})

	staleUpstreamID := "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaa01"
	movedUpstreamID := "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaa04"
	forkID := "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaa02"

	writeEditedSkill(t, home, "shared-skill")
	if err := saveSyncState(shadowForkMarker(staleUpstreamID)); err != nil {
		t.Fatal(err)
	}

	var lastForkedFrom, lastOwnerSkillID string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/skills":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"skills":[{"id":%q,"name":"shared-skill","slug":"shared-skill","version":"1.0.0","content_hash":"upstream-baseline-hash","tool_formats":["claude-code"],"visibility":"private","dependency_count":0,"org_id":"00000000-0000-0000-0000-000000000001"}]}`, staleUpstreamID)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/me":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"id":"00000000-0000-0000-0000-000000000099","username":"callerslug"}`)
		case r.Method == http.MethodPut && r.URL.Path == "/api/v1/skills/"+staleUpstreamID+"/archive":
			http.Error(w, `{"error":"forbidden"}`, http.StatusForbidden)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/skills":
			var body map[string]interface{}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if ff, ok := body["forked_from"].(string); ok {
				lastForkedFrom = ff
			}
			// Server followed the transfer: lineage points at the successor.
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"id":%q,"name":"shared-skill","slug":"shared-skill","version":"1.0.1","content_hash":"","forked_from":%q}`, forkID, movedUpstreamID)
		case r.Method == http.MethodPut && r.URL.Path == "/api/v1/skills/"+forkID+"/archive":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"id":%q,"version":"1.0.2","content_hash":"new-fork-hash"}`, forkID)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/suggestions":
			var body map[string]interface{}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if v, ok := body["owner_skill_id"].(string); ok {
				lastOwnerSkillID = v
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"id":"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaa03","status":"pending"}`)
		default:
			t.Logf("unexpected request: %s %s", r.Method, r.URL.String())
			http.Error(w, "not handled", 404)
		}
	}))
	defer srv.Close()

	writeTestConfigAndToken(t, home, srv.URL)

	cmd := &cobra.Command{Use: "push"}
	_ = captureStdout(t, func() {
		if err := pushCmd.RunE(cmd, nil); err != nil {
			t.Fatalf("push: %v", err)
		}
	})

	if lastForkedFrom != staleUpstreamID {
		t.Errorf("fork request forked_from = %q, want the marker's id %q", lastForkedFrom, staleUpstreamID)
	}
	if lastOwnerSkillID != movedUpstreamID {
		t.Errorf("suggestion owner_skill_id = %q, want the server-resolved successor %q", lastOwnerSkillID, movedUpstreamID)
	}

	entry := loadSyncState().Skills["shared-skill"]
	if entry == nil {
		t.Fatal("marker missing after push")
	}
	// Overlay identity follows the move: the marker tracks the live
	// successor, with the backup fork referenced only via Backup.
	if entry.SkillID != movedUpstreamID {
		t.Errorf("marker SkillID = %q, want the server-resolved successor %q", entry.SkillID, movedUpstreamID)
	}
	if entry.Backup == nil || entry.Backup.SkillID != forkID {
		t.Errorf("marker Backup should reference the hidden fork %q, got %+v", forkID, entry.Backup)
	}
	if entry.Source == nil || entry.Source.ID != movedUpstreamID {
		t.Errorf("marker Source must follow the move to %q, got %+v", movedUpstreamID, entry.Source)
	}
}

// When the server rejects the fork because the upstream is gone or not
// accessible (400 "forked_from skill ..."), the backup matters more than the
// lineage: retry as a plain personal skill, upload the edit, skip the
// suggestion, and say why.
func TestPushShadowForkFallsBackToPlainCopyWhenParentUnresolvable(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	oldIsTTY := isTTY
	isTTY = false
	t.Cleanup(func() { isTTY = oldIsTTY })

	upstreamID := "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaa01"
	forkID := "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaa02"

	writeEditedSkill(t, home, "shared-skill")
	if err := saveSyncState(shadowForkMarker(upstreamID)); err != nil {
		t.Fatal(err)
	}

	var createSkillCalls, forkPutCalls, suggestionCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/skills":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"skills":[{"id":%q,"name":"shared-skill","slug":"shared-skill","version":"1.0.0","content_hash":"upstream-baseline-hash","tool_formats":["claude-code"],"visibility":"private","dependency_count":0,"org_id":"00000000-0000-0000-0000-000000000001"}]}`, upstreamID)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/me":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"id":"00000000-0000-0000-0000-000000000099","username":"callerslug"}`)
		case r.Method == http.MethodPut && r.URL.Path == "/api/v1/skills/"+upstreamID+"/archive":
			http.Error(w, `{"error":"forbidden"}`, http.StatusForbidden)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/skills":
			createSkillCalls++
			var body map[string]interface{}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if _, hasLineage := body["forked_from"]; hasLineage {
				http.Error(w, `{"error":"forked_from skill was transferred and its successor is not accessible"}`, http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"id":%q,"name":"shared-skill","slug":"shared-skill","version":"1.0.1","content_hash":""}`, forkID)
		case r.Method == http.MethodPut && r.URL.Path == "/api/v1/skills/"+forkID+"/archive":
			forkPutCalls++
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"id":%q,"version":"1.0.2","content_hash":"new-fork-hash"}`, forkID)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/suggestions":
			suggestionCalls++
			http.Error(w, "upstream is unreachable; no suggestion should be sent", 500)
		default:
			t.Logf("unexpected request: %s %s", r.Method, r.URL.String())
			http.Error(w, "not handled", 404)
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

	if createSkillCalls != 2 {
		t.Errorf("createSkill calls = %d, want 2 (rejected fork, then plain retry)", createSkillCalls)
	}
	if forkPutCalls != 1 {
		t.Errorf("backup upload calls = %d, want 1", forkPutCalls)
	}
	if suggestionCalls != 0 {
		t.Errorf("no suggestion possible against an unreachable upstream, got %d", suggestionCalls)
	}
	if strings.Contains(out, "all unchanged") {
		t.Errorf("headline must not claim 'all unchanged' after a fallback save: %q", out)
	}

	entry := loadSyncState().Skills["shared-skill"]
	if entry == nil {
		t.Fatal("marker missing after push")
	}
	if entry.SkillID != forkID {
		t.Errorf("marker must track the plain backup copy %q, got %q", forkID, entry.SkillID)
	}
	if entry.SuggestDeclined {
		t.Error("skipping an impossible suggestion is not a user decline")
	}
}

// A fork that fails outright must count as a failure in the summary headline
// — not leave the push claiming "all unchanged" while a warning below says
// the edit couldn't be saved.
func TestPushShadowForkFailureCountsAsFailedInHeadline(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	oldIsTTY := isTTY
	isTTY = false
	t.Cleanup(func() { isTTY = oldIsTTY })

	upstreamID := "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaa01"

	writeEditedSkill(t, home, "shared-skill")
	if err := saveSyncState(shadowForkMarker(upstreamID)); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/skills":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"skills":[{"id":%q,"name":"shared-skill","slug":"shared-skill","version":"1.0.0","content_hash":"upstream-baseline-hash","tool_formats":["claude-code"],"visibility":"private","dependency_count":0,"org_id":"00000000-0000-0000-0000-000000000001"}]}`, upstreamID)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/me":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"id":"00000000-0000-0000-0000-000000000099","username":"callerslug"}`)
		case r.Method == http.MethodPut && r.URL.Path == "/api/v1/skills/"+upstreamID+"/archive":
			http.Error(w, `{"error":"forbidden"}`, http.StatusForbidden)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/skills":
			http.Error(w, `{"error":"boom"}`, http.StatusInternalServerError)
		default:
			t.Logf("unexpected request: %s %s", r.Method, r.URL.String())
			http.Error(w, "not handled", 404)
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

	if strings.Contains(out, "all unchanged") {
		t.Errorf("headline claims 'all unchanged' while the fork failed: %q", out)
	}
	if !strings.Contains(out, "1 failed") {
		t.Errorf("headline should report the failed save: %q", out)
	}

	// Atomic failure: the marker still tracks the upstream, nothing lost.
	entry := loadSyncState().Skills["shared-skill"]
	if entry == nil || entry.SkillID != upstreamID {
		t.Fatalf("marker should be unchanged after a failed fork, got %+v", entry)
	}
}
