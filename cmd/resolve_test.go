package cmd

import "testing"

func TestApplyResolveWritesResolvedHash(t *testing.T) {
	state := &SyncState{
		Version: 1,
		Skills: map[string]*SyncEntry{
			"heartbeat": {
				SkillID:     "id-1",
				ContentHash: "h-local",
				Source:      &skillSource{Owner: "chrismdp", Slug: "heartbeat"},
			},
		},
	}

	if err := applyResolve(state, "heartbeat", "h-upstream-current"); err != nil {
		t.Fatalf("applyResolve: %v", err)
	}
	if state.Skills["heartbeat"].ResolvedHash != "h-upstream-current" {
		t.Errorf("ResolvedHash = %q, want %q",
			state.Skills["heartbeat"].ResolvedHash, "h-upstream-current")
	}
}

func TestApplyResolveErrorsForUnknownSkill(t *testing.T) {
	state := &SyncState{Version: 1, Skills: map[string]*SyncEntry{}}
	if err := applyResolve(state, "ghost", "h"); err == nil {
		t.Error("expected error for unknown skill, got nil")
	}
}

func TestApplyResolveNoopForOwnedSkill(t *testing.T) {
	// Owned skill (no Source) — resolve is a no-op. ResolvedHash stays
	// empty. Ticket: "No-op for owned skills."
	state := &SyncState{
		Version: 1,
		Skills: map[string]*SyncEntry{
			"my-own": {SkillID: "id-1", ContentHash: "h"},
		},
	}
	err := applyResolve(state, "my-own", "anything")
	if err == nil {
		t.Error("expected applyResolve to error or no-op for owned skill")
		return
	}
	if state.Skills["my-own"].ResolvedHash != "" {
		t.Errorf("owned skill should not gain ResolvedHash, got %q",
			state.Skills["my-own"].ResolvedHash)
	}
}

func TestApplyResolveOverwritesPriorResolvedHash(t *testing.T) {
	// Resolving twice (e.g. after upstream moves and user re-reviews)
	// must overwrite the prior value, not append.
	state := &SyncState{
		Version: 1,
		Skills: map[string]*SyncEntry{
			"heartbeat": {
				SkillID:      "id-1",
				ContentHash:  "h-local",
				ResolvedHash: "h-upstream-old",
				Source:       &skillSource{Owner: "chrismdp", Slug: "heartbeat"},
			},
		},
	}

	if err := applyResolve(state, "heartbeat", "h-upstream-new"); err != nil {
		t.Fatalf("applyResolve: %v", err)
	}
	if state.Skills["heartbeat"].ResolvedHash != "h-upstream-new" {
		t.Errorf("expected overwrite to %q, got %q",
			"h-upstream-new", state.Skills["heartbeat"].ResolvedHash)
	}
}
