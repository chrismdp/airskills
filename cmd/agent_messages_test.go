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
