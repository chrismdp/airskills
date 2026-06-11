package cmd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// feedStdin replaces os.Stdin with a pipe pre-loaded with input, restoring
// it when the test ends. Used by tests that drive interactive prompts.
func feedStdin(t *testing.T, input string) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.WriteString(input); err != nil {
		t.Fatal(err)
	}
	w.Close()
	old := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = old })
}

// writeEditedSkill writes a local skill dir with content that differs from
// any server baseline used in these tests.
func writeEditedSkill(t *testing.T, home, name string) string {
	t.Helper()
	skillDir := filepath.Join(home, ".claude", "skills", name)
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := []byte("---\nname: " + name + "\ndescription: test\n---\n\nedited body\n")
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), body, 0o644); err != nil {
		t.Fatal(err)
	}
	return skillDir
}

// A marker that tracks a live org skill WITHOUT a Source pointer (the shape
// an older `pull --force` wrote) must not dead-end on "skill was moved" when
// the upload 403s. The skill is alive, in the caller's effective set, and
// simply not writable — push must self-heal into the same fork+suggest path
// an org-member edit takes.
func TestPushSelfHealsForbiddenOrgSkillIntoForkSuggest(t *testing.T) {
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

	upstreamID := "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaa01"
	forkID := "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaa02"
	suggestionID := "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaa03"
	orgID := "00000000-0000-0000-0000-000000000001"

	writeEditedSkill(t, home, "shared-skill")

	state := &SyncState{Version: 1, Skills: map[string]*SyncEntry{
		"shared-skill": {
			SkillID:     upstreamID, // tracking upstream directly...
			Version:     "1.0.0",
			ContentHash: "upstream-baseline-hash",
			Tool:        "claude-code",
			// ...but with NO Source — the broken pull --force shape.
		},
	}}
	if err := saveSyncState(state); err != nil {
		t.Fatal(err)
	}

	var createSkillCalls, suggestionCalls, upstreamPutCalls, forkPutCalls int
	var lastForkedFrom, lastOwnerSkillID, lastSuggesterID string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/skills":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"skills":[{"id":%q,"name":"shared-skill","slug":"shared-skill","version":"1.0.0","content_hash":"upstream-baseline-hash","tool_formats":["claude-code"],"visibility":"private","dependency_count":0,"org_id":%q}]}`, upstreamID, orgID)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/me":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"id":"00000000-0000-0000-0000-000000000099","username":"callerslug"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/organizations":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"organizations":[{"id":%q,"slug":"upstream-org","name":"Upstream","role":"member","member_count":2}]}`, orgID)
		case r.Method == http.MethodPut && r.URL.Path == "/api/v1/skills/"+upstreamID+"/archive":
			// Org member: read yes, write no.
			upstreamPutCalls++
			http.Error(w, `{"error":"forbidden"}`, http.StatusForbidden)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/skills":
			createSkillCalls++
			var body map[string]interface{}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if ff, ok := body["forked_from"].(string); ok {
				lastForkedFrom = ff
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"id":%q,"name":"shared-skill","slug":"shared-skill","version":"1.0.1","content_hash":""}`, forkID)
		case r.Method == http.MethodPut && r.URL.Path == "/api/v1/skills/"+forkID+"/archive":
			forkPutCalls++
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"id":%q,"version":"1.0.2","content_hash":"new-fork-hash"}`, forkID)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/suggestions":
			suggestionCalls++
			var body map[string]interface{}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if v, ok := body["owner_skill_id"].(string); ok {
				lastOwnerSkillID = v
			}
			if v, ok := body["suggester_skill_id"].(string); ok {
				lastSuggesterID = v
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"id":%q,"status":"pending"}`, suggestionID)
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

	if upstreamPutCalls != 1 {
		t.Errorf("expected exactly one (rejected) upload attempt at the upstream, got %d", upstreamPutCalls)
	}
	if createSkillCalls != 1 {
		t.Errorf("createSkill called %d times, want 1 (the self-healed fork)", createSkillCalls)
	}
	if lastForkedFrom != upstreamID {
		t.Errorf("fork forked_from = %q, want upstream %q", lastForkedFrom, upstreamID)
	}
	if forkPutCalls != 1 {
		t.Errorf("fork upload called %d times, want 1", forkPutCalls)
	}
	if suggestionCalls != 1 {
		t.Errorf("createSuggestion called %d times, want 1", suggestionCalls)
	}
	if lastOwnerSkillID != upstreamID || lastSuggesterID != forkID {
		t.Errorf("suggestion = %q→%q, want %q→%q", lastSuggesterID, lastOwnerSkillID, forkID, upstreamID)
	}
	if strings.Contains(out, "no longer have write access") {
		t.Errorf("output still shows the misleading moved warning: %q", out)
	}

	entry := loadSyncState().Skills["shared-skill"]
	if entry == nil {
		t.Fatal("marker missing after push")
	}
	// ONE skill: the marker keeps tracking the upstream; the backup fork
	// only ever appears inside Backup.
	if entry.SkillID != upstreamID {
		t.Errorf("marker SkillID = %q, want upstream %q", entry.SkillID, upstreamID)
	}
	if entry.Backup == nil || entry.Backup.SkillID != forkID {
		t.Errorf("marker Backup should reference the hidden fork %q, got %+v", forkID, entry.Backup)
	}
	if entry.Source == nil || entry.Source.ID != upstreamID {
		t.Errorf("marker Source must point at upstream, got %+v", entry.Source)
	}
	if entry.OwnerKind != "org" || entry.OwnerSlug != "upstream-org" {
		t.Errorf("marker owner = %q/%q, want org/upstream-org (the upstream's namespace — identity stays on the upstream)", entry.OwnerKind, entry.OwnerSlug)
	}
}

// Declining the suggestion must NOT discard the backup: the fork and upload
// happen unconditionally (transparently); the prompt only controls whether a
// suggestion is sent to the upstream owner.
func TestPushTransparentBackupDeclineStillBacksUp(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	oldIsTTY := isTTY
	isTTY = true // interactive — the suggest question is asked
	t.Cleanup(func() { isTTY = oldIsTTY })
	feedStdin(t, "n\n")

	upstreamID := "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaa01"
	forkID := "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaa02"

	writeEditedSkill(t, home, "shared-skill")

	state := &SyncState{Version: 1, Skills: map[string]*SyncEntry{
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
	if err := saveSyncState(state); err != nil {
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
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"id":%q,"name":"shared-skill","slug":"shared-skill","version":"1.0.1","content_hash":""}`, forkID)
		case r.Method == http.MethodPut && r.URL.Path == "/api/v1/skills/"+forkID+"/archive":
			forkPutCalls++
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"id":%q,"version":"1.0.2","content_hash":"new-fork-hash"}`, forkID)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/suggestions":
			suggestionCalls++
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

	if createSkillCalls != 1 || forkPutCalls != 1 {
		t.Errorf("backup must happen despite decline: createSkill=%d putArchive=%d, want 1/1", createSkillCalls, forkPutCalls)
	}
	if suggestionCalls != 0 {
		t.Errorf("declined — no suggestion should be created, got %d", suggestionCalls)
	}

	entry := loadSyncState().Skills["shared-skill"]
	if entry == nil {
		t.Fatal("marker missing after push")
	}
	if entry.SkillID != upstreamID {
		t.Errorf("marker must keep tracking the upstream %q, got %q", upstreamID, entry.SkillID)
	}
	if entry.Backup == nil || entry.Backup.SkillID != forkID {
		t.Errorf("marker Backup must reference the hidden fork %q, got %+v", forkID, entry.Backup)
	}
	if entry.SuggestionID != "" {
		t.Errorf("no suggestion was sent; SuggestionID should be empty, got %q", entry.SuggestionID)
	}
	if !entry.SuggestDeclined {
		t.Error("decline should be recorded on the marker")
	}
}

// A legacy marker stuck in the old "previous suggestion was declined"
// dead-end (tracking upstream directly + SuggestDeclined) must, on a NEW
// edit, go through the transparent backup again rather than warning and
// dropping the push.
func TestPushDeclinedShadowMarkerStillBacksUpNewEdits(t *testing.T) {
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

	upstreamID := "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaa01"
	forkID := "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaa02"

	writeEditedSkill(t, home, "shared-skill")

	state := &SyncState{Version: 1, Skills: map[string]*SyncEntry{
		"shared-skill": {
			SkillID:         upstreamID,
			Version:         "1.0.0",
			ContentHash:     "upstream-baseline-hash",
			Tool:            "claude-code",
			OwnerKind:       "org",
			OwnerSlug:       "upstream-org",
			SuggestDeclined: true, // old-flow decline
			Source: &skillSource{
				Owner:       "upstream-org",
				Slug:        "shared-skill",
				ID:          upstreamID,
				ContentHash: "upstream-baseline-hash",
			},
		},
	}}
	if err := saveSyncState(state); err != nil {
		t.Fatal(err)
	}

	var createSkillCalls, suggestionCalls int
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
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"id":%q,"name":"shared-skill","slug":"shared-skill","version":"1.0.1","content_hash":""}`, forkID)
		case r.Method == http.MethodPut && r.URL.Path == "/api/v1/skills/"+forkID+"/archive":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"id":%q,"version":"1.0.2","content_hash":"new-fork-hash"}`, forkID)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/suggestions":
			suggestionCalls++
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

	if createSkillCalls != 1 {
		t.Errorf("new edits after a past decline must still be backed up: createSkill=%d, want 1", createSkillCalls)
	}
	if suggestionCalls != 1 {
		t.Errorf("headless re-push of new edits should suggest again, got %d suggestions", suggestionCalls)
	}
	entry := loadSyncState().Skills["shared-skill"]
	if entry == nil || entry.SkillID != upstreamID {
		t.Fatalf("marker should keep tracking the upstream after backup, got %+v", entry)
	}
	if entry.Backup == nil || entry.Backup.SkillID != forkID {
		t.Fatalf("marker Backup should reference the hidden fork, got %+v", entry.Backup)
	}
}

// After declining once on a real fork, NEW edits must re-offer the
// suggestion question — a past decline applies to those bytes, not forever.
func TestPushReoffersSuggestionAfterDeclineWhenEdited(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	oldIsTTY := isTTY
	isTTY = true
	t.Cleanup(func() { isTTY = oldIsTTY })
	feedStdin(t, "y\n\n") // suggest: yes; message: empty

	upstreamID := "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaa01"
	forkID := "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaa02"
	myID := "00000000-0000-0000-0000-000000000099"

	writeEditedSkill(t, home, "my-fork")

	state := &SyncState{Version: 1, Skills: map[string]*SyncEntry{
		"my-fork": {
			SkillID:         forkID, // a real fork the caller owns
			Version:         "1.0.0",
			ContentHash:     "old-fork-hash",
			Tool:            "claude-code",
			OwnerKind:       "user",
			OwnerSlug:       "callerslug",
			SuggestDeclined: true, // declined for a previous edit
			Source: &skillSource{
				Owner:       "alice",
				Slug:        "my-fork",
				ID:          upstreamID,
				ContentHash: "upstream-baseline-hash",
			},
		},
	}}
	if err := saveSyncState(state); err != nil {
		t.Fatal(err)
	}

	var suggestionCalls int
	var lastSuggesterID, lastOwnerSkillID string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/skills":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"skills":[{"id":%q,"name":"my-fork","slug":"my-fork","version":"1.0.0","content_hash":"old-fork-hash","tool_formats":["claude-code"],"visibility":"private","dependency_count":0,"owner_id":%q}]}`, forkID, myID)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/me":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"id":%q,"username":"callerslug"}`, myID)
		case r.Method == http.MethodPut && r.URL.Path == "/api/v1/skills/"+forkID+"/archive":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"id":%q,"version":"1.0.1","content_hash":"new-fork-hash"}`, forkID)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/suggestions":
			suggestionCalls++
			var body map[string]interface{}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if v, ok := body["suggester_skill_id"].(string); ok {
				lastSuggesterID = v
			}
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

	if suggestionCalls != 1 {
		t.Fatalf("new edits must re-offer the suggestion despite an old decline; got %d suggestion calls", suggestionCalls)
	}
	if lastSuggesterID != forkID || lastOwnerSkillID != upstreamID {
		t.Errorf("suggestion = %q→%q, want %q→%q", lastSuggesterID, lastOwnerSkillID, forkID, upstreamID)
	}
}

// An org owner/admin editing a synced org skill must update it IN PLACE.
// The CLI can't see roles, so push tries the direct write first — the
// server's accept/403 is the role check. No fork, no suggestion, and no
// suggest prompt afterwards (you don't suggest to a skill you just wrote).
func TestPushAdminDirectWriteUpdatesOrgSkillInPlace(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	oldIsTTY := isTTY
	isTTY = true // interactive: a wrongly-fired suggest prompt would read EOF and record a decline
	t.Cleanup(func() { isTTY = oldIsTTY })
	feedStdin(t, "")

	upstreamID := "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaa01"

	writeEditedSkill(t, home, "shared-skill")

	state := &SyncState{Version: 1, Skills: map[string]*SyncEntry{
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
	if err := saveSyncState(state); err != nil {
		t.Fatal(err)
	}

	var upstreamPutCalls, createSkillCalls, suggestionCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/skills":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"skills":[{"id":%q,"name":"shared-skill","slug":"shared-skill","version":"1.0.0","content_hash":"upstream-baseline-hash","tool_formats":["claude-code"],"visibility":"private","dependency_count":0,"org_id":"00000000-0000-0000-0000-000000000001"}]}`, upstreamID)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/me":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"id":"00000000-0000-0000-0000-000000000099","username":"adminslug"}`)
		case r.Method == http.MethodPut && r.URL.Path == "/api/v1/skills/"+upstreamID+"/archive":
			// Admin: the server authorizes the direct write.
			upstreamPutCalls++
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"id":%q,"version":"1.1.0","content_hash":"new-org-hash"}`, upstreamID)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/skills":
			createSkillCalls++
			http.Error(w, "should not fork for an admin", 500)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/suggestions":
			suggestionCalls++
			http.Error(w, "should not suggest for an admin", 500)
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

	if upstreamPutCalls != 1 {
		t.Errorf("admin push must write the org skill in place: upstream PUTs = %d, want 1", upstreamPutCalls)
	}
	if createSkillCalls != 0 || suggestionCalls != 0 {
		t.Errorf("admin push must not fork or suggest: createSkill=%d suggestions=%d", createSkillCalls, suggestionCalls)
	}

	entry := loadSyncState().Skills["shared-skill"]
	if entry == nil {
		t.Fatal("marker missing after push")
	}
	if entry.SkillID != upstreamID {
		t.Errorf("marker must stay on the org skill, got %q", entry.SkillID)
	}
	if entry.ContentHash != "new-org-hash" || entry.Version != "1.1.0" {
		t.Errorf("marker should record the accepted write, got hash=%q version=%q", entry.ContentHash, entry.Version)
	}
	if entry.SuggestionID != "" || entry.SuggestDeclined {
		t.Errorf("no suggest prompt should fire after writing the skill itself: SuggestionID=%q SuggestDeclined=%v",
			entry.SuggestionID, entry.SuggestDeclined)
	}
}

// A marker still tracking its hidden backup fork directly (the broken
// pre-overlay shape from the 2026-06-11 field report) must be healed by
// push using the listing already in hand: the edit routes down the member
// backup path — uploaded to the backup fork, marker flipped to track the
// upstream — instead of the explicit-fork path (which uploaded to the
// backup row as if it were a first-class personal skill and then dangled
// an interactive suggestion prompt).
func TestPushHealsMarkerTrackingBackupRow(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	oldIsTTY := isTTY
	isTTY = false
	t.Cleanup(func() { isTTY = oldIsTTY })

	upstreamID := "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaa01"
	forkID := "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaa02"
	orgID := "00000000-0000-0000-0000-000000000001"

	writeEditedSkill(t, home, "home")

	state := &SyncState{Version: 1, Skills: map[string]*SyncEntry{
		"home": {
			SkillID:      forkID, // the broken shape: tracking the backup row
			Version:      "1.0.6",
			ContentHash:  "stale-local-hash",
			Tool:         "claude-code",
			OwnerKind:    "org",
			OwnerSlug:    "parsons-home",
			ResolvedHash: "upstream-hash",
			Source: &skillSource{
				Owner:               "parsons-home",
				Slug:                "home",
				ID:                  upstreamID,
				UpstreamSkillID:     upstreamID,
				UpstreamContentHash: "upstream-hash",
				ContentHash:         "upstream-hash",
			},
		},
	}}
	if err := saveSyncState(state); err != nil {
		t.Fatal(err)
	}

	var upstreamPutCalls, forkPutCalls, createSkillCalls, suggestionCalls int

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/skills":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"skills":[
				{"id":%q,"name":"home","slug":"home","version":"1.0.5","content_hash":"upstream-hash","tool_formats":["claude-code"],"visibility":"private","dependency_count":0,"org_id":%q},
				{"id":%q,"name":"home","slug":"home","version":"1.0.6","content_hash":"stale-backup-hash","tool_formats":["claude-code"],"visibility":"private","dependency_count":0,"forked_from":%q,"backup":true}
			]}`, upstreamID, orgID, forkID, upstreamID)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/me":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"id":"00000000-0000-0000-0000-000000000099","username":"poppinsparsons"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/organizations":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"organizations":[{"id":%q,"slug":"parsons-home","name":"Parsons Home","role":"member","member_count":2}]}`, orgID)
		case r.Method == http.MethodPut && r.URL.Path == "/api/v1/skills/"+upstreamID+"/archive":
			upstreamPutCalls++
			http.Error(w, `{"error":"forbidden"}`, http.StatusForbidden)
		case r.Method == http.MethodPut && r.URL.Path == "/api/v1/skills/"+forkID+"/archive":
			forkPutCalls++
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"id":%q,"version":"1.0.7","content_hash":"new-backup-hash"}`, forkID)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/skills":
			createSkillCalls++
			http.Error(w, `{"error":"should not create"}`, 500)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/suggestions":
			suggestionCalls++
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
	captureStdout(t, func() {
		if err := pushCmd.RunE(cmd, nil); err != nil {
			t.Fatalf("push: %v", err)
		}
	})

	if upstreamPutCalls != 0 {
		t.Errorf("member push must not upload to the upstream, got %d attempts", upstreamPutCalls)
	}
	if createSkillCalls != 0 {
		t.Errorf("no new skill may be created — the backup fork already exists, got %d creates", createSkillCalls)
	}
	if forkPutCalls != 1 {
		t.Errorf("the edit should be uploaded to the existing backup fork once, got %d", forkPutCalls)
	}

	entry := loadSyncState().Skills["home"]
	if entry == nil {
		t.Fatal("marker missing after push")
	}
	if entry.SkillID != upstreamID {
		t.Errorf("marker must heal to track the upstream, got SkillID=%q", entry.SkillID)
	}
	if entry.Backup == nil || entry.Backup.SkillID != forkID {
		t.Errorf("Backup must reference the fork, got %+v", entry.Backup)
	}
	// Headless without --force-suggest: the suggest decision is DEFERRED —
	// an agent must never auto-fire a suggestion at the upstream owner.
	if suggestionCalls != 0 {
		t.Errorf("headless push without --force-suggest must not create a suggestion, got %d", suggestionCalls)
	}
	if entry.SuggestDeclined {
		t.Error("deferral is not a decline — the question must be re-offered")
	}
}

// --force-suggest is the explicit non-interactive submit: same member
// overlay edit as above, but the suggestion goes out with no prompt
// (field report 2026-06-11, bug 2: the flag previously left the decision
// dangling at an interactive prompt that never came).
func TestPushForceSuggestSubmitsHeadless(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	oldIsTTY := isTTY
	isTTY = false
	oldForceSuggest := pushForceSuggest
	pushForceSuggest = true
	t.Cleanup(func() {
		isTTY = oldIsTTY
		pushForceSuggest = oldForceSuggest
	})

	upstreamID := "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaa01"
	forkID := "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaa02"
	orgID := "00000000-0000-0000-0000-000000000001"

	writeEditedSkill(t, home, "home")

	state := &SyncState{Version: 1, Skills: map[string]*SyncEntry{
		"home": {
			SkillID:     upstreamID,
			Version:     "1.0.5",
			ContentHash: "upstream-hash",
			Tool:        "claude-code",
			OwnerKind:   "org",
			OwnerSlug:   "parsons-home",
			Source: &skillSource{
				Owner:               "parsons-home",
				Slug:                "home",
				ID:                  upstreamID,
				UpstreamSkillID:     upstreamID,
				UpstreamContentHash: "upstream-hash",
				ContentHash:         "upstream-hash",
			},
		},
	}}
	if err := saveSyncState(state); err != nil {
		t.Fatal(err)
	}

	var suggestionCalls int

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/skills":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"skills":[{"id":%q,"name":"home","slug":"home","version":"1.0.5","content_hash":"upstream-hash","tool_formats":["claude-code"],"visibility":"private","dependency_count":0,"org_id":%q}]}`, upstreamID, orgID)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/me":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"id":"00000000-0000-0000-0000-000000000099","username":"poppinsparsons"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/organizations":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"organizations":[{"id":%q,"slug":"parsons-home","name":"Parsons Home","role":"member","member_count":2}]}`, orgID)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/skills":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"id":%q,"name":"home","slug":"home","version":"1.0.6","content_hash":""}`, forkID)
		case r.Method == http.MethodPut && r.URL.Path == "/api/v1/skills/"+forkID+"/archive":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"id":%q,"version":"1.0.7","content_hash":"new-backup-hash"}`, forkID)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/suggestions":
			suggestionCalls++
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
	captureStdout(t, func() {
		if err := pushCmd.RunE(cmd, nil); err != nil {
			t.Fatalf("push: %v", err)
		}
	})

	if suggestionCalls != 1 {
		t.Errorf("--force-suggest must submit the suggestion non-interactively, got %d calls", suggestionCalls)
	}
	entry := loadSyncState().Skills["home"]
	if entry == nil || entry.SuggestionID == "" {
		t.Errorf("marker should record the suggestion id, got %+v", entry)
	}
}
