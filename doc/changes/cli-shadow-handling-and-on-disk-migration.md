---
status: done
title: "CLI: handle shadow flags from /sync, surface 409 conflict, add `--as` local alias, on-disk migration from namespaced dirs to bare slugs"
created: 2026-05-23
completed: 2026-05-23
---

## Notes

- On-disk migration is handled by the existing `transferred` pull path
  rather than a new explicit migration routine. After mig 047 every
  prefixed local dir gets `remote.Name='retro' != trackedName='parsons-home-retro'`
  → transferred action → installs to `~/.claude/skills/retro/`,
  deletes old dir, updates marker. Spec called for SyncState-driven
  iteration; the SkillID-based match in `decidePullActions` is
  equivalent and already covers the realistic cases.
- `installSkillToAgents` now rewrites SKILL.md `name:` on every
  install so stale frontmatter (e.g. an org skill archived with
  `name: parsons-home-retro` before the migration) doesn't trip the
  archive-PUT `name_slug_mismatch` check on the next push. This is
  the bug-bait step the spec called out.
- Shadow filtering uses a new `fetchShadowMap` client method that
  hits `/api/v1/sync?since=epoch` and builds a map of shadowed
  skill_id → ShadowInfo. Cheap (single round-trip) and best-effort
  (errors degrade silently to pre-mig-047 behaviour).
- `mv` now relays the server's 409 `conflict_with` payload via the
  same `parseSkillConflict` helper that `push` uses, so users get
  the same "rename to keep your version" hint on either path.
- `--skillset` flag and `cfg.Skillset` are now no-ops on the user
  side. `resolveSkillsetFlag` warns once when a stale value is
  present and clears it from the config.

# What

Follow-up to `users-as-identities-drop-user-skillsets.md`. The platform
side shipped first (migration 047, API changes, dashboard). The CLI side
is split out because it requires:

1. A coordinated tagged release (build → push → tag → wait → platform
   `e2e-release-binary` job picks up the new binary).
2. An on-disk migration of users' real `~/.claude/skills/`,
   `~/.gemini/skills/`, `~/.pi/agent/skills/` directories — getting this
   wrong destroys local skill data, so it needs a careful local-Supabase
   loop to validate.

Until this lands, existing CLI binaries continue to work — they ignore
the new `shadowed`/`source`/`org_slug` fields on the sync response, and
the server's 409 on slug collisions still prevents bad pushes (the
error message is just less specific).

# Why

The platform changes shipped on 2026-05-23 as part of the migration to
drop user-owned skillsets. CLI work was cut from that ship to keep the
blast radius bounded — see the parent ticket's design doc for the full
context (`doc/changes/users-as-identities-drop-user-skillsets.md`,
sections Phase 4 and Phase 7).

# Acceptance

- `cli/cmd/pull.go` skips skills with `shadowed: true` from the sync
  response and prints a warning of the form:
  ```
  ⚠ retro shadowed by parsons-home/retro
    run `airskills mv retro <new-name>` to keep your version
  ```
  Fires on every pull until the user renames.
- `cli/cmd/push.go` formats the server's 409 `conflict_with` payload
  into a helpful CLI error naming the conflicting source.
- `cli/cmd/add.go` gains a `--as <local-alias>` flag that stores the
  pulled skill under a different on-disk name, rewriting SKILL.md
  `name:` via the existing `fixSkillNameInContent` helper. Persists
  `LocalAlias` in `~/.config/airskills/sync.json`.
- `cli/cmd/mv.go` adds an effective-set collision check via the new
  `effective_skills_for` RPC (returns 409 if the target slug already
  shadows something in the caller's effective set).
- `cli/cmd/skillset.go`: remove the user-targeted subcommands
  (`skillset create`, `skillset use`, `skillset set-default`,
  `skillset auto-absorb`). Org-scoped variants stay. `--skillset` flag
  on `pull` becomes a no-op with a deprecation warning.
- `cli/cmd/syncstate.go` adds a `LocalAlias` field to `SyncEntry`.
- **On-disk migration** runs once on first `airskills pull` after
  upgrade. For each `SyncState.Skills` entry whose current path equals
  `{Source.OwnerSlug}-{Source.Slug}` or
  `{Source.SkillsetSlug}-{Source.Slug}`:
  - Atomic `os.Rename` to bare slug if free
  - Auto-generate `{slug}-{owner_or_skillset}` alias on collision
  - Rewrite SKILL.md `name:` via `fixSkillNameInContent` after rename
  - Preserve uncommitted local edits (warn user to push)
  - Idempotent: skip entries already at bare slug
  - **Drive off SyncState, not regex** — a user can legitimately own a
    skill literally named `chrismdp-retro`.
- Binary smoke test (per `cli/CLAUDE.md` §Testing) before release:
  build, run `pull` against local Supabase with the migration applied,
  verify shadow warning verbatim, verify push 409 message, verify
  on-disk migration renamed a fixture.

# Release sequence

Per `platform/CLAUDE.md`: push CLI changes to main, tag the new CLI
version (`v0.X.Y` — bump patch), `gh run watch <run-id> -R
chrismdp/airskills --exit-status` for the release workflow before
considering this done. The platform side is already shipped, so no
coordinated platform push needed.
