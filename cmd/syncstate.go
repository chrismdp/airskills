package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// SyncEntry tracks the sync state of a single skill.
type SyncEntry struct {
	SkillID     string       `json:"skill_id"`
	Version     string       `json:"version"`
	ContentHash string       `json:"content_hash,omitempty"`
	Tool        string       `json:"tool"`
	Source      *skillSource `json:"source,omitempty"`
}

// SyncState holds sync metadata for all tracked skills.
// Stored at ~/.config/airskills/sync.json, keyed by local directory name.
type SyncState struct {
	Version int                   `json:"version"`
	Skills  map[string]*SyncEntry `json:"skills"`
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
