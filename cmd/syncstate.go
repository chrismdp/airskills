package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// SyncEntry tracks the sync state of a single skill.
//
// OwnerKind / OwnerSlug record the skill's CURRENT namespace as last seen on
// the server. They get updated after every push so a server-side transfer is
// picked up on the next push without a separate sync step. If they change
// between pushes, the local dir is renamed to match.
type SyncEntry struct {
	SkillID         string       `json:"skill_id"`
	Version         string       `json:"version"`
	ContentHash     string       `json:"content_hash,omitempty"`
	Tool            string       `json:"tool"`
	OwnerKind       string       `json:"owner_kind,omitempty"` // "user" or "org"
	OwnerSlug       string       `json:"owner_slug,omitempty"` // e.g. "chrismdp" or "cherrypick"
	Source          *skillSource `json:"source,omitempty"`
	SuggestionID    string       `json:"suggestion_id,omitempty"`
	SuggestDeclined bool         `json:"suggest_declined,omitempty"`
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
