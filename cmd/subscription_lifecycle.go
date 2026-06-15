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

// isPersonalSubscription reports whether a sourced overlay is a PERSONAL
// subscription — a non-org skill the caller `add`ed. Broader than
// isPersonalSubscriptionMarker: it does NOT require Backup==nil (an EDITED
// subscription is still a subscription) and does NOT require a SkillID (an
// anon-added one has none yet). Used by `rm` to decide whether to unsubscribe —
// an org overlay arrived via the org channel and has no subscription row to drop.
func isPersonalSubscription(entry *SyncEntry) bool {
	return entry != nil && entry.Source != nil &&
		entry.OwnerKind != "org" && entry.Source.SkillsetSlug == ""
}

// subscriptionOwnerLabel is the upstream owner shown as "added from <owner>"
// for a subscription. Falls back when the owner username isn't recorded (a
// pre-b0 marker, or a fresh device that hasn't seen the listing's owner_username
// yet).
func subscriptionOwnerLabel(m *SyncEntry) string {
	if m != nil && m.Source != nil && m.Source.Owner != "" {
		return m.Source.Owner
	}
	return "another user"
}

const (
	subUpstreamReadable  = iota // resolve 200, same skill → backfill subscribe
	subUpstreamMoved            // resolve 410 + readable successor → re-point
	subUpstreamGone             // resolve 404 / 410-no-successor → promote
	subUpstreamTransient        // 5xx / 429 / network → skip this sync
)

type subDisposition struct {
	kind int
	// Successor fields, set only for subUpstreamMoved. Taken from the resolve
	// moved_to response (present ONLY when the caller can read the successor —
	// an unreadable move returns a bare 410, classified Gone→promote). This is
	// how re-point learns the new owner: getSkill can't, it omits owner_username.
	successorID    string
	successorOwner string // new owner — username (user) or org slug (org)
	successorSlug  string // new skill slug
	successorKind  string // "user" | "org"
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
		if err := json.Unmarshal(body, &r); err != nil || r.ID == "" {
			// A 200 we can't parse (a proxy/CDN HTML page, a truncated body) is
			// NOT proof the skill is gone — treat it as transient, never promote.
			return subDisposition{kind: subUpstreamTransient}
		}
		if r.ID == trackedID {
			return subDisposition{kind: subUpstreamReadable}
		}
		// Parsed cleanly but the slug now resolves to a DIFFERENT skill (the
		// original was deleted and the name re-used) — the tracked upstream is gone.
		return subDisposition{kind: subUpstreamGone}
	case status == 410:
		var r struct {
			MovedTo *struct {
				SkillID   string `json:"skill_id"`
				Slug      string `json:"slug"`       // new owner (username or org slug)
				SkillSlug string `json:"skill_slug"` // new skill slug
				Kind      string `json:"kind"`       // "user" | "org"
			} `json:"moved_to"`
		}
		if json.Unmarshal(body, &r) == nil && r.MovedTo != nil && r.MovedTo.SkillID != "" {
			return subDisposition{
				kind:           subUpstreamMoved,
				successorID:    r.MovedTo.SkillID,
				successorOwner: r.MovedTo.Slug,
				successorSlug:  r.MovedTo.SkillSlug,
				successorKind:  r.MovedTo.Kind,
			}
		}
		// Bare 410 (no readable successor): the move went somewhere the caller
		// can't see — orphan it like any other gone-away skill.
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
		if !isReconcilableSubscription(entry) {
			continue
		}
		// subID is the upstream this subscription targets. It is the marker's
		// SkillID for an established overlay, and Source.UpstreamSkillID for a
		// marker added while anonymous (which has no SkillID yet — that's the
		// "registers on first login" case).
		subID := sourceUpstreamID(entry.Source)
		if inListing[subID] {
			// Already a live subscription (or otherwise reachable). Adopt the id
			// onto an anon marker so future syncs track it as an established
			// overlay.
			if entry.SkillID == "" {
				entry.SkillID = subID
			}
			continue
		}
		switch d := classifySubscriptionUpstream(client, entry.Source, subID); d.kind {
		case subUpstreamTransient:
			// Not definitive — leave it; the next sync retries.
		case subUpstreamReadable:
			// Backfill: register the subscription. A 403 means it became
			// unreadable between resolve and subscribe (a private-flip race) →
			// promote rather than retry a doomed subscribe. A transient error is
			// surfaced (not swallowed) so the user knows it'll retry.
			switch err := client.subscribe(subID); {
			case err == nil:
				entry.SkillID = subID // anon marker is now an established overlay
			case isForbiddenError(err):
				promoteSubscription(client, syncState, name, entry, localSkills, owners)
			default:
				fmt.Fprintf(os.Stderr, "  %s couldn't register a subscription for %q (%v) — will retry next sync\n",
					yellow("!"), name, err)
			}
		case subUpstreamMoved:
			repointSubscription(client, syncState, name, entry, d, localSkills, owners)
		case subUpstreamGone:
			promoteSubscription(client, syncState, name, entry, localSkills, owners)
		}
	}
}

// isReconcilableSubscription is the reconcile-pass predicate. It is broader than
// the presentation predicate isPersonalSubscriptionMarker: it ALSO matches a
// marker added while ANONYMOUS, which has no SkillID yet (only
// Source.UpstreamSkillID). Backfill needs that case — it's exactly the "added
// while logged out, then logged in" path that registers the subscription.
func isReconcilableSubscription(entry *SyncEntry) bool {
	if entry == nil || entry.Source == nil || entry.Backup != nil ||
		entry.OwnerKind == "org" || entry.Source.SkillsetSlug != "" ||
		entry.Deleted || entry.MovedTo != "" {
		return false
	}
	subID := sourceUpstreamID(entry.Source)
	return subID != "" && (entry.SkillID == "" || entry.SkillID == subID)
}

// promoteSubscription turns a vanished subscription into an owned skill, keeping
// the caller's local files. The transactional RPC creates the owned skill and
// deletes the subscription row together; the marker then flips to track the new
// owned skill (existing flipMarkerToPersonal — shared with the backup-promote
// arm so the two cannot drift).
func promoteSubscription(client *apiClient, syncState *SyncState, name string, entry *SyncEntry, localSkills map[string]string, owners *ownerResolver) {
	// subID is the source skill id the promote RPC keys provenance/idempotency
	// on — the marker's SkillID for an established overlay, Source.UpstreamSkillID
	// for a marker added while anonymous (SkillID still empty).
	subID := sourceUpstreamID(entry.Source)
	upstream := entry.Source.Owner + "/" + entry.Source.Slug
	dir := localSkills[name]
	files := map[string][]byte{}
	if dir != "" {
		files = readSkillFiles(dir)
	}
	if len(files) == 0 {
		// Nothing on disk to promote — the upstream is gone and we have no
		// bytes. Drop the dead subscription row so it stops being retried.
		_ = client.unsubscribe(subID)
		return
	}
	archive, err := createTarGz(dir)
	if err != nil {
		return // can't build the archive — retry next sync
	}
	hash := computeMerkleHash(files)
	skill, _, err := client.promote(subID, archive, name, name, entry.Version, hash)
	if err != nil {
		fmt.Fprintf(os.Stderr, "  %s %s: upstream %s is gone; saving your copy as your own skill failed (%v) — will retry next sync\n",
			yellow("!"), name, upstream, err)
		return
	}
	flipMarkerToPersonal(entry, name, skill.Id.String(), strDeref(skill.ContentHash), owners.callerUsername())
	syncState.Skills[name] = entry
	fmt.Printf("  %s %s: %s is no longer available — saved as your own skill\n", yellow("!"), name, upstream)
}

// repointSubscription follows a transferred upstream to a successor the caller
// can READ: subscribe to it, retrack the marker at its new home, drop the old
// row. There is no atomic UPDATE endpoint, so this is subscribe-then-unsubscribe
// — eventual-consistent (a failed second call is cleared next sync).
//
// "As if you'd added it from the new owner": the new owner/slug come from the
// resolve moved_to response (d), NOT getSkill — getSkill omits owner_username,
// which would blank Source.Owner and leave a marker that can never be re-resolved
// if the successor itself later vanishes. If the caller has LOST read access to
// the successor, subscribe 403s and we orphan it (promote) like any gone-away
// skill — matching the bare-410 path, which never reaches here.
func repointSubscription(client *apiClient, syncState *SyncState, name string, entry *SyncEntry, d subDisposition, localSkills map[string]string, owners *ownerResolver) {
	if err := client.subscribe(d.successorID); err != nil {
		if isForbiddenError(err) {
			// Lost read access to the successor between resolve and subscribe —
			// orphan it, exactly like a gone-away skill.
			promoteSubscription(client, syncState, name, entry, localSkills, owners)
		}
		return // transient → retry next sync
	}
	oldID := sourceUpstreamID(entry.Source)
	entry.SkillID = d.successorID
	entry.Source.Owner = d.successorOwner
	entry.Source.Slug = d.successorSlug
	entry.Source.ID = d.successorID
	entry.Source.UpstreamSkillID = d.successorID
	if d.successorKind != "" {
		entry.OwnerKind = d.successorKind
	}
	if d.successorOwner != "" {
		entry.OwnerSlug = d.successorOwner
	}
	seedCopyLedgerFromDisk(entry, name)
	syncState.Skills[name] = entry
	_ = client.unsubscribe(oldID)
	fmt.Printf("  %s %s: upstream was transferred — now following %s/%s\n",
		green("✓"), name, d.successorOwner, d.successorSlug)
}
