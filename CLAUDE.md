# Airskills CLI

Go CLI for airskills. Public repo: `github.com/chrismdp/airskills`.

The platform (Next.js API server) is a separate private repo. E2e tests live there.

## Source of truth: the agentskills.io spec

Airskills builds **to** the open Agent Skills format at
<https://agentskills.io/specification>. The spec is the source of truth for
on-disk skill layout. Two rules to never violate:

1. **`name` field must equal the parent directory name.** A skill in
   `~/.claude/skills/foo/` has `name: foo` in its SKILL.md frontmatter.
   Validators (`skills-ref validate`) reject any mismatch.
2. **`name` is `[a-z0-9-]+`** (lowercase alphanumeric + hyphens, no leading
   or trailing hyphen, no consecutive hyphens, max 64 chars).

What this means for our design:

- **Never rename a local skill directory without also rewriting the
  SKILL.md `name:` field.** Doing one without the other produces an
  invalid skill.
- **Don't prefix dir names server-slug-mismatched.** If the server slug is
  `foo`, the local dir should be `foo` with `name: foo`, not
  `cherrypick-foo/` with `name: foo` (mismatch) or `cherrypick-foo/` with
  `name: cherrypick-foo` (content drift from server).
- **Org "namespacing" lives in the marker, not on disk.** The marker
  (`~/.config/airskills/sync.json`) records `OwnerKind`/`OwnerSlug` so the
  CLI knows which namespace a local skill lives in. The on-disk dir name
  matches the server slug, full stop.
- **On collision at install time, abort with conflict-to-tmp.** Don't
  silently rename to disambiguate. The user (or their agent) decides.

## Command semantics

Two command verbs you must never conflate:

- **`airskills mv <old> <new>`** — rename. Same owner, new name. Consumer-transparent: the skill's `skill_id` is stable, consumers pinning by `skill_id` keep getting updates. Must hard-reject any attempt to change owner.
- **`airskills transfer <skill> --to-org <slug>` / `--to-user`** — ownership change. Server-side this is "soft-delete old + create new with new `skill_id`". Consumers see an "upstream archived" event on their next pull. Old slug returns 410.

If code ever looks like it's trying to change owner via the name-PUT path (`PUT /api/v1/skills/:id` with `{name}`), that's wrong. Owner change goes through the transfer endpoint.

User-facing docs for this split live at `airskills.ai/docs/getting-started/rename` and `airskills.ai/docs/concepts/transfers`. If you change behaviour in either command, keep those pages in sync.

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
