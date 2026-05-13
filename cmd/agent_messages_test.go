package cmd

import (
	"strings"
	"testing"
)

// TestAgentSuggestionInstructionsAddressesAgent verifies the non-TTY variant
// of the suggestions output addresses the agent directly (not the user) and
// names every skill that has a pending upstream suggestion.
func TestAgentSuggestionInstructionsAddressesAgent(t *testing.T) {
	prompts := []pendingSuggestionPrompt{
		{
			name:             "foo",
			suggesterSkillID: "skill-1",
			source: &skillSource{
				Owner: "alice",
				Slug:  "foo-original",
				ID:    "orig-1",
			},
		},
		{
			name:             "bar",
			suggesterSkillID: "skill-2",
			source: &skillSource{
				Owner: "bob",
				Slug:  "bar-original",
				ID:    "orig-2",
			},
		},
	}

	got := agentSuggestionInstructions(prompts)

	for _, want := range []string{"foo", "bar", "alice/foo-original", "bob/bar-original"} {
		if !strings.Contains(got, want) {
			t.Errorf("agentSuggestionInstructions missing %q in:\n%s", want, got)
		}
	}
	// Should clearly address the agent, not the user.
	if !strings.Contains(strings.ToLower(got), "agent") {
		t.Errorf("agentSuggestionInstructions should mention 'agent', got:\n%s", got)
	}
	// Should mention collaborating with the user.
	if !strings.Contains(strings.ToLower(got), "user") {
		t.Errorf("agentSuggestionInstructions should mention 'user', got:\n%s", got)
	}
}

// TestAgentSuggestionInstructionsEmpty verifies the helper returns an empty
// string when there are no pending prompts (nothing to print).
func TestAgentSuggestionInstructionsEmpty(t *testing.T) {
	got := agentSuggestionInstructions(nil)
	if got != "" {
		t.Errorf("expected empty string for no prompts, got %q", got)
	}
}

// TestConflictResolutionMessageFormat verifies the new three-outcome message
// includes all required sections: three commands, two recovery paths, and the
// "NEVER edit metadata" warning.
func TestConflictResolutionMessageFormat(t *testing.T) {
	entries := []conflictEntry{
		{
			name:      "my-skill",
			localDir:  "/home/user/.claude/skills/my-skill",
			remoteDir: "/tmp/airskills-conflicts/abc/my-skill",
			source:    nil,
		},
	}

	msg := conflictResolutionMessage(entries, false)

	for _, want := range []string{
		"my-skill",
		"push --force my-skill",
		"pull --force my-skill",
		"airskills sync",
		"After push --force",
		"After pull --force",
		"NEVER edit airskills metadata",
		"~/.config/airskills/sync.json",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("conflict message missing %q in:\n%s", want, msg)
		}
	}

	// Path display should show skill directories, not SKILL.md paths.
	if strings.HasSuffix(strings.TrimSpace(msg), "SKILL.md") {
		t.Errorf("conflict message should show directory paths, not SKILL.md file paths")
	}
}

// TestConflictResolutionMessageSourcedCaveat verifies the sourced-skill caveat
// is present when Source is non-nil and absent when it is nil.
func TestConflictResolutionMessageSourcedCaveat(t *testing.T) {
	base := conflictEntry{
		name:      "borrowed",
		localDir:  "/home/user/.claude/skills/borrowed",
		remoteDir: "/tmp/conflict/borrowed",
	}

	withoutSource := conflictResolutionMessage([]conflictEntry{base}, false)
	if strings.Contains(withoutSource, "sourced from") {
		t.Errorf("message without Source should not include sourced caveat, got:\n%s", withoutSource)
	}

	withSource := base
	withSource.source = &skillSource{Owner: "alice", Slug: "borrowed-original"}
	withMsg := conflictResolutionMessage([]conflictEntry{withSource}, false)
	if !strings.Contains(withMsg, "alice/borrowed-original") {
		t.Errorf("message with Source should include owner/slug, got:\n%s", withMsg)
	}
	if !strings.Contains(withMsg, "sourced from") {
		t.Errorf("message with Source should mention 'sourced from', got:\n%s", withMsg)
	}
}

// TestConflictResolutionMessageBothBranchesShowCommands verifies both reader
// variants retain the three resolution commands.
func TestConflictResolutionMessageBothBranchesShowCommands(t *testing.T) {
	entries := []conflictEntry{
		{name: "foo", localDir: "/local/foo", remoteDir: "/remote/foo"},
	}
	tty := conflictResolutionMessage(entries, false)
	headless := conflictResolutionMessage(entries, true)

	// Both should contain the three outcomes
	for _, want := range []string{"push --force foo", "pull --force foo", "airskills sync"} {
		if !strings.Contains(tty, want) {
			t.Errorf("TTY message missing %q", want)
		}
		if !strings.Contains(headless, want) {
			t.Errorf("headless message missing %q", want)
		}
	}
}

func TestConflictResolutionMessageTTYAddressesHuman(t *testing.T) {
	msg := conflictResolutionMessage([]conflictEntry{
		{name: "foo", localDir: "/local/foo", remoteDir: "/remote/foo"},
	}, false)

	if !strings.Contains(msg, "Ask your agent") {
		t.Fatalf("TTY message should ask the human to involve their agent, got:\n%s", msg)
	}
	if strings.Contains(msg, "You are an agent") {
		t.Fatalf("TTY message should not address the reader as an agent, got:\n%s", msg)
	}
}

func TestConflictResolutionMessageHeadlessAddressesAgent(t *testing.T) {
	msg := conflictResolutionMessage([]conflictEntry{
		{name: "foo", localDir: "/local/foo", remoteDir: "/remote/foo"},
	}, true)

	if !strings.Contains(msg, "You are an agent") {
		t.Fatalf("headless message should address the agent directly, got:\n%s", msg)
	}
	if strings.Contains(msg, "Ask your agent") {
		t.Fatalf("headless message should not ask the agent to ask itself, got:\n%s", msg)
	}
}

func TestConflictResolutionMessageBranchesDifferOnlyAtOpening(t *testing.T) {
	entries := []conflictEntry{
		{name: "foo", localDir: "/local/foo", remoteDir: "/remote/foo"},
	}
	tty := conflictResolutionMessage(entries, false)
	headless := conflictResolutionMessage(entries, true)

	if tty == headless {
		t.Fatal("TTY and headless messages should differ at their addressed reader text")
	}

	normalize := func(s string) string {
		s = strings.ReplaceAll(s,
			"Ask your agent to read /local/foo and /remote/foo and decide with you\nhow to resolve. The agent should then run one of:",
			"<opening>")
		s = strings.ReplaceAll(s,
			"You are an agent. Read /local/foo and /remote/foo and decide with the\nuser how to resolve. Then run one of:",
			"<opening>")
		s = strings.ReplaceAll(s, "the changes you want to keep", "the changes to keep")
		return s
	}

	if normalize(tty) != normalize(headless) {
		t.Fatalf("conflict message branches drifted beyond opening/merge clause.\nTTY:\n%s\nHeadless:\n%s", tty, headless)
	}
}

// TestReviewGuideAgentVariantAddressesAgent verifies that the agent variant
// of the review guide explicitly addresses the agent as the reader, while
// the TTY variant keeps the existing human-oriented phrasing.
func TestReviewGuideAgentVariantAddressesAgent(t *testing.T) {
	agent := reviewGuideText(true)
	human := reviewGuideText(false)

	if !strings.Contains(strings.ToLower(agent), "you are an agent") {
		t.Errorf("agent review guide should open with 'You are an agent', got:\n%s", agent)
	}
	if strings.Contains(strings.ToLower(human), "you are an agent") {
		t.Errorf("human review guide should NOT address an agent, got:\n%s", human)
	}
	// Both should still describe the core workflow steps.
	for _, want := range []string{"airskills review download", "airskills review accept", "airskills review decline"} {
		if !strings.Contains(agent, want) {
			t.Errorf("agent guide missing %q", want)
		}
		if !strings.Contains(human, want) {
			t.Errorf("human guide missing %q", want)
		}
	}
}
