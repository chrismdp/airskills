package cmd

import (
	"strings"
	"testing"
)

// TestRenderAgentNextStepsEmpty verifies the helper returns "" when there
// are no steps — lets callers emit unconditionally without a wrapper check.
func TestRenderAgentNextStepsEmpty(t *testing.T) {
	if got := renderAgentNextSteps(nil); got != "" {
		t.Errorf("expected empty string for no steps, got %q", got)
	}
	if got := renderAgentNextSteps([]agentNextStep{}); got != "" {
		t.Errorf("expected empty string for empty slice, got %q", got)
	}
}

// TestRenderAgentNextStepsLayout checks the block starts with a leading
// blank line + "Next steps:" header so the primary output above stays
// machine-parseable for callers that pipe it, and that each step is on
// its own indented line.
func TestRenderAgentNextStepsLayout(t *testing.T) {
	got := renderAgentNextSteps([]agentNextStep{
		{Cmd: "airskills status", Why: "see what's in sync"},
		{Cmd: "airskills sync", Why: "pull latest changes"},
	})
	if !strings.HasPrefix(got, "\nNext steps:\n") {
		t.Errorf("block should start with blank-line + 'Next steps:' header, got:\n%s", got)
	}
	for _, want := range []string{
		"  airskills status  — see what's in sync",
		"  airskills sync  — pull latest changes",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("block missing %q, got:\n%s", want, got)
		}
	}
}

// TestRenderAgentNextStepsOmitsWhyWhenEmpty confirms a step without a Why
// renders as just the command, so callers don't accidentally emit a
// dangling " — " separator.
func TestRenderAgentNextStepsOmitsWhyWhenEmpty(t *testing.T) {
	got := renderAgentNextSteps([]agentNextStep{
		{Cmd: "airskills whoami"},
	})
	if !strings.Contains(got, "  airskills whoami\n") {
		t.Errorf("step with empty Why should render bare, got:\n%s", got)
	}
	if strings.Contains(got, "—") {
		t.Errorf("step with empty Why should not include em-dash separator, got:\n%s", got)
	}
}
