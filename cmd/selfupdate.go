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
	fmt.Printf("Current version: %s\n", version)

	// Fetch latest release
	fmt.Print("Checking for updates... ")
	client := &http.Client{Timeout: 30 * time.Second}
	req, err := http.NewRequest("GET", releasesURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to check for updates: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return fmt.Errorf("GitHub API returned %d (is the repo public?)", resp.StatusCode)
	}

	var release ghRelease
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return fmt.Errorf("failed to parse release: %w", err)
	}

	latest := strings.TrimPrefix(release.TagName, "v")
	if !isNewer(latest, version) && version != "dev" {
		fmt.Printf("already on latest (%s)\n", version)
		return nil
	}
	fmt.Printf("found %s\n", latest)

	// Find the right archive for this OS/arch
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
		return fmt.Errorf("no release found for %s/%s", runtime.GOOS, runtime.GOARCH)
	}

	// Download checksum file
	var expectedHash string
	if checksumURL != "" {
		fmt.Print("Downloading checksums... ")
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
			fmt.Println("ok")
		} else {
			fmt.Println("skipped")
		}
	}

	// Download archive
	fmt.Printf("Downloading %s... ", archiveName)
	archiveData, err := httpGet(client, archiveURL)
	if err != nil {
		return fmt.Errorf("download failed: %w", err)
	}
	fmt.Printf("ok (%d bytes)\n", len(archiveData))

	// Verify checksum
	if expectedHash != "" {
		actualHash := sha256Hex(archiveData)
		if actualHash != expectedHash {
			return fmt.Errorf("checksum mismatch: expected %s, got %s", expectedHash, actualHash)
		}
		fmt.Println("Checksum verified.")
	}

	// Extract binary from tar.gz
	binary, err := extractBinaryFromTarGz(archiveData, "airskills")
	if err != nil {
		return fmt.Errorf("extraction failed: %w", err)
	}

	// Replace current binary
	execPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("cannot find current binary: %w", err)
	}
	execPath, err = filepath.EvalSymlinks(execPath)
	if err != nil {
		return fmt.Errorf("cannot resolve binary path: %w", err)
	}

	newPath := execPath + ".new"
	if err := os.WriteFile(newPath, binary, 0755); err != nil {
		return fmt.Errorf("cannot write new binary (try: sudo airskills self-update): %w", err)
	}

	if err := os.Rename(newPath, execPath); err != nil {
		os.Remove(newPath)
		return fmt.Errorf("cannot replace binary (try: sudo airskills self-update): %w", err)
	}

	fmt.Printf("Updated airskills to %s\n", latest)
	return nil
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
