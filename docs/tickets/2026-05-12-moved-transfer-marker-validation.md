---
status: todo
title: Validate moved/transfer marker handling against a live transfer
created: 2026-05-12
---

# What

After a skill is `airskills transfer`'d (or otherwise re-rooted under a different owner), other machines that still hold the old marker should detect the move on their next `airskills sync`, drop the stale marker, and keep the local dir as the user's own untracked copy — surfacing a clear message about what happened, what the next sync will do, and how to either follow updates at the new owner or discard.

The classifier and per-skill messaging for this case already exist (see `cmd/skipped_marker.go` `actionMovedKeep`, applied in `cmd/push.go`'s post-filter block). They have unit-test coverage via `httptest` but have NOT been exercised against a real server-side transfer. This ticket is to validate the live behaviour and fix whatever drift surfaces.

# Why

The orphan-marker fix shipped just before this ticket was filed. Its empirical end-to-end smoke test on the live server worked: push `orphan-test` → `airskills rm` it → restore the marker on the same machine → run sync → observe the `1 orphan removed` summary, the dir cleanup, and the `Undo: airskills restore` instruction. The orphan-with-edits variant was also smoke-tested and produced the expected 4-line warning.

The moved/transfer path takes a different branch (`classifyMarkerSkill` returns `markerStateMoved` instead of `markerStateOrphan`, populating `ownerSlug`/`skillSlug` from `GET /api/v1/skills/<id>`). Unit-test pass ≠ live correctness here because:

- The server response shape under a real transfer may differ from the unit-test fixtures. Real responses might have partial or differently-named fields, especially under the v2 transfer model where the originating skill_id is soft-deleted and a new row is created at the target owner (see `cmd/transfer.go:108-126`).
- The "moved to" target needs to be a real, addressable `<owner>/<slug>` pair so the printed `airskills add` command actually works for the user.
- The recovery path (`airskills rm --keep-remote <name> && airskills add <new-owner>/<slug>`) needs to be re-validated end-to-end as the user-facing remediation.

# Acceptance

- A controlled live smoke test exists for the moved case: create a throwaway skill on machine A, push, sync on machine B (or simulate B via local marker save/restore as the orphan test did), transfer to an org or another user on A, sync on the lagging machine.
- The lagging-machine sync surfaces a `1 moved (re-link needed)` summary part with the per-skill warning naming the new `<owner>/<slug>` correctly.
- Running the printed `airskills rm --keep-remote <name> && airskills add <new-owner>/<slug>` recovery command leaves the lagging machine with a working consumer install of the new skill under the new owner namespace.
- If the live response shape diverges from `cmd/marker_resolve.go:147-167` parsing, fix the parser and update the classifier unit tests in `cmd/skipped_marker_test.go` to match.

# Open question — should pull also do an orphan sweep?

Today the orphan/moved classifier only runs from inside `push.go`'s filter loop. If a user runs `airskills pull` directly (not `sync`), or `push` is a no-op because they're anonymous / have no markers to push, orphans go undetected on this invocation.

Decide whether this is worth fixing. Arguments either way:

- **Skip it.** `airskills sync` is the recommended path and runs push first, so any logged-in user with markers will hit the classifier. The classifier needs an authenticated client to call `/api/v1/skills/<id>`, so an anonymous pull couldn't run it anyway.
- **Add it.** A symmetric sweep in `cmd/pull.go` makes orphan detection invocation-independent. Possible shape: after `decidePullActions`, iterate `syncState.Skills` for entries whose `SkillID` isn't in any returned `remoteSkills`, and run the same classifier-orchestrate-act block. The natural refactor is to extract the orchestration from `push.go` into a shared helper.

Either decision is fine — pick one and document in a comment near the existing push classifier block. Don't leave both files silently divergent.

# Pointers

- Existing classifier: `cmd/skipped_marker.go` (covers all four action kinds; unit-tested)
- Push integration: the filter-and-classify block in `cmd/push.go` (look for `skippedCandidates` / `classifySkippedMarker`)
- Server-truth resolver: `classifyMarkerSkill` in `cmd/marker_resolve.go:133-170`
- Existing transfer flow on the originating machine: `cmd/transfer.go:95-126`
- Recent smoke-test recipe used for the orphan case is reproducible — same save-sync.json / `rm` / restore-marker pattern works for transfer if you swap `rm` for `transfer --to-org`.
