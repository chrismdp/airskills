package cmd

import (
	"fmt"
	"io"
	"os"
	"strings"
)

// agentNextStep is one "what could the agent do next" hint attached to a
// command's output. Cmd is the full command the agent can run (e.g.
// "airskills status"), Why is a one-line rationale for choosing it.
type agentNextStep struct {
	Cmd string
	Why string
}

// renderAgentNextSteps builds the "Next steps:" block that trails a
// command's primary output. The block is separated from whatever came
// before by a leading blank line and a "Next steps:" header, so text
// above stays machine-parseable for callers that pipe it — the contract
// spelled out in doc/changes/cli-agent-affordances.md.
//
// Returns an empty string when there are no steps, letting callers
// emit it unconditionally via printAgentNextSteps.
func renderAgentNextSteps(steps []agentNextStep) string {
	if len(steps) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\nNext steps:\n")
	for _, s := range steps {
		if s.Why == "" {
			fmt.Fprintf(&b, "  %s\n", s.Cmd)
			continue
		}
		fmt.Fprintf(&b, "  %s  — %s\n", s.Cmd, s.Why)
	}
	return b.String()
}

// printAgentNextSteps writes the rendered block to w, but only when
// the process is not attached to a TTY (so an agent or script is the
// likely reader) and only when AIRSKILLS_NO_HINTS is not set.
//
// Rationale: a human on a TTY has `airskills --help` and shell history;
// an extra "Next steps:" block is just noise. An agent driving the CLI
// can't consult the docs mid-flow and benefits from an explicit nudge
// toward the likely follow-up. Test harnesses that assert exact output
// can opt out via AIRSKILLS_NO_HINTS=1.
func printAgentNextSteps(w io.Writer, steps []agentNextStep) {
	if isTTY {
		return
	}
	if os.Getenv("AIRSKILLS_NO_HINTS") != "" {
		return
	}
	block := renderAgentNextSteps(steps)
	if block == "" {
		return
	}
	fmt.Fprint(w, block)
}
