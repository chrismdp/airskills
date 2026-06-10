package cmd

import (
	"os"
	"testing"
)

// TestMain redirects the system temp dir for the whole package run.
//
// Conflict copies park at os.TempDir()/airskills-conflicts/<name>
// (conflictParkPath, add_collision.go, push.go's 409 path). Any test that
// exercises a conflict path without overriding TMPDIR therefore leaks dirs
// like /tmp/airskills-conflicts/borrowed into the developer's REAL temp dir
// — and the developer's own `airskills status` (often eval'd in the shell
// prompt) then nags a phantom "pending conflict: borrowed" until the dir is
// hand-deleted. Redirecting TMPDIR here makes every test hermetic by
// default; tests that point TMPDIR somewhere more specific (rm_test,
// status_test) still override per-test via t.Setenv.
func TestMain(m *testing.M) {
	tmp, err := os.MkdirTemp("", "airskills-cmd-test-")
	if err == nil {
		os.Setenv("TMPDIR", tmp) // unix: os.TempDir reads this per call
		os.Setenv("TMP", tmp)    // windows: GetTempPath checks TMP, then TEMP
		os.Setenv("TEMP", tmp)
	}
	code := m.Run()
	if tmp != "" {
		os.RemoveAll(tmp)
	}
	os.Exit(code)
}
