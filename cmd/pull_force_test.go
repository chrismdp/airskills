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

// pull --force adopting a skill the caller does not own must write the same
// ownership fields a normal pull install would — above all Source, which is
// what routes the next push into fork+suggest instead of a doomed write to
// the upstream (the "you no longer have write access" dead-end).
func TestForcePullMarkerWritesSourceForOrgSkill(t *testing.T) {
	orgID := testUUID("org-parsons")
	skill := apiSkill{
		Id:          testUUID("skill-home"),
		Name:        "home",
		Slug:        "home",
		Version:     "1.2.0",
		ContentHash: strPtr("serverhash"),
		OrgId:       &orgID,
	}
	owners := &ownerResolver{
		userID:   testUUID("me").String(),
		username: "poppinsparsons",
		orgsByID: map[string]string{orgID.String(): "parsons-home"},
	}
	state := &SyncState{Version: 1, Skills: map[string]*SyncEntry{}}

	m := applyForcePullMarker(state, "home", skill, owners)

	if m.SkillID != skill.Id.String() || m.ContentHash != "serverhash" || m.Version != "1.2.0" {
		t.Fatalf("marker basics wrong: %+v", m)
	}
	if m.OwnerKind != "org" || m.OwnerSlug != "parsons-home" {
		t.Errorf("owner = %q/%q, want org/parsons-home", m.OwnerKind, m.OwnerSlug)
	}
	if m.Source == nil {
		t.Fatal("marker.Source must be set for a non-owned skill — without it the next push dead-ends on 403")
	}
	if m.Source.ID != skill.Id.String() || m.Source.Owner != "parsons-home" || m.Source.Slug != "home" {
		t.Errorf("Source = %+v, want upstream pointer at the org skill", m.Source)
	}
	if state.Skills["home"] != m {
		t.Error("marker must be stored in sync state under the local name")
	}
}

func TestForcePullMarkerNoSourceForOwnedSkill(t *testing.T) {
	me := testUUID("me")
	skill := apiSkill{
		Id: testUUID("skill-mine"), Name: "mine", Slug: "mine",
		Version: "1.0.0", ContentHash: strPtr("h"), OwnerId: &me,
	}
	owners := &ownerResolver{userID: me.String(), username: "chris"}
	state := &SyncState{Version: 1, Skills: map[string]*SyncEntry{}}

	m := applyForcePullMarker(state, "mine", skill, owners)

	if m.Source != nil {
		t.Errorf("owned skill must not get a Source pointer, got %+v", m.Source)
	}
	if m.OwnerKind != "user" || m.OwnerSlug != "chris" {
		t.Errorf("owner = %q/%q, want user/chris", m.OwnerKind, m.OwnerSlug)
	}
}

// A tracked skill that already carries identity fields must keep them —
// force-pull updates the snapshot, it does not re-create the marker.
func TestForcePullMarkerPreservesExistingSourceAndAlias(t *testing.T) {
	skill := apiSkill{
		Id: testUUID("skill-x"), Name: "x", Slug: "x",
		Version: "2.0.0", ContentHash: strPtr("h2"),
	}
	existing := &skillSource{Owner: "alice", Slug: "x", ID: testUUID("upstream-x").String()}
	state := &SyncState{Version: 1, Skills: map[string]*SyncEntry{
		"x": {SkillID: "old", LocalAlias: "x-alias", Source: existing, Tool: "claude-code"},
	}}

	m := applyForcePullMarker(state, "x", skill, &ownerResolver{})

	if m.Source != existing {
		t.Errorf("existing Source must be preserved, got %+v", m.Source)
	}
	if m.LocalAlias != "x-alias" {
		t.Errorf("LocalAlias clobbered: %q", m.LocalAlias)
	}
	if m.SkillID != skill.Id.String() || m.ContentHash != "h2" {
		t.Errorf("snapshot fields not updated: %+v", m)
	}
}

// Full-path repro of the org-member trap: untracked local "home" conflicts
// with org skill parsons-home/home; pull --force adopts the org bytes. The
// marker it writes must be sourced (so the next push fork+suggests instead
// of 403ing) and the parked review copy must be swept (so sync stops
// nagging "1 pending conflict").
func TestRunPullForceAdoptsOrgSkillSourcedAndSweepsParkedCopy(t *testing.T) {
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

	// Server copy of the skill, served as a tar.gz archive.
	serverBody := "---\nname: home\ndescription: org version\n---\n# org home\n"
	serverDir := filepath.Join(t.TempDir(), "home")
	if err := os.MkdirAll(serverDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(serverDir, "SKILL.md"), []byte(serverBody), 0644); err != nil {
		t.Fatal(err)
	}
	archive, err := createTarGz(serverDir)
	if err != nil {
		t.Fatal(err)
	}

	myID := testUUID("me").String()
	orgID := testUUID("org-parsons").String()
	skillID := testUUID("skill-home").String()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/skills":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"skills":[{"id":%q,"name":"home","slug":"home","version":"1.0.0","content_hash":"serverhash","org_id":%q}]}`, skillID, orgID)
		case "/api/v1/me":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"id":%q,"username":"poppinsparsons","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z","role":"user"}`, myID)
		case "/api/v1/organizations":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"organizations":[{"id":%q,"slug":"parsons-home","name":"Parsons Home","role":"member","member_count":2}]}`, orgID)
		case "/api/v1/skills/" + skillID + "/archive":
			w.Header().Set("Content-Type", "application/gzip")
			_, _ = w.Write(archive)
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

	// Feed "y" to the force-pull confirmation prompt.
	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stdinW.WriteString("y\n"); err != nil {
		t.Fatal(err)
	}
	stdinW.Close()
	oldStdin := os.Stdin
	os.Stdin = stdinR
	t.Cleanup(func() { os.Stdin = oldStdin })

	_ = captureStdout(t, func() {
		if err := runPullForce(pullCmd, []string{"home"}); err != nil {
			t.Fatalf("runPullForce: %v", err)
		}
	})

	e := loadSyncState().Skills["home"]
	if e == nil {
		t.Fatal("force-pull must write a tracking marker for home")
	}
	if e.SkillID != skillID || e.ContentHash != "serverhash" {
		t.Fatalf("marker should track the server skill at its current hash; got %+v", e)
	}
	if e.OwnerKind != "org" || e.OwnerSlug != "parsons-home" {
		t.Errorf("owner = %q/%q, want org/parsons-home", e.OwnerKind, e.OwnerSlug)
	}
	if e.Source == nil || e.Source.ID != skillID {
		t.Fatalf("marker must be sourced at the org skill so the next push fork+suggests; got Source=%+v", e.Source)
	}
	if _, err := os.Stat(parkDir); !os.IsNotExist(err) {
		t.Error("force-pull must sweep the parked review copy — it resolved the conflict")
	}
	if b, _ := os.ReadFile(filepath.Join(skillDir, "SKILL.md")); string(b) != serverBody {
		t.Errorf("local files must be overwritten with the server copy; got %q", b)
	}
}
