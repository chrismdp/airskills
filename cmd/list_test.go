package cmd

import "testing"

func TestListStateLabel(t *testing.T) {
	cases := []struct {
		state SkillState
		want  string
	}{
		{StateSynced, "synced"},
		{StateModified, "modified"},
		{StateModifiedPending, "modified*"},
		{StateUntracked, "untracked"},
		{StateLinked, "untracked"},
		{StateUntrackedConflict, "untracked"},
		{StateNotLocal, "—"},
	}
	for _, c := range cases {
		got := listStateLabel(c.state)
		if got != c.want {
			t.Errorf("listStateLabel(%s) = %q, want %q", c.state, got, c.want)
		}
	}
}

func TestListStateLabelUnknownDefaultsToDash(t *testing.T) {
	if got := listStateLabel(SkillState("garbage")); got != "—" {
		t.Errorf("expected dash for unknown state, got %q", got)
	}
}
