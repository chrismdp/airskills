package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chrismdp/airskills/config"
)

// status with no token used to return nil silently — nothing printed, exit 0.
// A logged-out machine looked identical to a healthy idle one, which cost
// real debugging time (2026-06-10: a test-suite bug had deleted the real
// token and status gave no clue). It must say so and fail.
func TestRunStatusNotLoggedInSaysSo(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	// Config present, token absent — the logged-out shape.
	cfgDir := filepath.Join(home, ".config", "airskills")
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatal(err)
	}
	cfgData, err := json.Marshal(config.Config{APIURL: "http://127.0.0.1:1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "config.json"), cfgData, 0o600); err != nil {
		t.Fatal(err)
	}

	err = runStatus(statusCmd, nil)
	if err == nil {
		t.Fatal("status without a token must return an error, got nil")
	}
	if !strings.Contains(err.Error(), "not logged in") {
		t.Fatalf("error should say the machine is not logged in, got: %v", err)
	}
}

// status that cannot fetch the skill list must surface the failure, not
// print nothing and exit 0.
func TestRunStatusSurfacesServerFailure(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	// Logged in, but the API URL points at a dead port.
	writeTestConfigAndToken(t, home, "http://127.0.0.1:1")

	err := runStatus(statusCmd, nil)
	if err == nil {
		t.Fatal("status with an unreachable server must return an error, got nil")
	}
}
