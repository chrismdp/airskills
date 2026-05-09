package cmd

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/chrismdp/airskills/telemetry"
	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(selfUpdateCmd)
}

var selfUpdateCmd = &cobra.Command{
	Use:   "self-update",
	Short: "Update the airskills CLI to the latest version",
	Long:  "Downloads the latest airskills binary from GitHub Releases, verifies the checksum, and replaces the current binary.",
	RunE:  runSelfUpdate,
}

type ghRelease struct {
	TagName string    `json:"tag_name"`
	Assets  []ghAsset `json:"assets"`
}

type ghAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

func runSelfUpdate(cmd *cobra.Command, args []string) error {
	_, err := performUpdate(version, true, "manual")
	return err
}

// performUpdate fetches the latest GitHub release and, if newer than
// currentVersion, downloads + verifies + replaces the running binary.
// Returns the new version on success ("" if already on latest).
//
// verbose=true matches today's manual `self-update` output (chatty
// stdout, multiple status lines). verbose=false is the auto-mode
// notification format used by maybeAutoUpdate(): two terse stderr
// lines on success, one on failure. Auto-mode also uses a tighter
// 10s HTTP timeout so a slow network doesn't add 30s to every command.
//
// trigger ("manual" or "auto") tags the three telemetry events fired
// from this function: cli_self_update_started, cli_self_update_completed,
// cli_self_update_failed. Events fire only once we've decided an update
// is available — pre-decision errors (manifest fetch, JSON parse) are
// stderr-only by design.
//
// Auto-update fires *before* the user's command runs, never after,
// so it cannot interfere with a mid-flight skill-state mutation.
func performUpdate(currentVersion string, verbose bool, trigger string) (string, error) {
	timeout := 30 * time.Second
	if !verbose {
		timeout = 10 * time.Second
	}
	client := &http.Client{Timeout: timeout}

	if verbose {
		fmt.Printf("Current version: %s\n", currentVersion)
		fmt.Print("Checking for updates... ")
	}

	req, err := http.NewRequest("GET", releasesURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to check for updates: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return "", fmt.Errorf("GitHub API returned %d (is the repo public?)", resp.StatusCode)
	}

	var release ghRelease
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return "", fmt.Errorf("failed to parse release: %w", err)
	}

	latest := strings.TrimPrefix(release.TagName, "v")
	if !isNewer(latest, currentVersion) && currentVersion != "dev" {
		if verbose {
			fmt.Printf("already on latest (%s)\n", currentVersion)
		}
		return "", nil
	}

	if verbose {
		fmt.Printf("found %s\n", latest)
	} else {
		fmt.Fprintf(os.Stderr, "airskills: updating to v%s (from v%s)...\n", latest, currentVersion)
	}

	startedAt := time.Now()
	telemetry.Capture("cli_self_update_started", map[string]interface{}{
		"from_version": currentVersion,
		"to_version":   latest,
		"trigger":      trigger,
	})

	fail := func(reason string, err error) (string, error) {
		msg := err.Error()
		if len(msg) > 200 {
			msg = msg[:200]
		}
		telemetry.Capture("cli_self_update_failed", map[string]interface{}{
			"from_version": currentVersion,
			"to_version":   latest,
			"trigger":      trigger,
			"reason":       reason,
			"error":        msg,
		})
		return "", err
	}

	archiveName := fmt.Sprintf("airskills_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	var archiveURL, checksumURL string
	for _, asset := range release.Assets {
		if asset.Name == archiveName {
			archiveURL = asset.BrowserDownloadURL
		}
		if asset.Name == "checksums.txt" {
			checksumURL = asset.BrowserDownloadURL
		}
	}
	if archiveURL == "" {
		return fail("other", fmt.Errorf("no release found for %s/%s", runtime.GOOS, runtime.GOARCH))
	}

	var expectedHash string
	if checksumURL != "" {
		if verbose {
			fmt.Print("Downloading checksums... ")
		}
		checksumData, err := httpGet(client, checksumURL)
		if err == nil {
			for _, line := range strings.Split(string(checksumData), "\n") {
				if strings.Contains(line, archiveName) {
					parts := strings.Fields(line)
					if len(parts) >= 1 {
						expectedHash = parts[0]
					}
				}
			}
			if verbose {
				fmt.Println("ok")
			}
		} else if verbose {
			fmt.Println("skipped")
		}
	}

	if verbose {
		fmt.Printf("Downloading %s... ", archiveName)
	}
	archiveData, err := httpGet(client, archiveURL)
	if err != nil {
		return fail("network", fmt.Errorf("download failed: %w", err))
	}
	if verbose {
		fmt.Printf("ok (%d bytes)\n", len(archiveData))
	}

	if expectedHash != "" {
		actualHash := sha256Hex(archiveData)
		if actualHash != expectedHash {
			return fail("checksum", fmt.Errorf("checksum mismatch: expected %s, got %s", expectedHash, actualHash))
		}
		if verbose {
			fmt.Println("Checksum verified.")
		}
	}

	binary, err := extractBinaryFromTarGz(archiveData, "airskills")
	if err != nil {
		return fail("extract", fmt.Errorf("extraction failed: %w", err))
	}

	execPath, err := os.Executable()
	if err != nil {
		return fail("permission", fmt.Errorf("cannot find current binary: %w", err))
	}
	execPath, err = filepath.EvalSymlinks(execPath)
	if err != nil {
		return fail("permission", fmt.Errorf("cannot resolve binary path: %w", err))
	}

	newPath := execPath + ".new"
	if err := os.WriteFile(newPath, binary, 0755); err != nil {
		return fail("permission", fmt.Errorf("cannot write new binary (try: sudo airskills self-update): %w", err))
	}

	if err := os.Rename(newPath, execPath); err != nil {
		os.Remove(newPath)
		return fail("permission", fmt.Errorf("cannot replace binary (try: sudo airskills self-update): %w", err))
	}

	if verbose {
		fmt.Printf("Updated airskills to %s\n", latest)
	} else {
		fmt.Fprintf(os.Stderr, "airskills: updated to v%s\n", latest)
	}
	telemetry.Capture("cli_self_update_completed", map[string]interface{}{
		"from_version": currentVersion,
		"to_version":   latest,
		"trigger":      trigger,
		"duration_ms":  time.Since(startedAt).Milliseconds(),
	})
	return latest, nil
}

func httpGet(client *http.Client, url string) ([]byte, error) {
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

func extractBinaryFromTarGz(data []byte, name string) ([]byte, error) {
	gr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer gr.Close()

	tr := tar.NewReader(gr)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		// Match the binary name at any path depth
		if filepath.Base(hdr.Name) == name && !hdr.FileInfo().IsDir() {
			return io.ReadAll(tr)
		}
	}
	return nil, fmt.Errorf("binary %q not found in archive", name)
}
