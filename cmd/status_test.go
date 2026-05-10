package cmd

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/chrismdp/airskills/config"
)

func TestClassifyForStatusFlagsTrackedOnServerNoMarker(t *testing.T) {
	// Skill exists on the server, exists locally, but no marker —
	// next sync would surface a conflict. Doctor catches this; status
	// must too. Regression for the silent-skip in cmd/status.go where
	// the trackedName == "" + local-exists case fell off the loop.
	remote := []apiSkill{
		{Id: testUUID("id-foo"), Name: "foo", ContentHash: strPtr("rh1")},
	}
	local := map[string]string{
		"foo": "/agent/skills/foo",
	}
	state := &SyncState{Skills: map[string]*SyncEntry{}}

	got := classifyForStatus(remote, local, state)
	if !reflect.DeepEqual(got.untracked, []string{"foo"}) {
		t.Fatalf("untracked = %v, want [foo]", got.untracked)
	}
	if len(got.toPull) != 0 || len(got.toPush) != 0 || len(got.toUpdate) != 0 {
		t.Fatalf("expected only untracked bucket populated, got %+v", got)
	}
}

func TestClassifyForStatusBuckets(t *testing.T) {
	remote := []apiSkill{
		{Id: testUUID("id-tracked-clean"), Name: "tracked-clean", ContentHash: strPtr("h-clean")},
		{Id: testUUID("id-tracked-changed"), Name: "tracked-changed", ContentHash: strPtr("h-new")},
		{Id: testUUID("id-not-local"), Name: "not-local", ContentHash: strPtr("h-x")},
		{Id: testUUID("id-untracked"), Name: "untracked-skill", ContentHash: strPtr("h-y")},
	}
	local := map[string]string{
		"tracked-clean":   "/agent/skills/tracked-clean",
		"tracked-changed": "/agent/skills/tracked-changed",
		"untracked-skill": "/agent/skills/untracked-skill",
		"local-only":      "/agent/skills/local-only",
	}
	state := &SyncState{Skills: map[string]*SyncEntry{
		"tracked-clean":   {SkillID: testUUID("id-tracked-clean").String(), ContentHash: "h-clean"},
		"tracked-changed": {SkillID: testUUID("id-tracked-changed").String(), ContentHash: "h-old"},
	}}

	got := classifyForStatus(remote, local, state)
	if !reflect.DeepEqual(got.toPush, []string{"local-only"}) {
		t.Errorf("toPush = %v, want [local-only]", got.toPush)
	}
	if !reflect.DeepEqual(got.toPull, []string{"not-local"}) {
		t.Errorf("toPull = %v, want [not-local]", got.toPull)
	}
	if !reflect.DeepEqual(got.toUpdate, []string{"tracked-changed"}) {
		t.Errorf("toUpdate = %v, want [tracked-changed]", got.toUpdate)
	}
	if !reflect.DeepEqual(got.untracked, []string{"untracked-skill"}) {
		t.Errorf("untracked = %v, want [untracked-skill]", got.untracked)
	}
}

// TestRunStatusSuppressesLatestCLIHintAfterAutoUpdate guards the Phase 5
// fix: when maybeAutoUpdate has already swapped the on-disk binary in
// this process, runStatus must NOT also print "[airskills] update → X:
// run 'airskills self-update'". The user just saw "airskills: updated
// to vX" — repeating the prompt for a version they already have on disk
// is confusing and undermines the auto-update UX.
func TestRunStatusSuppressesLatestCLIHintAfterAutoUpdate(t *testing.T) {
	out := runStatusCapture(t, "0.6.1", "99.99.99", true)
	if strings.Contains(out, "self-update") {
		t.Errorf("after auto-update, latestCLI hint must be suppressed; got:\n%s", out)
	}
	// Cosmetic side-bug: with the suppression in place we should route
	// into the all-clean branch since nothing else is pending. Verify
	// we don't emit the empty "[airskills]  — run 'airskills sync'" line.
	if strings.Contains(out, "[airskills]  — run") {
		t.Errorf("empty parts line leaked through; got:\n%s", out)
	}
	if !strings.Contains(out, "in sync") {
		t.Errorf("expected the all-clean '✓ in sync' line; got:\n%s", out)
	}
}

// TestRunStatusPrintsLatestCLIHintWhenNoAutoUpdate guards the existing
// behaviour: a system-managed install (brew/apt/snap) where auto-update
// never fired must still see the "run airskills self-update" prompt.
// Suppression only kicks in for processes that themselves auto-updated.
func TestRunStatusPrintsLatestCLIHintWhenNoAutoUpdate(t *testing.T) {
	out := runStatusCapture(t, "0.6.1", "99.99.99", false)
	if !strings.Contains(out, "self-update") {
		t.Errorf("with autoUpdateDidFire=false, latestCLI hint must print; got:\n%s", out)
	}
	if !strings.Contains(out, "99.99.99") {
		t.Errorf("expected the latest version 99.99.99 in the hint; got:\n%s", out)
	}
}

// runStatusCapture drives runStatus end-to-end against a stub server
// that reports the given latestCLI from /api/v1/health, returns empty
// skills + zero suggestions, and captures the resulting stderr output
// so tests can assert on what the user actually sees. Sets the
// in-memory `version` constant and the `autoUpdateDidFire` flag for
// the duration of the test.
func runStatusCapture(t *testing.T, runningVersion, serverLatestCLI string, autoUpdated bool) string {
	t.Helper()

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/health":
			w.Write([]byte(`{"latest_cli":"` + serverLatestCLI + `"}`))
		case "/api/v1/skills":
			w.Write([]byte(`{"skills":[]}`))
		case "/api/v1/suggestions/count":
			w.Write([]byte(`{"count":0}`))
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

	oldVersion := version
	version = runningVersion
	t.Cleanup(func() { version = oldVersion })

	autoUpdateDidFire.Store(autoUpdated)
	t.Cleanup(func() { autoUpdateDidFire.Store(false) })

	return captureStderr(t, func() {
		if err := runStatus(statusCmd, nil); err != nil {
			t.Fatalf("runStatus: %v", err)
		}
	})
}

// captureStderr swaps os.Stderr for a pipe, runs fn, and returns
// everything written. The reader runs in a goroutine so a bigger
// payload than the pipe buffer doesn't deadlock the writer.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	old := os.Stderr
	os.Stderr = w

	done := make(chan string, 1)
	go func() {
		data, _ := io.ReadAll(r)
		done <- string(data)
	}()

	fn()

	w.Close()
	os.Stderr = old
	return <-done
}
