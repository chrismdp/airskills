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

// TestPushConflictInstructionsAgentVariant verifies that the per-conflict
// block switches from "tell your AI agent" (TTY) to a direct imperative
// addressing the agent when non-TTY, while still naming both file paths.
func TestPushConflictInstructionsAgentVariant(t *testing.T) {
	c := conflictInfo{
		name:       "foo",
		localPath:  "/tmp/local/SKILL.md",
		remotePath: "/tmp/remote/SKILL.md",
	}

	human := pushConflictResolutionInstructions(c, false)
	agent := pushConflictResolutionInstructions(c, true)

	if !strings.Contains(human, "tell your AI agent") {
		t.Errorf("human variant should say 'tell your AI agent', got:\n%s", human)
	}
	if strings.Contains(agent, "tell your AI agent") {
		t.Errorf("agent variant should not delegate back to the user's agent, got:\n%s", agent)
	}
	if !strings.Contains(strings.ToLower(agent), "you are an agent") {
		t.Errorf("agent variant should open with 'You are an agent', got:\n%s", agent)
	}
	for _, want := range []string{c.localPath, c.remotePath} {
		if !strings.Contains(human, want) {
			t.Errorf("human variant missing %q", want)
		}
		if !strings.Contains(agent, want) {
			t.Errorf("agent variant missing %q", want)
		}
	}
}

// TestPullDivergenceFooterAgentVariant verifies the footer under the
// diverged-skills list swaps tone based on TTY and keeps the --force hint
// and the sourced-skill options in both variants.
func TestPullDivergenceFooterAgentVariant(t *testing.T) {
	human := pullDivergenceFooter(true, false)
	agent := pullDivergenceFooter(true, true)

	for _, want := range []string{"push --force"} {
		if !strings.Contains(human, want) {
			t.Errorf("human footer missing %q", want)
		}
		if !strings.Contains(agent, want) {
			t.Errorf("agent footer missing %q", want)
		}
	}

	if !strings.Contains(human, "your agent can") {
		t.Errorf("human footer should say 'your agent can', got:\n%s", human)
	}
	if strings.Contains(agent, "your agent can") {
		t.Errorf("agent footer should not say 'your agent can' — it IS the agent, got:\n%s", agent)
	}
	if !strings.Contains(strings.ToLower(agent), "you are an agent") {
		t.Errorf("agent footer should open with 'You are an agent', got:\n%s", agent)
	}

	// When hasSourced is false, the sourced-skill options block should be
	// absent from both variants.
	agentNoSource := pullDivergenceFooter(false, true)
	if strings.Contains(agentNoSource, "a)") || strings.Contains(agentNoSource, "owner") {
		t.Errorf("agent footer without hasSourced should not include fork options, got:\n%s", agentNoSource)
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
