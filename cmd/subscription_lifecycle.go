package cmd

import (
	"encoding/json"
	"fmt"
	"os"
)

// Subscription lifecycle (cli-add-subscribes-skill-to-account, Phase B).
//
// A subscription is a BARE personal overlay marker: it tracks an upstream skill
// the caller `add`ed but does not own (Source set, no Backup), recorded in their
// account as a skillset_skills row so the add follows them across machines. This
// pass keeps those rows honest on every logged-in sync, deriving everything from
// facts the client already has — is the marker's skill in the full effective
// listing, and does `resolve` still find it — with NO privileged server signal:
//
//   - BACKFILL — a readable upstream not yet a live subscription (e.g. it was
//     added while anonymous) → subscribe it. Idempotent; this is the "registers
//     on first login" path.
//   - PROMOTE  — a subscribed upstream that's gone (deleted or made private, not
//     distinguished) → promote the local copy to an owned skill via the one
//     transactional RPC, so the user keeps their files.
//   - RE-POINT — a transferred upstream → follow it to the readable successor.
//
// Org skills are out of scope: they arrive via the org channel and keep
// remove-on-leave behaviour (the absence of a subscription row is the "not
// explicitly mine" signal). Edited subscriptions (a Backup exists) are handled
// by sweepOverlayBackups, which promotes the backup fork; this pass only touches
// the bare ones.

// isPersonalSubscriptionMarker reports whether a marker is a bare personal
// subscription this pass owns: an overlay tracking a non-org upstream, with no
// edits backed up and not a transfer tombstone.
func isPersonalSubscriptionMarker(entry *SyncEntry) bool {
	return entry != nil &&
		entry.Source != nil &&
		entry.Backup == nil &&
		entry.OwnerKind != "org" &&
		entry.Source.SkillsetSlug == "" &&
		entry.SkillID != "" &&
		entry.SkillID == sourceUpstreamID(entry.Source) &&
		!entry.Deleted && entry.MovedTo == ""
}

const (
	subUpstreamReadable  = iota // resolve 200, same skill → backfill subscribe
	subUpstreamMoved            // resolve 410 + readable successor → re-point
	subUpstreamGone             // resolve 404 / 410-no-successor → promote
	subUpstreamTransient        // 5xx / 429 / network → skip this sync
)

type subDisposition struct {
	kind        int
	successorID string
}

// classifySubscriptionUpstream resolves a subscription's upstream and classifies
// the result EXPLICITLY by status code — never isGoneError's substring match,
// which a 5xx proxy body ("not found") could spoof into a false promote.
func classifySubscriptionUpstream(client *apiClient, source *skillSource, trackedID string) subDisposition {
	if source == nil || source.Owner == "" || source.Slug == "" {
		// No owner/slug to resolve against — can't classify, so don't act.
		return subDisposition{kind: subUpstreamTransient}
	}
	body, status, err := client.getWithStatus(fmt.Sprintf("/api/v1/resolve/%s/%s", source.Owner, source.Slug))
	if err != nil {
		return subDisposition{kind: subUpstreamTransient} // network — not definitive
	}
	switch {
	case status == 200:
		var r struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(body, &r) == nil && r.ID == trackedID {
			return subDisposition{kind: subUpstreamReadable}
		}
		// The slug now resolves to a DIFFERENT skill (the original was deleted
		// and the name re-used) — the tracked upstream is gone.
		return subDisposition{kind: subUpstreamGone}
	case status == 410:
		var r struct {
			MovedTo *struct {
				SkillID string `json:"skill_id"`
			} `json:"moved_to"`
		}
		if json.Unmarshal(body, &r) == nil && r.MovedTo != nil && r.MovedTo.SkillID != "" {
			return subDisposition{kind: subUpstreamMoved, successorID: r.MovedTo.SkillID}
		}
		return subDisposition{kind: subUpstreamGone}
	case status == 404 || status == 401:
		// Deleted or made private — RLS gives the same answer, not distinguished.
		return subDisposition{kind: subUpstreamGone}
	default:
		// 429, 5xx, and anything unexpected: not definitive, skip this sync.
		return subDisposition{kind: subUpstreamTransient}
	}
}

// reconcileSubscriptions runs on every logged-in pull, AFTER the install/remove
// loop. inListing is the membership of the caller's full server-side effective
// set (NOT the probe-augmented or shadow-filtered slice — a probe-fetched
// not-yet-subscribed marker must still count as absent so backfill fires).
func reconcileSubscriptions(client *apiClient, syncState *SyncState, inListing map[string]bool, localSkills map[string]string, owners *ownerResolver) {
	if client == nil || syncState == nil {
		return
	}
	// Snapshot names first: promote/re-point mutate syncState.Skills.
	names := make([]string, 0, len(syncState.Skills))
	for name := range syncState.Skills {
		names = append(names, name)
	}
	for _, name := range names {
		entry := syncState.Skills[name]
		if !isPersonalSubscriptionMarker(entry) {
			continue
		}
		if inListing[entry.SkillID] {
			continue // already a live subscription (or otherwise reachable) — nothing to do
		}
		switch d := classifySubscriptionUpstream(client, entry.Source, entry.SkillID); d.kind {
		case subUpstreamTransient:
			// Not definitive — leave it; the next sync retries.
		case subUpstreamReadable:
			// Backfill: register the subscription. A 403 means it became
			// unreadable between resolve and subscribe (a private-flip race) →
			// promote rather than retry a doomed subscribe.
			if err := client.subscribe(entry.SkillID); err != nil && isForbiddenError(err) {
				promoteSubscription(client, syncState, name, entry, localSkills, owners)
			}
		case subUpstreamMoved:
			repointSubscription(client, syncState, name, entry, d.successorID, localSkills, owners)
		case subUpstreamGone:
			promoteSubscription(client, syncState, name, entry, localSkills, owners)
		}
	}
}

// promoteSubscription turns a vanished subscription into an owned skill, keeping
// the caller's local files. The transactional RPC creates the owned skill and
// deletes the subscription row together; the marker then flips to track the new
// owned skill (existing flipMarkerToPersonal — shared with the backup-promote
// arm so the two cannot drift).
func promoteSubscription(client *apiClient, syncState *SyncState, name string, entry *SyncEntry, localSkills map[string]string, owners *ownerResolver) {
	upstream := entry.Source.Owner + "/" + entry.Source.Slug
	dir := localSkills[name]
	files := map[string][]byte{}
	if dir != "" {
		files = readSkillFiles(dir)
	}
	if len(files) == 0 {
		// Nothing on disk to promote — the upstream is gone and we have no
		// bytes. Drop the dead subscription row so it stops being retried.
		_ = client.unsubscribe(entry.SkillID)
		return
	}
	archive, err := createTarGz(dir)
	if err != nil {
		return // can't build the archive — retry next sync
	}
	hash := computeMerkleHash(files)
	skill, _, err := client.promote(entry.SkillID, archive, name, name, entry.Version, hash)
	if err != nil {
		fmt.Fprintf(os.Stderr, "  %s %s: upstream %s is gone; saving your copy as your own skill failed (%v) — will retry next sync\n",
			yellow("!"), name, upstream, err)
		return
	}
	flipMarkerToPersonal(entry, name, skill.Id.String(), strDeref(skill.ContentHash), owners.callerUsername())
	syncState.Skills[name] = entry
	fmt.Printf("  %s %s: %s is no longer available — saved as your own skill\n", yellow("!"), name, upstream)
}

// repointSubscription follows a transferred upstream to its successor: subscribe
// to the successor (readability-guarded server-side), drop the old row, and
// retrack the marker. There is no atomic UPDATE endpoint, so this is
// subscribe-then-unsubscribe — eventual-consistent (a failed second call is
// cleared next sync). If the successor is unreadable the subscribe 403s → fall
// through to promote.
func repointSubscription(client *apiClient, syncState *SyncState, name string, entry *SyncEntry, successorID string, localSkills map[string]string, owners *ownerResolver) {
	if err := client.subscribe(successorID); err != nil {
		if isForbiddenError(err) {
			promoteSubscription(client, syncState, name, entry, localSkills, owners)
		}
		return // transient → retry next sync
	}
	oldID := entry.SkillID
	successor, gerr := client.getSkill(successorID)
	if gerr != nil || successor == nil {
		// Subscribed, but couldn't read the successor's metadata to retrack the
		// marker now. Leave the marker; next sync sees the successor in the
		// listing and links it. Still drop the dead old row.
		_ = client.unsubscribe(oldID)
		return
	}
	entry.SkillID = successorID
	if src := owners.sourceFor(successor); src != nil {
		entry.Source = src
	}
	if kind, slug := owners.resolve(successor); kind != "" {
		entry.OwnerKind = kind
		entry.OwnerSlug = slug
	}
	entry.ContentHash = strDeref(successor.ContentHash)
	entry.Version = successor.Version
	seedCopyLedgerFromDisk(entry, name)
	syncState.Skills[name] = entry
	_ = client.unsubscribe(oldID)
	fmt.Printf("  %s %s: upstream was transferred — now following its new home\n", green("✓"), name)
}
