package cmd

import (
	"encoding/json"
	"fmt"
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

	got := classifyForStatus(remote, local, state, nil, nil)
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

	got := classifyForStatus(remote, local, state, nil, nil)
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

// After `airskills skillset use poppins` the user's other 70+ skills
// drop out of the active listing. Before the in-other-skillset bucket
// they were dumped into toPush, which lied: the skill already exists
// server-side under another skillset, and a push would have collided
// on name. They belong in their own bucket so status reports them as
// "owned but in another skillset" instead.
func TestClassifyForStatusBucketsInOtherSkillset(t *testing.T) {
	remote := []apiSkill{
		{Id: testUUID("id-active"), Name: "active-skill", ContentHash: strPtr("ha")},
	}
	local := map[string]string{
		"active-skill": "/agent/skills/active-skill",
		"in-other":     "/agent/skills/in-other",
		"never-pushed": "/agent/skills/never-pushed",
	}
	state := &SyncState{Skills: map[string]*SyncEntry{
		"active-skill": {SkillID: testUUID("id-active").String(), ContentHash: "ha"},
		"in-other":     {SkillID: testUUID("id-elsewhere").String(), ContentHash: "hb"},
	}}
	ownedElsewhere := map[string]bool{"in-other": true}

	got := classifyForStatus(remote, local, state, nil, ownedElsewhere)
	if !reflect.DeepEqual(got.inOtherSkillset, []string{"in-other"}) {
		t.Errorf("inOtherSkillset = %v, want [in-other]", got.inOtherSkillset)
	}
	if !reflect.DeepEqual(got.toPush, []string{"never-pushed"}) {
		t.Errorf("toPush = %v, want [never-pushed] (in-other must NOT be here)", got.toPush)
	}
}

// Obsolete after mig 047: cfg.Skillset is no longer plumbed through —
// the server's /api/v1/skills always resolves to the user's single
// implicit 'default' regardless of the param value. The test above
// described pre-mig behaviour that no longer applies.

// Reproduces the tmux-re-attach scenario: maybeAutoUpdate fires,
// autoUpdateDidFire is true, runStatus must not nag for self-update.
func TestRunStatusSuppressesLatestCLIHintAfterAutoUpdate(t *testing.T) {
	out := runStatusCapture(t, statusFixture{
		runningVersion: "0.6.1",
		latestCLI:      "99.99.99",
		autoUpdated:    true,
	})
	if strings.Contains(out, "self-update") {
		t.Errorf("latestCLI hint must be suppressed; got:\n%s", out)
	}
	if strings.Contains(out, "[airskills]  — run") {
		t.Errorf("empty parts line leaked through; got:\n%s", out)
	}
	if !strings.Contains(out, "in sync") {
		t.Errorf("expected the all-clean '✓ in sync' line; got:\n%s", out)
	}
}

// brew/apt/snap installs never auto-update — the hint must still print.
func TestRunStatusPrintsLatestCLIHintWhenNoAutoUpdate(t *testing.T) {
	out := runStatusCapture(t, statusFixture{
		runningVersion: "0.6.1",
		latestCLI:      "99.99.99",
		autoUpdated:    false,
	})
	if !strings.Contains(out, "self-update") {
		t.Errorf("hint must print; got:\n%s", out)
	}
	if !strings.Contains(out, "99.99.99") {
		t.Errorf("expected version 99.99.99 in the hint; got:\n%s", out)
	}
}

// Pins down that suppression doesn't accidentally route into the
// all-clean branch (and produce an empty parts line) when other work
// is pending. autoUpdateDidFire=true with one local-only skill must
// still print the "↑ 1 to push" line, and not the self-update line.
func TestRunStatusSuppressesLatestCLIHintButKeepsPartsLine(t *testing.T) {
	out := runStatusCapture(t, statusFixture{
		runningVersion: "0.6.1",
		latestCLI:      "99.99.99",
		autoUpdated:    true,
		localSkills:    []string{"local-only-skill"},
	})
	if strings.Contains(out, "self-update") {
		t.Errorf("self-update line must not print; got:\n%s", out)
	}
	if !strings.Contains(out, "to push") {
		t.Errorf("expected '↑ 1 to push' parts line; got:\n%s", out)
	}
	if strings.Contains(out, "in sync") {
		t.Errorf("must not route into all-clean branch when work is pending; got:\n%s", out)
	}
}

// Quiet mode is the shell-prompt hot path (eval "$(airskills status)").
// Suppression must hold there too — and the output must collapse to
// nothing on the all-clean route.
func TestRunStatusSuppressesLatestCLIHintInQuietMode(t *testing.T) {
	out := runStatusCapture(t, statusFixture{
		runningVersion: "0.6.1",
		latestCLI:      "99.99.99",
		autoUpdated:    true,
		quiet:          true,
	})
	if strings.Contains(out, "self-update") {
		t.Errorf("self-update line must not print in quiet mode; got:\n%s", out)
	}
	if strings.TrimSpace(out) != "" {
		t.Errorf("quiet all-clean must produce no output; got:\n%s", out)
	}
}

func TestRunStatusIgnoresStaleRememberedSkillset(t *testing.T) {
	var gotQueries []string
	out := runStatusCapture(t, statusFixture{
		runningVersion:  "0.6.1",
		rememberedSlug:  "poppins",
		skillsQuerySink: &gotQueries,
	})
	for _, q := range gotQueries {
		if strings.Contains(q, "skillset=poppins") {
			t.Fatalf("status sent stale remembered skillset query; queries = %v", gotQueries)
		}
	}
	if !strings.Contains(out, "in sync") {
		t.Fatalf("expected clean status after ignoring stale skillset, got:\n%s", out)
	}
}

// Direct unit test for the post-performUpdate gate: the flag must only
// flip when an actual swap happened. ("", nil) is the rare-but-real
// case where update_state.json said newer-available but the GitHub
// re-fetch in performUpdate disagreed (release rollback, stale cache).
func TestFinalizeAutoUpdateGatesOnNewVersion(t *testing.T) {
	cases := []struct {
		name       string
		newVersion string
		err        error
		wantFlag   bool
	}{
		{"successful swap sets flag", "0.6.4", nil, true},
		{"already on latest does not set flag", "", nil, false},
		{"failure does not set flag", "", fmt.Errorf("download failed"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			autoUpdateDidFire.Store(false)
			t.Cleanup(func() { autoUpdateDidFire.Store(false) })
			// Capture stderr so the failure-path notification doesn't
			// leak into test output.
			_ = captureStderr(t, func() {
				finalizeAutoUpdate("0.6.4", "0.6.1", tc.newVersion, tc.err)
			})
			if got := autoUpdateDidFire.Load(); got != tc.wantFlag {
				t.Errorf("autoUpdateDidFire = %v, want %v", got, tc.wantFlag)
			}
		})
	}
}

// statusFixture bundles the inputs runStatusCapture varies. Most tests
// only set a subset; zero values are sensible defaults (no local
// skills, no quiet mode).
type statusFixture struct {
	runningVersion  string
	latestCLI       string
	autoUpdated     bool
	localSkills     []string
	quiet           bool
	rememberedSlug  string    // cfg.Skillset on disk before runStatus
	skillsQuerySink *[]string // appended by httptest server on /api/v1/skills hits
}

func runStatusCapture(t *testing.T, f statusFixture) string {
	t.Helper()

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	for _, name := range f.localSkills {
		dir := filepath.Join(home, ".claude", "skills", name)
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatalf("MkdirAll skill: %v", err)
		}
		body := "---\nname: " + name + "\ndescription: test\n---\n# " + name + "\n"
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0600); err != nil {
			t.Fatalf("write SKILL.md: %v", err)
		}
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/health":
			w.Write([]byte(`{"latest_cli":"` + f.latestCLI + `"}`))
		case "/api/v1/skills":
			if f.skillsQuerySink != nil {
				*f.skillsQuerySink = append(*f.skillsQuerySink, r.URL.RawQuery)
			}
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
	cfgData, _ := json.Marshal(config.Config{APIURL: srv.URL, Skillset: f.rememberedSlug})
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
	version = f.runningVersion
	t.Cleanup(func() { version = oldVersion })

	autoUpdateDidFire.Store(f.autoUpdated)
	t.Cleanup(func() { autoUpdateDidFire.Store(false) })

	cmd := *statusCmd
	cmd.ResetFlags()
	cmd.Flags().BoolP("quiet", "q", false, "")
	if f.quiet {
		cmd.Flags().Set("quiet", "true")
	}

	return captureStderr(t, func() {
		if err := runStatus(&cmd, nil); err != nil {
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
