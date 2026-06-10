package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// CopyState records what one on-disk copy of a skill looked like at the last
// time the mirror reconciled it. A skill lives in N agent directories, each
// independently editable; a single ContentHash can't say which copy moved.
// The per-copy baseline can. SyncedAt is a display hint only — it must never
// be the thing that decides which edit wins (that path silently discards an
// edit; see cli-per-copy-skill-divergence.md).
type CopyState struct {
	Hash     string    `json:"hash"`
	SyncedAt time.Time `json:"synced_at,omitempty"`
}

// backupRef points at the hidden server-side backup fork that holds the
// user's local edits to a non-owned skill (the overlay model — see
// platform/doc/changes/cli-one-skill-overlay-and-lineage-split.md). The
// marker's SkillID stays on the UPSTREAM skill; this ref is the only place
// the backup row's id appears. Present iff the user has divergence they
// can't write upstream.
type backupRef struct {
	SkillID     string `json:"skill_id"`
	ContentHash string `json:"content_hash,omitempty"`
}

// SyncEntry tracks the sync state of a single skill.
//
// OwnerKind / OwnerSlug record the skill's CURRENT namespace as last seen on
// the server. They get updated after every push so a server-side transfer is
// picked up on the next push without a separate sync step. If they change
// between pushes, the local dir is renamed to match.
type SyncEntry struct {
	SkillID     string `json:"skill_id"`
	Version     string `json:"version"`
	ContentHash string `json:"content_hash,omitempty"`
	Tool        string `json:"tool"`
	// Copies is the per-copy reconciliation ledger, keyed by absolute
	// skill-dir path. Seeded at marker creation (seedCopyLedgerFromDisk)
	// and maintained by the mirror (mirrorLocalSkills); used to tell which
	// agent copy of a skill diverged. Empty only for legacy markers, in
	// which case a divergence is surfaced for reconciliation rather than
	// guessed (never mtime, never the marker-wide hash). See
	// cli-per-copy-skill-divergence.md.
	Copies    map[string]CopyState `json:"copies,omitempty"`
	OwnerKind string               `json:"owner_kind,omitempty"` // "user" or "org"
	OwnerSlug string               `json:"owner_slug,omitempty"` // e.g. "chrismdp" or "cherrypick"
	// LocalAlias is the on-disk directory name when it differs from
	// the server slug. Set by `airskills add --as <alias>` and by the
	// on-disk migration when it has to disambiguate a rename
	// collision. Empty means "dir name matches server slug." The
	// marker stays the source of truth — see CLAUDE.md "Org
	// namespacing lives in the marker, not on disk."
	LocalAlias string       `json:"local_alias,omitempty"`
	Source     *skillSource `json:"source,omitempty"`
	// Backup references the hidden backup fork holding this skill's local
	// edits when the caller can't write the upstream. Only meaningful on
	// overlay markers (SkillID == the upstream's id). Cleared when the
	// edits land upstream (admin fold-in / accepted suggestion) or the
	// backup is promoted to a visible personal skill (upstream lost).
	Backup *backupRef `json:"backup,omitempty"`
	// ResolvedHash records the upstream content hash the user last
	// reviewed against via `airskills resolve`. Only meaningful for
	// sourced skills (Source != nil). Empty for owned skills, and for
	// sourced skills the user has never resolved against — in that
	// case the classifier treats any divergence as modified-pending.
	ResolvedHash    string `json:"resolved_hash,omitempty"`
	SuggestionID    string `json:"suggestion_id,omitempty"`
	SuggestDeclined bool   `json:"suggest_declined,omitempty"`
	// Deleted is set when the skill was transferred away and local edits
	// prevent removing the old dir. Pushes are blocked for deleted markers.
	Deleted bool   `json:"deleted,omitempty"`
	MovedTo string `json:"moved_to,omitempty"` // new dir name after transfer
}

// SyncState holds sync metadata for all tracked skills.
// Stored at ~/.config/airskills/sync.json, keyed by local directory name.
type SyncState struct {
	Version int                   `json:"version"`
	Skills  map[string]*SyncEntry `json:"skills"`
	// LastSuggestionNotifyAt is the cutoff for printing suggestion
	// accept/decline notifications. Anything reviewed at or before this
	// has already been shown. Stateless alternative to tracking IDs.
	LastSuggestionNotifyAt string `json:"last_suggestion_notify_at,omitempty"`
}

func syncStatePath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "airskills", "sync.json")
}

func loadSyncState() *SyncState {
	data, err := os.ReadFile(syncStatePath())
	if err != nil {
		return &SyncState{Version: 1, Skills: map[string]*SyncEntry{}}
	}
	var state SyncState
	if json.Unmarshal(data, &state) != nil {
		return &SyncState{Version: 1, Skills: map[string]*SyncEntry{}}
	}
	if state.Skills == nil {
		state.Skills = map[string]*SyncEntry{}
	}
	return &state
}

func saveSyncState(state *SyncState) error {
	path := syncStatePath()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
