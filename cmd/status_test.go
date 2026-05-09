package cmd

import (
	"reflect"
	"testing"
)

func TestClassifyForStatusFlagsTrackedOnServerNoMarker(t *testing.T) {
	// Skill exists on the server, exists locally, but no marker —
	// next sync would surface a conflict. Doctor catches this; status
	// must too. Regression for the silent-skip in cmd/status.go where
	// the trackedName == "" + local-exists case fell off the loop.
	remote := []apiSkill{
		{Id: testUUID("id-foo"), Name: "foo", ContentHash: strPtr("rh1")},
	}
	local := map[string]string{
		"foo": "/agent/skills/foo",
	}
	state := &SyncState{Skills: map[string]*SyncEntry{}}

	got := classifyForStatus(remote, local, state)
	if !reflect.DeepEqual(got.untracked, []string{"foo"}) {
		t.Fatalf("untracked = %v, want [foo]", got.untracked)
	}
	if len(got.toPull) != 0 || len(got.toPush) != 0 || len(got.toUpdate) != 0 {
		t.Fatalf("expected only untracked bucket populated, got %+v", got)
	}
}

func TestClassifyForStatusBuckets(t *testing.T) {
	remote := []apiSkill{
		{Id: testUUID("id-tracked-clean"), Name: "tracked-clean", ContentHash: strPtr("h-clean")},
		{Id: testUUID("id-tracked-changed"), Name: "tracked-changed", ContentHash: strPtr("h-new")},
		{Id: testUUID("id-not-local"), Name: "not-local", ContentHash: strPtr("h-x")},
		{Id: testUUID("id-untracked"), Name: "untracked-skill", ContentHash: strPtr("h-y")},
	}
	local := map[string]string{
		"tracked-clean":   "/agent/skills/tracked-clean",
		"tracked-changed": "/agent/skills/tracked-changed",
		"untracked-skill": "/agent/skills/untracked-skill",
		"local-only":      "/agent/skills/local-only",
	}
	state := &SyncState{Skills: map[string]*SyncEntry{
		"tracked-clean":   {SkillID: testUUID("id-tracked-clean").String(), ContentHash: "h-clean"},
		"tracked-changed": {SkillID: testUUID("id-tracked-changed").String(), ContentHash: "h-old"},
	}}

	got := classifyForStatus(remote, local, state)
	if !reflect.DeepEqual(got.toPush, []string{"local-only"}) {
		t.Errorf("toPush = %v, want [local-only]", got.toPush)
	}
	if !reflect.DeepEqual(got.toPull, []string{"not-local"}) {
		t.Errorf("toPull = %v, want [not-local]", got.toPull)
	}
	if !reflect.DeepEqual(got.toUpdate, []string{"tracked-changed"}) {
		t.Errorf("toUpdate = %v, want [tracked-changed]", got.toUpdate)
	}
	if !reflect.DeepEqual(got.untracked, []string{"untracked-skill"}) {
		t.Errorf("untracked = %v, want [untracked-skill]", got.untracked)
	}
}
