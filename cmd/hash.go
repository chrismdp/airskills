package cmd

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
)

// sha256Hex returns the hex-encoded SHA-256 hash of data.
func sha256Hex(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

// computeMerkleHash computes a Merkle-style content hash covering all files.
// Matches the server-side computeContentHash in lib/storage-utils.ts.
func computeMerkleHash(files map[string][]byte) string {
	type fileEntry struct {
		path string
		hash string
	}
	entries := make([]fileEntry, 0, len(files))
	for path, content := range files {
		entries = append(entries, fileEntry{path: path, hash: sha256Hex(content)})
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].path < entries[j].path
	})

	var lines []string
	for _, e := range entries {
		lines = append(lines, fmt.Sprintf("%s:%s", e.path, e.hash))
	}
	manifest := strings.Join(lines, "\n")
	return sha256Hex([]byte(manifest))
}
