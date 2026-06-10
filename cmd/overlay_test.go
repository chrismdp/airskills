package cmd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// writeLocalSkillDir creates a local skill dir under $HOME/.claude/skills
// and returns (path, merkle hash of its content).
func writeLocalSkillDir(t *testing.T, home, name, body string) (string, string) {
	t.Helper()
	dir := filepath.Join(home, ".claude", "skills", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := []byte("---\nname: " + name + "\ndescription: test\n---\n\n" + body + "\n")
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), content, 0o644); err != nil {
		t.Fatal(err)
	}
	return dir, computeMerkleHash(readSkillFiles(dir))
}

func overlayMarker(upstreamID, localHash, baseline string) *SyncEntry {
	return &SyncEntry{
		SkillID:     upstreamID,
		Version:     "1.0.0",
		ContentHash: localHash,
		Tool:        "claude-code",
		OwnerKind:   "org",
		OwnerSlug:   "upstream-org",
		Source: &skillSource{
			Owner:               "upstream-org",
			Slug:                "shared-skill",
			ID:                  upstreamID,
			ContentHash:         baseline,
			UpstreamSkillID:     upstreamID,
			UpstreamContentHash: baseline,
		},
	}
}

// A diverged overlay with an unchanged upstream is the overlay's NORMAL
// state — pull must not park a conflict, must not install the upstream
// over the user's edits, and must not flag a divergence. This is the
// one-skill model the field report asked for.
func TestDecidePullActionsOverlayDivergedIsNoAction(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	dir, localHash := writeLocalSkillDir(t, home, "shared-skill", "edited body")
	upstreamID := testUUID("upstream-1").String()

	state := &SyncState{Version: 1, Skills: map[string]*SyncEntry{
		"shared-skill": overlayMarker(upstreamID, localHash, "upstream-hash"),
	}}
	remote := []apiSkill{
		{Id: testUUID("upstream-1"), Name: "shared-skill", Slug: "shared-skill", Version: "1.0.0", ContentHash: strPtr("upstream-hash")},
	}
	local := map[string]string{"shared-skill": dir}

	actions, warnings, diverged := decidePullActions(remote, local, state, nil)
	if len(actions) != 0 {
		t.Errorf("diverged overlay should produce no pull action, got %+v", actions)
	}
	if len(diverged) != 0 {
		t.Errorf("diverged overlay must not register a conflict, got %v", diverged)
	}
	if len(warnings) != 0 {
		t.Errorf("unexpected warnings: %v", warnings)
	}
}

// A byte-converged overlay reconciles silently (auto-resolved) so the
// marker hash catches up and the sweep can retire a redundant backup.
func TestDecidePullActionsOverlayConvergedAutoResolves(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	dir, localHash := writeLocalSkillDir(t, home, "shared-skill", "same body")
	upstreamID := testUUID("upstream-1").String()

	state := &SyncState{Version: 1, Skills: map[string]*SyncEntry{
		// Marker hash is stale (an old edit), but disk now equals upstream.
		"shared-skill": overlayMarker(upstreamID, "stale-local-hash", "old-baseline"),
	}}
	remote := []apiSkill{
		{Id: testUUID("upstream-1"), Name: "shared-skill", Slug: "shared-skill", Version: "1.0.1", ContentHash: strPtr(localHash)},
	}
	local := map[string]string{"shared-skill": dir}

	actions, _, diverged := decidePullActions(remote, local, state, nil)
	if len(actions) != 1 || actions[0].reason != "auto-resolved" {
		t.Fatalf("converged overlay should auto-resolve, got %+v", actions)
	}
	if len(diverged) != 0 {
		t.Errorf("no conflict expected, got %v", diverged)
	}
}

// Upstream advanced + overlay diverged = the 3-way state: surface as
// upstream-advanced (incoming), never fast-forward over the user's edits.
// Upstream advanced + overlay clean (local == BASELINE, not marker hash) =
// safe fast-forward.
func TestDecidePullActionsOverlayUpstreamAdvanced(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	dir, localHash := writeLocalSkillDir(t, home, "shared-skill", "edited body")
	upstreamID := testUUID("upstream-1").String()

	state := &SyncState{Version: 1, Skills: map[string]*SyncEntry{
		"shared-skill": overlayMarker(upstreamID, localHash, "old-baseline"),
	}}
	remote := []apiSkill{
		{Id: testUUID("upstream-1"), Name: "shared-skill", Slug: "shared-skill", Version: "2.0.0", ContentHash: strPtr("new-upstream-hash")},
	}
	local := map[string]string{"shared-skill": dir}

	actions, _, _ := decidePullActions(remote, local, state, nil)
	if len(actions) != 1 || actions[0].reason != "upstream-advanced" {
		t.Fatalf("diverged overlay + advanced upstream should be upstream-advanced, got %+v", actions)
	}

	// Clean overlay: local equals the BASELINE the user last incorporated.
	state.Skills["shared-skill"] = overlayMarker(upstreamID, localHash, localHash)
	actions, _, _ = decidePullActions(remote, local, state, nil)
	if len(actions) != 1 || actions[0].reason != "upstream-updated" {
		t.Fatalf("clean overlay + advanced upstream should fast-forward, got %+v", actions)
	}
}

// A legacy marker that tracks the backup fork row directly (the old
// shadow-fork shape, flagged backup=true by the server backfill) heals
// onto the overlay shape: SkillID flips to the upstream, the fork id moves
// into Backup, SuggestDeclined survives verbatim.
func TestHealOverlayMarkersFlipsLegacyForkMarkers(t *testing.T) {
	forkID := testUUID("fork-1")
	upstreamID := testUUID("upstream-1")
	orgID := testUUID("org-1")

	state := &SyncState{Version: 1, Skills: map[string]*SyncEntry{
		"shared-skill": {
			SkillID:         forkID.String(),
			ContentHash:     "local-edit-hash",
			Tool:            "claude-code",
			OwnerKind:       "user",
			OwnerSlug:       "callerslug",
			SuggestDeclined: true,
			SuggestionID:    "11111111-1111-1111-1111-111111111111",
			Source: &skillSource{
				Owner:               "upstream-org",
				Slug:                "shared-skill",
				ID:                  upstreamID.String(),
				ContentHash:         "old-baseline",
				UpstreamSkillID:     upstreamID.String(),
				UpstreamContentHash: "old-baseline",
			},
		},
	}}

	remoteByID := map[string]*apiSkill{
		forkID.String(): {
			Id: forkID, Name: "shared-skill", Slug: "shared-skill",
			ContentHash: strPtr("local-edit-hash"),
			ForkedFrom:  &upstreamID,
			Backup:      true,
		},
		upstreamID.String(): {
			Id: upstreamID, Name: "shared-skill", Slug: "shared-skill",
			Version:     "1.2.0",
			ContentHash: strPtr("current-upstream-hash"),
			OrgId:       &orgID,
		},
	}

	healed := healOverlayMarkers(state, remoteByID, newOwnerResolver(nil))
	if len(healed) != 1 {
		t.Fatalf("expected 1 healed marker, got %v", healed)
	}
	entry := state.Skills["shared-skill"]
	if entry.SkillID != upstreamID.String() {
		t.Errorf("SkillID = %q, want upstream %q", entry.SkillID, upstreamID.String())
	}
	if entry.Backup == nil || entry.Backup.SkillID != forkID.String() {
		t.Errorf("Backup should reference the fork, got %+v", entry.Backup)
	}
	if entry.Backup != nil && entry.Backup.ContentHash != "local-edit-hash" {
		t.Errorf("Backup hash = %q, want local-edit-hash", entry.Backup.ContentHash)
	}
	if !entry.SuggestDeclined {
		t.Error("SuggestDeclined must survive healing verbatim")
	}
	if entry.SuggestionID == "" {
		t.Error("SuggestionID must survive healing verbatim")
	}
	// The baseline the user last incorporated must NOT silently advance to
	// the current upstream hash.
	if got := sourceBaselineHash(entry.Source); got != "old-baseline" {
		t.Errorf("baseline = %q, want old-baseline preserved", got)
	}

	// Idempotent: a second pass finds nothing to heal.
	if again := healOverlayMarkers(state, remoteByID, newOwnerResolver(nil)); len(again) != 0 {
		t.Errorf("healing must be idempotent, got %v", again)
	}
}

// A fresh device with the local dir already present (e.g. via git) and a
// backup row in the listing rebuilds the OVERLAY marker — never a visible
// second skill tracked at the fork.
func TestReconstructOverlayMarkersAttachesBackup(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	dir, _ := writeLocalSkillDir(t, home, "shared-skill", "edited body")
	forkID := testUUID("fork-1")
	upstreamID := testUUID("upstream-1")
	orgID := testUUID("org-1")

	state := &SyncState{Version: 1, Skills: map[string]*SyncEntry{}}
	backupRows := []apiSkill{
		{
			Id: forkID, Name: "shared-skill", Slug: "shared-skill", Version: "1.0.1",
			ContentHash: strPtr("backup-hash"), ForkedFrom: &upstreamID, Backup: true,
		},
	}
	remoteByID := map[string]*apiSkill{
		upstreamID.String(): {
			Id: upstreamID, Name: "shared-skill", Slug: "shared-skill", Version: "1.2.0",
			ContentHash: strPtr("upstream-hash"), OrgId: &orgID,
		},
	}
	local := map[string]string{"shared-skill": dir}

	rebuilt := reconstructOverlayMarkers(nil, state, backupRows, remoteByID, local, newOwnerResolver(nil))
	if len(rebuilt) != 1 {
		t.Fatalf("expected 1 rebuilt overlay, got %v", rebuilt)
	}
	entry := state.Skills["shared-skill"]
	if entry == nil {
		t.Fatal("marker missing after reconstruction")
	}
	if entry.SkillID != upstreamID.String() {
		t.Errorf("SkillID = %q, want upstream %q (one skill, not two)", entry.SkillID, upstreamID.String())
	}
	if entry.Backup == nil || entry.Backup.SkillID != forkID.String() {
		t.Errorf("Backup should reference the fork, got %+v", entry.Backup)
	}
	if entry.Source == nil || sourceUpstreamID(entry.Source) != upstreamID.String() {
		t.Errorf("Source should pin the upstream, got %+v", entry.Source)
	}
}

// The convergence sweep retires a backup whose CONTENT equals the
// upstream's (suggestion accepted / owner converged): fork deleted,
// pending suggestion withdrawn, Backup cleared. The guard is
// backup==upstream, NOT local==upstream — after `add --force`
// (incorporate) the backup still holds the user's only copy of their
// proposal and must survive.
func TestSweepOverlayBackups(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	forkID := testUUID("fork-1")
	upstreamID := testUUID("upstream-1")
	suggestionID := "33333333-3333-3333-3333-333333333333"

	var deleted, withdrawn int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodDelete && r.URL.Path == "/api/v1/skills/"+forkID.String():
			deleted++
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"status":"deleted"}`)
		case r.Method == http.MethodPut && r.URL.Path == "/api/v1/suggestions/"+suggestionID:
			withdrawn++
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"id":%q,"status":"withdrawn"}`, suggestionID)
		default:
			http.Error(w, "not handled", 404)
		}
	}))
	defer srv.Close()
	client := &apiClient{baseURL: srv.URL, token: "test-token", http: srv.Client()}

	// Redundant backup: content equals upstream.
	entry := overlayMarker(upstreamID.String(), "converged-hash", "converged-hash")
	entry.Backup = &backupRef{SkillID: forkID.String(), ContentHash: "converged-hash"}
	entry.SuggestionID = suggestionID
	state := &SyncState{Version: 1, Skills: map[string]*SyncEntry{"shared-skill": entry}}
	remoteByID := map[string]*apiSkill{
		upstreamID.String(): {Id: upstreamID, Name: "shared-skill", Slug: "shared-skill", ContentHash: strPtr("converged-hash")},
	}

	sweepOverlayBackups(client, state, remoteByID, newOwnerResolver(nil))
	if deleted != 1 || withdrawn != 1 {
		t.Errorf("redundant backup: deleted=%d withdrawn=%d, want 1/1", deleted, withdrawn)
	}
	if entry.Backup != nil || entry.SuggestionID != "" {
		t.Errorf("backup/suggestion should be cleared, got %+v / %q", entry.Backup, entry.SuggestionID)
	}

	// Post-incorporate shape: local == upstream but the backup still holds
	// the user's pre-incorporate proposal. It must stay, suggestion open.
	deleted, withdrawn = 0, 0
	entry2 := overlayMarker(upstreamID.String(), "upstream-hash", "upstream-hash")
	entry2.Backup = &backupRef{SkillID: forkID.String(), ContentHash: "proposal-hash"}
	entry2.SuggestionID = suggestionID
	state2 := &SyncState{Version: 1, Skills: map[string]*SyncEntry{"shared-skill": entry2}}
	remoteByID2 := map[string]*apiSkill{
		upstreamID.String(): {Id: upstreamID, Name: "shared-skill", Slug: "shared-skill", ContentHash: strPtr("upstream-hash")},
	}
	sweepOverlayBackups(client, state2, remoteByID2, newOwnerResolver(nil))
	if deleted != 0 || withdrawn != 0 {
		t.Errorf("proposal backup must survive incorporate: deleted=%d withdrawn=%d, want 0/0", deleted, withdrawn)
	}
	if entry2.Backup == nil || entry2.SuggestionID != suggestionID {
		t.Errorf("backup/suggestion must be kept, got %+v / %q", entry2.Backup, entry2.SuggestionID)
	}
}

// Upstream genuinely lost (gone from the listing AND 404 on direct read,
// no moved_to): the backup is promoted to a visible personal skill the
// marker flips to — the user's edits are never stranded.
func TestSweepPromotesBackupWhenUpstreamLost(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	forkID := testUUID("fork-1")
	upstreamID := testUUID("upstream-1")

	var promoted int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/skills/"+upstreamID.String():
			http.Error(w, `{"error":"not found"}`, 404)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/resolve/upstream-org/shared-skill":
			http.Error(w, `{"error":"not found"}`, 404)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/me":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"id":"00000000-0000-0000-0000-000000000099","username":"callerslug"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/organizations":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"organizations":[]}`)
		case r.Method == http.MethodPut && r.URL.Path == "/api/v1/skills/"+forkID.String():
			var body map[string]interface{}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if v, ok := body["backup"].(bool); ok && !v {
				promoted++
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"id":%q,"backup":false}`, forkID)
		default:
			http.Error(w, "not handled", 404)
		}
	}))
	defer srv.Close()
	client := &apiClient{baseURL: srv.URL, token: "test-token", http: srv.Client()}

	entry := overlayMarker(upstreamID.String(), "edit-hash", "old-baseline")
	entry.Backup = &backupRef{SkillID: forkID.String(), ContentHash: "edit-hash"}
	state := &SyncState{Version: 1, Skills: map[string]*SyncEntry{"shared-skill": entry}}

	sweepOverlayBackups(client, state, map[string]*apiSkill{}, newOwnerResolver(client))

	if promoted != 1 {
		t.Fatalf("expected the backup row to be promoted (backup:false PUT), got %d", promoted)
	}
	if entry.SkillID != forkID.String() {
		t.Errorf("marker should flip to the promoted skill, got %q", entry.SkillID)
	}
	if entry.Backup != nil {
		t.Errorf("Backup should be cleared after promotion, got %+v", entry.Backup)
	}
	if entry.OwnerKind != "user" || entry.OwnerSlug != "callerslug" {
		t.Errorf("owner = %q/%q, want user/callerslug", entry.OwnerKind, entry.OwnerSlug)
	}
	if entry.Source == nil {
		t.Error("Source must be kept for lineage")
	}
}
