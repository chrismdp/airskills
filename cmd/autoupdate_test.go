package cmd

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestIsAutoUpdateSafePath_AcceptsUserDir(t *testing.T) {
	dir := t.TempDir()
	binPath := filepath.Join(dir, "airskills")
	if err := os.WriteFile(binPath, []byte("dummy"), 0755); err != nil {
		t.Fatal(err)
	}
	if !isAutoUpdateSafePath(binPath) {
		t.Errorf("user tmpdir %s should be safe; got false", dir)
	}
}

func TestIsAutoUpdateSafePath_RejectsSystemPaths(t *testing.T) {
	cases := []string{
		"/usr/bin/airskills",
		"/usr/local/bin/airskills",
		"/opt/homebrew/bin/airskills",
		"/opt/homebrew/Cellar/airskills/0.5.30/bin/airskills",
		"/snap/bin/airskills",
		"/Applications/Airskills.app/Contents/MacOS/airskills",
	}
	for _, p := range cases {
		if isAutoUpdateSafePath(p) {
			t.Errorf("%s: expected unsafe (system path), got safe", p)
		}
	}
}

func TestIsAutoUpdateSafePath_RejectsReadOnlyParent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod 0555 doesn't reliably block writes on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root can always write — chmod test irrelevant")
	}
	dir := t.TempDir()
	binPath := filepath.Join(dir, "airskills")
	if err := os.WriteFile(binPath, []byte("dummy"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0755) })
	if isAutoUpdateSafePath(binPath) {
		t.Errorf("read-only parent should be unsafe; got safe")
	}
}

// TestMaybeAutoUpdateReExecsAfterSuccess pins the behaviour Chris hit
// in the wild: a long-running command (airskills sync) triggered
// auto-update mid-flight, the binary on disk got swapped, but the
// running process kept executing the OLD code — so the user's
// command ran on whatever the previous version's flow looked like.
// After update, the process must re-exec into the new binary so the
// command runs on current code.
func TestMaybeAutoUpdateReExecsAfterSuccess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("re-exec uses syscall.Exec; Windows path is structurally different")
	}

	// Put a writable fake binary in a tempdir and point os.Executable at it
	// by running the test binary itself — we only need execPath to pass
	// isAutoUpdateSafe(), and the temp HOME is what config.Dir() reads.
	tmpHome := t.TempDir()
	setTestHome(t, tmpHome)

	cfgDir := filepath.Join(tmpHome, ".config", "airskills")
	if err := os.MkdirAll(cfgDir, 0700); err != nil {
		t.Fatalf("mkdir cfg: %v", err)
	}
	state := updateState{LatestVersion: "9.9.9"}
	data, _ := json.Marshal(state)
	if err := os.WriteFile(filepath.Join(cfgDir, "update_state.json"), data, 0644); err != nil {
		t.Fatalf("write update_state: %v", err)
	}

	// Pin version to something less than 9.9.9 so isNewer fires.
	oldVersion := version
	version = "0.0.1"
	t.Cleanup(func() { version = oldVersion })

	// autoUpdateDidFire is package state; reset.
	autoUpdateDidFire.Store(false)
	t.Cleanup(func() { autoUpdateDidFire.Store(false) })

	var updateCalled, reExecCalled bool
	performUpdateFn = func(currentVersion string, verbose bool, trigger string) (string, error) {
		updateCalled = true
		if trigger != "auto" {
			t.Errorf("performUpdate trigger: want %q, got %q", "auto", trigger)
		}
		return "9.9.9", nil
	}
	t.Cleanup(func() { performUpdateFn = performUpdate })

	reExecFn = func(execPath string, args []string, env []string) error {
		reExecCalled = true
		// Reuses the same guard env as the 426 path so a runaway
		// loop is impossible.
		found := false
		for _, kv := range env {
			if kv == reExecGuardEnv+"=1" {
				found = true
			}
		}
		if !found {
			t.Errorf("re-exec env missing %s=1", reExecGuardEnv)
		}
		return nil
	}
	t.Cleanup(func() { reExecFn = reExec })

	maybeAutoUpdate()

	if !updateCalled {
		t.Error("performUpdateFn was not called")
	}
	if !reExecCalled {
		t.Error("reExecFn was not called — running process will keep executing pre-update code")
	}
}

// TestMaybeAutoUpdateSkipsWhenGuardEnvSet pins the no-loop defence:
// the child of a previous re-exec must not attempt another update,
// even if its `version` somehow disagrees with state.LatestVersion
// (pathological download or a state.json race).
func TestMaybeAutoUpdateSkipsWhenGuardEnvSet(t *testing.T) {
	tmpHome := t.TempDir()
	setTestHome(t, tmpHome)

	cfgDir := filepath.Join(tmpHome, ".config", "airskills")
	if err := os.MkdirAll(cfgDir, 0700); err != nil {
		t.Fatalf("mkdir cfg: %v", err)
	}
	state := updateState{LatestVersion: "9.9.9"}
	data, _ := json.Marshal(state)
	if err := os.WriteFile(filepath.Join(cfgDir, "update_state.json"), data, 0644); err != nil {
		t.Fatalf("write update_state: %v", err)
	}

	oldVersion := version
	version = "0.0.1"
	t.Cleanup(func() { version = oldVersion })

	t.Setenv(reExecGuardEnv, "1")

	performUpdateFn = func(string, bool, string) (string, error) {
		t.Fatal("performUpdate must NOT be called when guard env is set")
		return "", nil
	}
	t.Cleanup(func() { performUpdateFn = performUpdate })

	if maybeAutoUpdate() {
		t.Error("maybeAutoUpdate returned true (attempted) under guard env; should have skipped")
	}
}

func TestClassifyUpdateError(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{errors.New("checksum mismatch: expected X, got Y"), "checksum mismatch"},
		{errors.New("extraction failed: bad gzip"), "extraction error"},
		{errors.New("cannot write new binary (try: sudo airskills self-update)"), "permission denied"},
		{errors.New("cannot replace binary"), "permission denied"},
		{errors.New("download failed: connection refused"), "network error"},
		{errors.New("GitHub API returned 503"), "network error"},
		{errors.New("Get \"https://api.github.com/...\": dial tcp: lookup api.github.com: no such host"), "network error"},
		{errors.New("context deadline exceeded"), "network error"},
		{errors.New("something completely unexpected"), "error"},
		{nil, "ok"},
	}
	for _, c := range cases {
		got := classifyUpdateError(c.err)
		if got != c.want {
			label := "<nil>"
			if c.err != nil {
				label = c.err.Error()
			}
			t.Errorf("classifyUpdateError(%q) = %q, want %q", label, got, c.want)
		}
	}
}
