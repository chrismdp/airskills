---
status: done
title: "CLI: `airskills rm <skill>/<path>` removes a single file from a skill (mirror-proof) and pushes the deletion"
created: 2026-06-05
completed: 2026-06-05
---

## Problem

From CLI feedback (2026-06-05): there was no way to remove a *single
file* from a multi-file skill. Hand-deleting the file locally and
running `airskills push` appeared to drop it, but the next sync
resurrected it — the mirror fan-out copies a skill back from any sibling
agent dir that still has the file. `airskills rm` only removed whole
skills. The reporter's workaround was a `.askignore` entry listing the
moved paths so the mirror stopped re-creating them.

Root cause: deletion is not a detectable signal from content hash + mtime
alone. The mirror cannot distinguish "user deleted file X from dir A"
from "dir B is ahead and has file X that dir A never received". The only
robust signal is an *explicit* user statement of intent — the same reason
whole-skill deletion needs `airskills rm` rather than `rm -rf`.

## Change

`airskills rm` now accepts a path form: `airskills rm <skill>/<relpath>`.

1. Deletes the file from **every** detected agent copy of the skill
   (defeating the mirror — it finds a single consistent version with the
   file already gone everywhere, so it restores nothing), then prunes any
   parent dirs the deletion left empty (never the skill root).
2. Pushes the skill (scoped, via the existing push path) so the server
   archive and the sync-state marker hash drop the file too. The server's
   archive PUT replaces whole-skill content, so no platform/API change was
   needed.
3. `--keep-remote` stops after the local removal (no push). `--keep-local`
   and `--pending` are rejected for the path form (not meaningful).
4. `SKILL.md` is refused (removing the manifest silently breaks the skill
   — point the user at `airskills rm <skill>`). Path traversal, absolute
   paths, and empty paths are rejected. Errors clearly when the file is in
   no installed copy or the skill isn't installed locally.

CLI-only change. New helpers `removeLocalSkillFile`,
`validateSkillFileRelPath`, `pruneEmptyParents`, and `runRemoveSkillFile`
in `cmd/rm.go`.

## Validation

- 4 new unit tests in `cmd/rm_test.go` (removes across agents, keeps
  non-empty parents, refuses bad paths incl. `SKILL.md`/traversal, errors
  when missing). Full `go test ./...` green; `go vet` + `gofmt` clean.
- Smoke-tested the built binary against an isolated `HOME` with the same
  skill in two agent dirs: the file was removed from both copies while
  `SKILL.md` and sibling files survived, the empty `scripts/` dir was
  pruned, and every guard (SKILL.md, traversal, missing file, unknown
  skill) produced the expected error.
- NOT exercised locally: the real server push half of the default (no
  `--keep-remote`) path, which needs auth + a live server. It reuses the
  existing, e2e-covered scoped `push` flow; only the local pre-deletion is
  new.

## Docs

Updated `cli-reference.mdx` (`airskills rm` synopsis + file-removal
paragraph) and `getting-started/delete.mdx` (new "Removing a single file
from a skill" section).

## Part 2 — automatic detection on push/sync

The explicit command covers the deliberate case. The second half makes the
*natural* workflow (hand-delete a file, then `push`/`sync`) safe, per the
follow-up feedback ("on push, say these files are missing — permanently
remove? y/n").

A new resolver (`cmd/intra_skill_deletions.go`) runs as a pre-pass before
the mirror in both `push` (standalone branch) and `pull` (which also covers
`sync`, since sync calls `runPull`; in a sync run push skips its own mirror
via `syncActiveConflicts`, so it fires exactly once — one shared code path,
two call sites, no proliferation):

- **Baseline = the remote manifest.** The other mirror alone can't
  disambiguate "deleted here" from "added there, not yet propagated" — the
  remote is the decisive baseline. A file present on the remote but missing
  from a local copy is a confirmed deletion. The manifest is **paths only**:
  `GET /api/v1/skills/<id>` already returns a `files: [{path,size}]` array
  (`getSkillFilePaths`), so no archive download is needed to detect a
  deletion — far lighter than pulling bodies. (The full archive is fetched
  only on the rare *restore* path, to get the content back.)
- **Cheap, network-free gate**: only skills whose local hash differs from
  the marker (`skillChangedVsMarker`) cost a manifest fetch — and that's
  exactly the set push is about to upload anyway. Unchanged skills (the
  overwhelming majority) cost nothing. This covers **both** single-agent
  and multi-agent deletions, unlike a cross-copy-only gate.
- **Policy**: `push --force` → remove permanently (`deletionRemove`); a TTY
  → prompt per skill (`deletionAsk`); headless → keep + restore from remote
  + print a hint (`deletionKeep`). Headless never destroys without
  confirmation — the whole agent fleet syncs non-interactively.
- `removeMissing` deletes from every copy (reusing `removeLocalSkillFile`)
  so the mirror can't resurrect, then push drops it server-side. "Missing
  from at least one copy" (not from the union) is the detection rule, so a
  partial multi-agent delete is caught too. `restoreMissing` downloads the
  archive lazily (only when keeping) and rewrites the file where any copy
  lacks it. `SKILL.md` and `.askignore`-excluded paths are never flagged.
- A `suppressDeletionPrompt` guard stops the explicit `rm <skill>/<path>`
  command's internal push from re-prompting to restore what it just
  deleted.

### Part 2 validation

- 9 unit tests in `cmd/intra_skill_deletions_test.go`: pure detection
  (incl. never-manifest, ignore-aware), the changed-skill gate, the remove/
  restore helpers, and three end-to-end resolver runs against a mock server
  serving the manifest (`GET /skills/<id>`) + archive (keep restores, force
  removes everywhere, suppressed = no-op). Two existing push conflict-test
  mocks gained a manifest handler. Full `go test ./...` green; `go vet` +
  `gofmt` clean.

### Code-review fix

A high-effort review caught one real bug: the resolver ran *before* push's
scope filter, so `airskills push <skill> --force` would have run deletion
removal across **every** changed skill, not just the named one — it could
permanently delete files from an unrelated skill. Fixed by threading the
push scope (`args`) into `resolveIntraSkillDeletions`; a non-empty scope
confines detection/removal to those skills. `pull`/`sync`/full-push pass an
empty scope (all skills). Covered by `TestResolveIntraSkillDeletionsRespectsScope`.

## Follow-up

Needs a tagged CLI release (`v*`) before users get it — binary behaviour
changed. Platform docs ship on push to main.
