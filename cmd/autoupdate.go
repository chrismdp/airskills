package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"

	"github.com/chrismdp/airskills/config"
)

// autoUpdateDidFire is true once maybeAutoUpdate has successfully
// swapped the on-disk binary in the current process. Other code paths
// that compare the server-reported latest CLI version against the
// in-process `version` constant — notably cmd/status.go's "run
// airskills self-update" hint — consult this to suppress a redundant
// prompt: the user already saw "airskills: updated to vX" from the
// auto-update, but the running process's compiled-in `version` lags
// the on-disk binary until the next spawn, so a naive isNewer check
// would still tell them to upgrade to a version they already have.
var autoUpdateDidFire atomic.Bool

// maybeAutoUpdate auto-applies a CLI binary update if one is known to
// be available, the binary lives in a user-writable path, and no
// escape hatch is set. Best-effort: any failure is logged to stderr
// and the caller continues with the current binary. Never returns an
// error — auto-update must not fail the user's command.
//
// Returns true if the function attempted an update (success or failure
// with stderr message) — in that case the caller should suppress the
// passive "new version available" hint that checkForUpdates() prints,
// since it would either be redundant ("we just installed it") or
// stack noisily on top of the failure line. Returns false if the
// function decided not to attempt: dev build, env var / flag opt-out,
// no cached update available, system-managed binary path, etc.
func maybeAutoUpdate() bool {
	if version == "dev" {
		return false
	}
	if os.Getenv("AIRSKILLS_NO_AUTO_UPDATE") == "1" {
		return false
	}
	if noUpdate {
		return false
	}

	dir, err := config.Dir()
	if err != nil {
		return false
	}
	statePath := filepath.Join(dir, "update_state.json")
	data, err := os.ReadFile(statePath)
	if err != nil {
		return false
	}
	var state updateState
	if err := json.Unmarshal(data, &state); err != nil {
		return false
	}
	if state.LatestVersion == "" || !isNewer(state.LatestVersion, version) {
		return false
	}

	execPath, err := os.Executable()
	if err != nil {
		return false
	}
	if !isAutoUpdateSafe(execPath) {
		return false
	}

	newVersion, err := performUpdate(version, false, "auto")
	finalizeAutoUpdate(state.LatestVersion, version, newVersion, err)
	return true
}

// finalizeAutoUpdate handles bookkeeping after performUpdate returns.
// On err, narrate the failure on stderr. Set autoUpdateDidFire only
// when newVersion is non-empty — performUpdate returns ("", nil) when
// the GitHub fetch shows we're already on latest, which is reachable
// in the wild whenever update_state.json races with a release rollback
// or a stale cached manifest. Setting the flag in that case would
// suppress legitimate hints in cmd/status.go.
func finalizeAutoUpdate(latestVersion, currentVersion, newVersion string, err error) {
	if err != nil {
		fmt.Fprintf(os.Stderr,
			"airskills: auto-update to v%s failed (%s) — current command will run on v%s\n",
			latestVersion, classifyUpdateError(err), currentVersion)
		return
	}
	if newVersion != "" {
		autoUpdateDidFire.Store(true)
	}
}

// isAutoUpdateSafe returns true if execPath looks like a user-writable
// install (curl-pipe-bash, manual download, `go install`) and false
// for paths owned by a system package manager (apt, brew, snap, Mac
// App Store). Symlinks are resolved before classification so a brew
// shim under /opt/homebrew/bin still resolves into the Cellar.
func isAutoUpdateSafe(execPath string) bool {
	resolved, err := filepath.EvalSymlinks(execPath)
	if err != nil {
		return false
	}
	return isAutoUpdateSafePath(resolved)
}

// isAutoUpdateSafePath is the path-only logic split out so unit tests
// can pass synthetic strings without needing a real file at the path.
func isAutoUpdateSafePath(resolved string) bool {
	systemPrefixes := []string{"/usr/", "/opt/", "/snap/", "/Applications/"}
	for _, p := range systemPrefixes {
		if strings.HasPrefix(resolved, p) {
			return false
		}
	}

	parent := filepath.Dir(resolved)
	probe := filepath.Join(parent, ".airskills-write-probe")
	f, err := os.OpenFile(probe, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return false
	}
	f.Close()
	os.Remove(probe)
	return true
}

// classifyUpdateError buckets a performUpdate error into a short
// category for the auto-update stderr line. Substring-matched against
// the error message — coarse, but the user-facing string benefits
// more from "(network error)" than from a wrapped Go error chain.
func classifyUpdateError(err error) string {
	if err == nil {
		return "ok"
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "checksum"):
		return "checksum mismatch"
	case strings.Contains(msg, "extract"):
		return "extraction error"
	case strings.Contains(msg, "cannot write"),
		strings.Contains(msg, "cannot replace"),
		strings.Contains(msg, "permission"):
		return "permission denied"
	case strings.Contains(msg, "download"),
		strings.Contains(msg, "github"),
		strings.Contains(msg, "deadline"),
		strings.Contains(msg, "timeout"),
		strings.Contains(msg, "no such host"),
		strings.Contains(msg, "connection"):
		return "network error"
	default:
		return "error"
	}
}
