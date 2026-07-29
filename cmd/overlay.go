package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Overlay lifecycle passes (cli-one-skill-overlay-and-lineage-split.md).
//
// A non-owned skill is always ONE skill locally: the marker tracks the
// UPSTREAM's id, local edits are an overlay, and the server-side copy of
// those edits lives in a hidden backup fork referenced only by
// marker.Backup. These passes run during pull/sync and keep that shape
// true across devices and time:
//
//   - healOverlayMarkers     — legacy markers that track a backup row
//                              directly flip to the overlay shape
//   - reconstructOverlayMarkers — a fresh device rebuilds overlays from
//                              the server's backup rows instead of
//                              installing them as visible second skills
//   - sweepOverlayBackups    — retires backups that became redundant
//                              (backup == upstream) and promotes backups
//                              whose upstream is genuinely lost

// isBackupRow reports whether a listed skill row is a hidden overlay-backup
// fork — plumbing that must never render as a row, install as a second
// skill, or count as a conflict. The ONE predicate every surface shares.
func isBackupRow(s *apiSkill) bool {
	return s != nil && s.Backup && s.ForkedFrom != nil
}

// dropBackupRows filters hidden backup forks out of a listing for surfaces
// that only need the visible rows (force-pull, keep-local).
func dropBackupRows(skills []apiSkill) []apiSkill {
	out := make([]apiSkill, 0, len(skills))
	for i := range skills {
		if !isBackupRow(&skills[i]) {
			out = append(out, skills[i])
		}
	}
	return out
}

// retireSuggestion withdraws the caller's pending suggestion and reports
// whether it is now safe to treat as retired (withdrawn, or already
// resolved by the owner). On a transient failure the suggestion may still
// be PENDING — callers must keep the id and must NOT delete the backup
// fork, or the owner is left reviewing a suggestion whose suggester skill
// is gone (the exact broken state the withdraw exists to prevent).
func retireSuggestion(client *apiClient, id string) bool {
	if id == "" {
		return true
	}
	if err := client.withdrawSuggestion(id); err == nil {
		return true
	}
	// The withdraw can lose benign races (owner accepted/declined first,
	// another device already withdrew) — confirm by reading the row.
	if s, gerr := client.getSuggestion(id); gerr == nil && s != nil && string(s.Status) != "pending" {
		return true
	}
	return false
}

// adoptUpstreamIdentity points a marker at the resolved upstream id when
// `add owner/slug --force` finds it tracking something else. The stale id
// is usually the hidden backup fork from the pre-overlay fork-on-push era
// — confirmed via lookup, it moves into entry.Backup; a gone row (404/410)
// is simply dropped. The caller's own VISIBLE fork keeps its identity here
// — its retirement (server delete + repoint) is
// retireVisibleForkOnTakeUpstream's job, which runs first and is gated on
// the delete succeeding — as does anything a transient lookup failure
// leaves unconfirmed.
func adoptUpstreamIdentity(entry *SyncEntry, upstreamID string, lookup func(id string) (*apiSkill, error)) {
	if entry == nil || upstreamID == "" || entry.SkillID == upstreamID {
		return
	}
	if entry.SkillID == "" {
		entry.SkillID = upstreamID
		return
	}
	if entry.Backup != nil || lookup == nil {
		return
	}
	old, err := lookup(entry.SkillID)
	if err != nil {
		if isGoneError(err) {
			entry.SkillID = upstreamID
		}
		return
	}
	if isBackupRow(old) && old.ForkedFrom.String() == upstreamID {
		entry.Backup = &backupRef{SkillID: entry.SkillID, ContentHash: strDeref(old.ContentHash)}
		entry.SkillID = upstreamID
	}
}

// retireVisibleForkOnTakeUpstream implements take-upstream for the
// visible-fork shape (marker tracks the caller's own fork of the
// upstream): soft-delete the fork row, then repoint the marker at the
// upstream. Taking the upstream wholesale means the fork's job is done —
// the same retirement rule backup forks already follow. Without this,
// add --force self-reverts: the marker keeps tracking the fork, whose
// server row still holds the old bytes, and the next pull fast-forwards
// them back over the upstream's
// (doc/changes/cli-visible-fork-take-upstream-gap.md).
//
// The repoint is gated on the delete succeeding: repointing while the
// fork row survives would leave it in the listing unmatched by any
// marker, and the next pull would resurrect it locally as a name
// collision. On delete failure the marker is untouched and the error is
// returned so the caller can warn. Consumers pinning the fork see the
// standard "upstream archived" event, same as a transfer.
func retireVisibleForkOnTakeUpstream(entry *SyncEntry, upstreamID string, lookup func(id string) (*apiSkill, error), deleteSkill func(id string) error) (bool, error) {
	if entry == nil || upstreamID == "" || entry.SkillID == "" || entry.SkillID == upstreamID {
		return false, nil
	}
	if lookup == nil || deleteSkill == nil {
		return false, nil
	}
	old, err := lookup(entry.SkillID)
	if err != nil || old == nil {
		// Gone and transient rows are adoptUpstreamIdentity's department.
		return false, nil
	}
	if old.Backup || old.ForkedFrom == nil || old.ForkedFrom.String() != upstreamID {
		return false, nil
	}
	if err := deleteSkill(entry.SkillID); err != nil {
		return false, err
	}
	entry.SkillID = upstreamID
	entry.Backup = nil
	return true, nil
}

// healOverlayMarkers flips markers that track an owned skill whose server
// row is flagged backup=true (set for legacy shadow-fork shapes by the
// lineage-split backfill) into the overlay shape: SkillID becomes the
// upstream, the fork id moves into Backup. Keying on the server flag — not
// lineage equality — matters: the lineage split repoints forked_from to
// transfer successors, so a stale marker whose Source still names the
// tombstone would never match an equality predicate. Idempotent by shape:
// once flipped, the marker no longer tracks a backup row.
//
// SuggestDeclined / SuggestionID survive verbatim (per-bytes decline
// memory is orthogonal to where the bytes live).
func healOverlayMarkers(syncState *SyncState, remoteByID map[string]*apiSkill, owners *ownerResolver) []string {
	var healed []string
	for name, entry := range syncState.Skills {
		if entry == nil || entry.SkillID == "" || entry.Backup != nil {
			continue
		}
		row := remoteByID[entry.SkillID]
		if row == nil || !row.Backup || row.ForkedFrom == nil {
			continue
		}
		upstreamID := row.ForkedFrom.String()
		entry.Backup = &backupRef{SkillID: entry.SkillID, ContentHash: strDeref(row.ContentHash)}
		entry.SkillID = upstreamID
		if up := remoteByID[upstreamID]; up != nil {
			src := owners.sourceFor(up)
			// Keep the baseline the user actually incorporated — sourceFor
			// reads the CURRENT upstream hash, which would silently mark
			// any standing upstream advance as already seen.
			if src != nil && entry.Source != nil && sourceBaselineHash(entry.Source) != "" {
				src.UpstreamContentHash = entry.Source.UpstreamContentHash
				src.ContentHash = entry.Source.ContentHash
				src.UpstreamVersion = entry.Source.UpstreamVersion
			}
			if src != nil {
				entry.Source = src
			}
			if kind, slug := owners.resolve(up); kind != "" {
				entry.OwnerKind = kind
				entry.OwnerSlug = slug
			}
		} else if entry.Source != nil {
			entry.Source.ID = upstreamID
			entry.Source.UpstreamSkillID = upstreamID
		}
		healed = append(healed, name)
	}
	return healed
}

// reconstructOverlayMarkers rebuilds overlay markers on a fresh device: a
// personal skill in the caller's listing with backup=true is plumbing, not
// an installable row. When no marker references it yet, the user's edits
// (the backup fork's bytes) are installed locally and the marker is
// written tracking the UPSTREAM with Backup set — never a visible second
// skill. Explicit forks (backup=false) never reach here.
func reconstructOverlayMarkers(
	client *apiClient,
	syncState *SyncState,
	backupRows []apiSkill,
	remoteByID map[string]*apiSkill,
	localSkills map[string]string,
	owners *ownerResolver,
) []string {
	referenced := map[string]bool{}
	markerNameByUpstream := map[string]string{}
	for name, entry := range syncState.Skills {
		if entry == nil {
			continue
		}
		if entry.Backup != nil {
			referenced[entry.Backup.SkillID] = true
		}
		if entry.SkillID != "" {
			markerNameByUpstream[entry.SkillID] = name
		}
	}

	var rebuilt []string
	for i := range backupRows {
		row := backupRows[i]
		if row.ForkedFrom == nil || referenced[row.Id.String()] {
			continue
		}
		upstreamID := row.ForkedFrom.String()
		if name, tracked := markerNameByUpstream[upstreamID]; tracked {
			// A marker already tracks this upstream (it just hadn't recorded
			// the backup ref — e.g. written by an older CLI). Attach the ref.
			if entry := syncState.Skills[name]; entry != nil && entry.Backup == nil {
				entry.Backup = &backupRef{SkillID: row.Id.String(), ContentHash: strDeref(row.ContentHash)}
				rebuilt = append(rebuilt, name)
			}
			continue
		}

		dirName := row.Slug
		entry := &SyncEntry{
			SkillID:     upstreamID,
			Version:     row.Version,
			ContentHash: strDeref(row.ContentHash),
			Tool:        "claude-code",
			Backup:      &backupRef{SkillID: row.Id.String(), ContentHash: strDeref(row.ContentHash)},
		}
		up := remoteByID[upstreamID]
		if up == nil && client != nil {
			// User-owned upstreams are never in the effective listing —
			// fetch directly so the Source carries a real baseline (without
			// one, upstream advances would never surface on this device).
			if fetched, err := client.getSkill(upstreamID); err == nil {
				up = fetched
			}
		}
		if up != nil {
			entry.Source = owners.sourceFor(up)
			if kind, slug := owners.resolve(up); kind != "" {
				entry.OwnerKind = kind
				entry.OwnerSlug = slug
			}
		} else {
			entry.Source = &skillSource{
				Slug:            row.Slug,
				ID:              upstreamID,
				UpstreamSkillID: upstreamID,
			}
		}

		if _, exists := localSkills[dirName]; !exists {
			// Install the user's edits (the backup bytes) locally.
			files, err := downloadSkillFiles(client, row.Id.String())
			if err != nil || len(files) == 0 {
				continue
			}
			if _, err := installSkillToAgents(dirName, files); err != nil {
				continue
			}
			if home, err := os.UserHomeDir(); err == nil {
				// Register the fresh install so the action classifier later
				// in this run sees the dir without a rescan.
				localSkills[dirName] = filepath.Join(home, ".claude", "skills", dirName)
			}
		}
		seedCopyLedgerFromDisk(entry, dirName)
		syncState.Skills[dirName] = entry
		rebuilt = append(rebuilt, dirName)
		fmt.Printf("  %s %s %s\n", green("+"), dirName, dim("(your edits, restored from your backup copy)"))
	}
	return rebuilt
}

// sweepOverlayBackups walks every overlay marker with a Backup and either
// retires the backup (it became redundant — its content equals the
// upstream's, i.e. the suggestion was accepted or the owner converged on
// the same bytes) or promotes it to a visible personal skill (the upstream
// is genuinely lost, with no moved_to chain to follow). The redundancy
// guard is backup==upstream, NOT local==upstream: after `incoming
// incorporate` local equals upstream but the backup still holds the user's
// proposal — it stays, and the suggestion stays open for the owner.
func sweepOverlayBackups(
	client *apiClient,
	syncState *SyncState,
	remoteByID map[string]*apiSkill,
	owners *ownerResolver,
) {
	for name, entry := range syncState.Skills {
		if entry == nil || entry.Backup == nil || entry.Source == nil {
			continue
		}
		if entry.SkillID != sourceUpstreamID(entry.Source) {
			continue // not an overlay marker
		}
		upstreamID := entry.SkillID

		upstreamHash := ""
		upstreamKnown := false
		if up := remoteByID[upstreamID]; up != nil {
			upstreamHash = strDeref(up.ContentHash)
			upstreamKnown = true
		} else {
			// Not in the effective listing — normal for user-owned
			// upstreams, terminal for lost org skills. Ask the server.
			if up, err := client.getSkill(upstreamID); err == nil && up != nil {
				upstreamHash = strDeref(up.ContentHash)
				upstreamKnown = true
			} else if err != nil && isGoneError(err) {
				if upstreamMovedSomewhereVisible(client, entry.Source) {
					// A transfer the moved-source notices flow will surface;
					// not a loss. Leave the overlay alone this run.
					continue
				}
				promoteBackupToVisible(client, syncState, name, entry, owners)
				continue
			}
		}

		if upstreamKnown && upstreamHash != "" && entry.Backup.ContentHash == upstreamHash {
			// The backup is redundant — the user's bytes ARE upstream now.
			// The suggestion MUST be retired before the fork goes: deleting
			// the fork under a still-pending suggestion breaks the owner's
			// review. A transient withdraw failure just retries next sync.
			if !retireSuggestion(client, entry.SuggestionID) {
				continue
			}
			entry.SuggestionID = ""
			if err := client.del("/api/v1/skills/" + entry.Backup.SkillID); err == nil {
				entry.Backup = nil
				fmt.Printf("  %s %s %s\n", green("✓"), name, dim("backup retired (your edits are upstream now)"))
			}
		}
	}
}

// flipMarkerToPersonal is THE upstream-lost marker shape, shared by pull's
// promoteBackupToVisible and push's promoteEditsToPersonalSkill so the two
// arms cannot drift apart: the marker tracks a caller-owned visible skill,
// Source stays for lineage, all overlay/tombstone state clears.
func flipMarkerToPersonal(entry *SyncEntry, name, skillID, contentHash, username string) {
	entry.SkillID = skillID
	if contentHash != "" {
		entry.ContentHash = contentHash
	}
	entry.OwnerKind = "user"
	if username != "" {
		entry.OwnerSlug = username
	}
	entry.Backup = nil
	entry.SuggestionID = ""
	entry.Deleted = false
	entry.MovedTo = ""
	seedCopyLedgerFromDisk(entry, name)
}

// promoteBackupToVisible is the pull-side arm of the upstream-lost
// transition: the overlay's upstream is gone (member left the org, the org
// removed the skill or the assignment, or a user-owned upstream was
// deleted), so the backup fork — the user's only server-side copy — becomes
// a visible personal skill the marker flips to track.
func promoteBackupToVisible(client *apiClient, syncState *SyncState, name string, entry *SyncEntry, owners *ownerResolver) {
	if err := client.promoteBackupSkill(entry.Backup.SkillID); err != nil {
		fmt.Printf("  %s %s: upstream is gone; promoting your backup copy failed (%v) — will retry next sync\n",
			yellow("!"), name, err)
		return
	}
	flipMarkerToPersonal(entry, name, entry.Backup.SkillID, entry.Backup.ContentHash, owners.callerUsername())
	syncState.Skills[name] = entry
	src := entry.Source.Owner + "/" + entry.Source.Slug
	fmt.Printf("  %s %s: upstream %s is gone — your edited copy is now a personal skill in your account\n",
		yellow("!"), name, src)
}

// isGoneError reports whether an API error means the skill no longer
// exists (or is invisible) — 404/410 — as opposed to a transient failure.
func isGoneError(err error) bool {
	if status, ok := httpErrorStatus(err); ok {
		return status == 404 || status == 410
	}
	msg := err.Error()
	return strings.Contains(msg, "(404)") || strings.Contains(msg, "(410)") || strings.Contains(msg, "not found")
}

// upstreamMovedSomewhereVisible checks the resolve endpoint for a
// transferred upstream whose new location the caller can see. A visible
// move is NOT a loss — the moved-source notices flow handles it.
func upstreamMovedSomewhereVisible(client *apiClient, source *skillSource) bool {
	if source == nil || source.Owner == "" || source.Slug == "" {
		return false
	}
	body, status, err := client.getWithStatus(fmt.Sprintf("/api/v1/resolve/%s/%s", source.Owner, source.Slug))
	if err != nil || status != 410 {
		return false
	}
	var resp struct {
		MovedTo *struct {
			SkillID string `json:"skill_id"`
		} `json:"moved_to"`
	}
	if json.Unmarshal(body, &resp) != nil {
		return false
	}
	return resp.MovedTo != nil && resp.MovedTo.SkillID != ""
}
