package cmd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/chrismdp/airskills/config"
)

// The core of `pull --keep-local`: adopting a conflicting skillset skill as
// the upstream of the user's LOCAL copy must (a) keep local bytes untouched
// and (b) silence the recurring conflict. The marker records the server's
// current hash as the baseline, so decidePullActions goes quiet — without
// the local dir's differing bytes being overwritten or re-flagged.
func TestKeepLocalEntryQuietsUntrackedConflict(t *testing.T) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "home")
	if err := os.MkdirAll(skillDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("# my local home"), 0644); err != nil {
		t.Fatal(err)
	}

	state := &SyncState{Version: 1, Skills: map[string]*SyncEntry{}}
	remote := []apiSkill{
		{Id: testUUID("skill-home"), Name: "home", Version: "1.0.0", ContentHash: strPtr("server-hash")},
	}
	local := map[string]string{"home": skillDir}

	// Baseline: an untracked local dir + same-named server skill with
	// different bytes is an untracked-conflict (the recurring warning).
	actions, _, _ := decidePullActions(remote, local, state, nil)
	if len(actions) != 1 || actions[0].reason != "untracked-conflict" {
		t.Fatalf("expected untracked-conflict baseline, got %+v", actions)
	}

	// Adopt: keep local, track against the server skill. Own skill → no Source.
	state.Skills["home"] = buildKeepLocalEntry(remote[0], "user", "chris", nil)

	// Now quiet: the skill no longer queues a diverged/untracked-conflict action.
	actions2, _, _ := decidePullActions(remote, local, state, nil)
	for _, a := range actions2 {
		if a.reason == "untracked-conflict" || a.reason == "diverged" {
			t.Fatalf("keep-local should silence the conflict; still got %q", a.reason)
		}
	}

	e := state.Skills["home"]
	if e.SkillID != testUUID("skill-home").String() {
		t.Fatalf("marker should track the server skill id; got %q", e.SkillID)
	}
	if e.ContentHash != "server-hash" {
		t.Fatalf("marker baseline should be the server's current hash; got %q", e.ContentHash)
	}
	if e.ResolvedHash != "" {
		t.Fatalf("an owned skill (no Source) should not set ResolvedHash; got %q", e.ResolvedHash)
	}
}

// For a sourced/org skill, keep-local must also acknowledge the current
// upstream (ResolvedHash) so the modified-pending prompt stays quiet until
// upstream actually moves — mirroring `airskills resolve`.
func TestKeepLocalEntrySourcedSetsResolvedHash(t *testing.T) {
	remote := apiSkill{Id: testUUID("skill-home"), Name: "home", Version: "2.0.0", ContentHash: strPtr("orgh")}
	src := &skillSource{Owner: "cherrypick", Slug: "home", ID: testUUID("skill-home").String()}

	e := buildKeepLocalEntry(remote, "org", "cherrypick", src)
	if e.ResolvedHash != "orgh" {
		t.Fatalf("sourced skill should acknowledge upstream via ResolvedHash; got %q", e.ResolvedHash)
	}
	if e.Source != src {
		t.Fatal("Source should be carried through onto the marker")
	}
	if e.OwnerKind != "org" || e.OwnerSlug != "cherrypick" {
		t.Fatalf("owner kind/slug should be recorded; got %q/%q", e.OwnerKind, e.OwnerSlug)
	}
}

// End-to-end: a live untracked-conflict on "home", resolved with
// `pull --keep-local home`, must write the tracking marker, sweep the
// parked review copy, and leave local files byte-for-byte untouched.
func TestRunPullKeepLocalResolvesConflict(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	tmpDir := filepath.Join(home, "tmp")
	if err := os.MkdirAll(tmpDir, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", tmpDir)
	t.Setenv("TMP", tmpDir)
	t.Setenv("TEMP", tmpDir)

	oldFlag := skillsetFlag
	t.Cleanup(func() { skillsetFlag = oldFlag })
	skillsetFlag = ""

	// Untracked local "home" whose bytes won't match the server hash.
	skillDir := filepath.Join(home, ".claude", "skills", "home")
	if err := os.MkdirAll(skillDir, 0755); err != nil {
		t.Fatal(err)
	}
	localBody := "---\nname: home\ndescription: mine\n---\n# my home\n"
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(localBody), 0644); err != nil {
		t.Fatal(err)
	}

	// A parked review copy already exists from a prior sync.
	parkDir := conflictParkPath("home")
	if err := os.MkdirAll(parkDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(parkDir, "SKILL.md"), []byte("# server home"), 0600); err != nil {
		t.Fatal(err)
	}

	myID := testUUID("me").String()
	skillID := testUUID("skill-home").String()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/skills":
			fmt.Fprintf(w, `{"skills":[{"id":%q,"name":"home","slug":"home","version":"1.0.0","content_hash":"serverhash","owner_id":%q}]}`, skillID, myID)
		case "/api/v1/me":
			fmt.Fprintf(w, `{"id":%q,"username":"chris","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z","role":"user"}`, myID)
		default:
			http.NotFound(w, r) // /api/v1/organizations 404 is swallowed by the resolver
		}
	}))
	t.Cleanup(srv.Close)

	cfgDir := filepath.Join(home, ".config", "airskills")
	if err := os.MkdirAll(cfgDir, 0700); err != nil {
		t.Fatal(err)
	}
	cfgData, _ := json.Marshal(config.Config{APIURL: srv.URL})
	if err := os.WriteFile(filepath.Join(cfgDir, "config.json"), cfgData, 0600); err != nil {
		t.Fatal(err)
	}
	tokenData, _ := json.Marshal(config.TokenData{AccessToken: "x", RefreshToken: "y", ExpiresAt: time.Now().Add(time.Hour).Unix()})
	if err := os.WriteFile(filepath.Join(cfgDir, "token.json"), tokenData, 0600); err != nil {
		t.Fatal(err)
	}

	_ = captureStdout(t, func() {
		if err := runPullKeepLocal(pullCmd, []string{"home"}); err != nil {
			t.Fatalf("runPullKeepLocal: %v", err)
		}
	})

	e := loadSyncState().Skills["home"]
	if e == nil {
		t.Fatal("keep-local must write a tracking marker for home")
	}
	if e.SkillID != skillID || e.ContentHash != "serverhash" {
		t.Fatalf("marker should track the server skill at its current hash; got %+v", e)
	}
	if _, err := os.Stat(parkDir); !os.IsNotExist(err) {
		t.Fatal("keep-local must sweep the parked review copy")
	}
	if b, _ := os.ReadFile(filepath.Join(skillDir, "SKILL.md")); string(b) != localBody {
		t.Fatalf("local files must be untouched; got %q", b)
	}

	// Multi-step: re-classify against the same server listing — the conflict
	// must NOT recur. This is the proof the recurring-warning bug is gone.
	remoteSkills := []apiSkill{
		{Id: testUUID("skill-home"), Name: "home", Version: "1.0.0", ContentHash: strPtr("serverhash")},
	}
	localMap := map[string]string{"home": skillDir}
	actions, _, _ := decidePullActions(remoteSkills, localMap, loadSyncState(), nil)
	for _, a := range actions {
		if a.reason == "untracked-conflict" || a.reason == "diverged" {
			t.Fatalf("after keep-local, a re-sync must be quiet; still classified %q", a.reason)
		}
	}
}

// keep-local on a tracked (diverged) skill must update the hash/source but
// PRESERVE identity fields a fresh marker would wipe — a LocalAlias from
// `add --as` or a pending SuggestionID. Regression for the clobber where
// buildKeepLocalEntry's fresh marker replaced the tracked one wholesale.
func TestRunPullKeepLocalPreservesMarkerFields(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	tmpDir := filepath.Join(home, "tmp")
	if err := os.MkdirAll(tmpDir, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", tmpDir)
	t.Setenv("TMP", tmpDir)
	t.Setenv("TEMP", tmpDir)

	oldFlag := skillsetFlag
	t.Cleanup(func() { skillsetFlag = oldFlag })
	skillsetFlag = ""

	skillDir := filepath.Join(home, ".claude", "skills", "home")
	if err := os.MkdirAll(skillDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("# my edited home\n"), 0644); err != nil {
		t.Fatal(err)
	}

	skillID := testUUID("skill-home").String()
	// Prior marker → this is a *tracked* skill that has diverged (marker hash
	// differs from both local and server), carrying fields to preserve.
	st := loadSyncState()
	st.Skills["home"] = &SyncEntry{
		SkillID:      skillID,
		Version:      "1.0.0",
		ContentHash:  "oldhash",
		Tool:         "claude-code",
		SuggestionID: "sugg-123",
	}
	if err := saveSyncState(st); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/skills":
			fmt.Fprintf(w, `{"skills":[{"id":%q,"name":"home","slug":"home","version":"2.0.0","content_hash":"serverhash"}]}`, skillID)
		case "/api/v1/me":
			fmt.Fprintf(w, `{"id":%q,"username":"chris","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z","role":"user"}`, testUUID("me").String())
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	cfgDir := filepath.Join(home, ".config", "airskills")
	if err := os.MkdirAll(cfgDir, 0700); err != nil {
		t.Fatal(err)
	}
	cfgData, _ := json.Marshal(config.Config{APIURL: srv.URL})
	if err := os.WriteFile(filepath.Join(cfgDir, "config.json"), cfgData, 0600); err != nil {
		t.Fatal(err)
	}
	tokenData, _ := json.Marshal(config.TokenData{AccessToken: "x", RefreshToken: "y", ExpiresAt: time.Now().Add(time.Hour).Unix()})
	if err := os.WriteFile(filepath.Join(cfgDir, "token.json"), tokenData, 0600); err != nil {
		t.Fatal(err)
	}

	_ = captureStdout(t, func() {
		if err := runPullKeepLocal(pullCmd, []string{"home"}); err != nil {
			t.Fatalf("runPullKeepLocal: %v", err)
		}
	})

	e := loadSyncState().Skills["home"]
	if e == nil {
		t.Fatal("marker missing after keep-local")
	}
	if e.ContentHash != "serverhash" {
		t.Fatalf("hash should re-baseline to the server's; got %q", e.ContentHash)
	}
	if e.SuggestionID != "sugg-123" {
		t.Fatalf("pending SuggestionID must be preserved; got %q", e.SuggestionID)
	}
}
