# Airskills CLI

Go CLI for airskills. Public repo: `github.com/chrismdp/airskills`.

The platform (Next.js API server) is a separate private repo. E2e tests live there.

## Architecture

Single Go binary, no runtime dependencies. Cobra for CLI commands. Config and tokens stored in `~/.config/airskills/`.

### Key files

- `cmd/push.go` — push flow: scan skills, create tar.gz, upload via archive PUT
- `cmd/pull.go` — pull flow: compare content hashes, download/update/detect divergence
- `cmd/sync.go` — push then pull
- `cmd/api.go` — API client, skill/commit types, HTTP methods
- `cmd/hash.go` — Merkle content hash (must match server-side computation)
- `cmd/agents.go` — agent detection, skill scanning, file installation
- `cmd/add.go` — install public skills without auth, tar extraction
- `cmd/export.go` — export skills, download files from archive endpoint
- `install.sh` — curl installer, served via `airskills.ai/install.sh`

### Content hash

The Merkle hash algorithm must match the platform exactly:
1. For each file: SHA-256 of content → hex string
2. Sort entries by path using **byte order** (not locale order)
3. Join as `path:hash` separated by `\n`
4. SHA-256 of the manifest string

If this diverges from the server, multi-file skills will get `hash_mismatch` 400 errors.

### Conflict detection

Push sends `X-Expected-Hash` (content hash from marker) for conflict detection. Server compares against stored `content_hash`. On 409, CLI downloads remote version to a temp dir for merge.

Pull compares remote `content_hash` against local marker. If local files also changed (local hash ≠ marker hash), it's a divergence — remote saved to temp dir, local preserved.

### Size limits

CLI warns before upload if archive > 1MB (soft limit). Rejects if > 100MB (hard limit). Server enforces both.

### Releases

GoReleaser builds cross-platform binaries on `v*` tag push. GitHub Actions workflow at `.github/workflows/release.yml`. Homebrew tap is broken and not advertised — use `curl | bash` installer.

## Testing

E2e tests live in the platform repo. Run them with `CLI_REPO` pointing here to avoid re-cloning. Unit tests: `go test ./...`.

**Write a failing test first, then fix the bug.** All behaviour changes and bug fixes must start with a test that demonstrates the problem, then make it pass.
