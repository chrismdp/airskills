package cmd

import (
	"fmt"
	"strings"
)

// agentSuggestionInstructions returns the block that push prints in place of
// the interactive "create a suggestion?" prompt when it's running headless.
// It addresses the agent directly and names every forked skill that has a
// pending upstream suggestion, so the agent can decide with the user which
// ones to send back by re-running push interactively.
//
// Returns an empty string when there are no prompts, so callers can emit it
// unconditionally without a wrapper check.
func agentSuggestionInstructions(prompts []pendingSuggestionPrompt) string {
	if len(prompts) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("\n=== Forked skills with upstream suggestions pending ===\n\n")
	b.WriteString("You are an agent running airskills non-interactively. The skills below\n")
	b.WriteString("were originally added from another user's skill, and your push updated\n")
	b.WriteString("them. airskills can optionally send your changes back to the owner as\n")
	b.WriteString("a suggestion — but that needs a decision per skill, which can't be\n")
	b.WriteString("prompted here.\n\n")

	for _, p := range prompts {
		if p.source == nil {
			continue
		}
		fmt.Fprintf(&b, "  • %s  (from %s/%s)\n", p.name, p.source.Owner, p.source.Slug)
	}

	b.WriteString("\nCollaborate with the user to decide, per skill, whether to send a\n")
	b.WriteString("suggestion upstream. When you're ready, re-run 'airskills push' in an\n")
	b.WriteString("interactive terminal and answer 's' at the prompt (with an optional\n")
	b.WriteString("message). Doing nothing leaves these as pending — they'll be offered\n")
	b.WriteString("again on the next interactive push.\n\n")

	return b.String()
}

// pushConflictResolutionInstructions returns the per-conflict block shown
// under the "--- Conflicts ---" header after a push reports a remote-content
// change. TTY readers are told how to brief their agent; the non-TTY variant
// addresses the agent directly since it's the one reading the output.
func pushConflictResolutionInstructions(c conflictInfo, isAgent bool) string {
	var b strings.Builder
	if isAgent {
		b.WriteString("\n  You are an agent. Merge the remote version with the local version,\n")
		fmt.Fprintf(&b, "  keeping local changes where they conflict:\n")
		fmt.Fprintf(&b, "    Remote: %s\n", c.remotePath)
		fmt.Fprintf(&b, "    Local:  %s\n", c.localPath)
		b.WriteString("  Show the user the diff before saving, and don't overwrite the\n")
		b.WriteString("  local file until they've agreed.\n")
	} else {
		b.WriteString("\n  To resolve, tell your AI agent:\n")
		fmt.Fprintf(&b, "  \"Merge %s (remote) with %s (my version),\n", c.remotePath, c.localPath)
		b.WriteString("   keeping my local changes where possible. Show me the diff before saving.\"\n")
	}
	return b.String()
}

// pullDivergenceFooter returns the footer shown below the diverged-skills
// list. When hasSourced is true it also explains the fork-specific options
// (replace/merge/keep). The agent variant addresses the agent directly and
// drops the "your agent can" framing that makes no sense when the agent is
// the reader.
func pullDivergenceFooter(hasSourced bool, isAgent bool) string {
	var b strings.Builder
	if isAgent {
		b.WriteString("\nYou are an agent. Merge the files shown above with the user, then\n")
		b.WriteString("run 'airskills push --force' to resolve. Don't overwrite local\n")
		b.WriteString("content until the user has reviewed the diff.\n")
		if hasSourced {
			b.WriteString("\nFor skills originally from another user, walk the user through\n")
			b.WriteString("these options and let them choose:\n")
			b.WriteString("  a) Replace their version with the owner's (accept the update)\n")
			b.WriteString("  b) Merge the owner's changes into their version\n")
			b.WriteString("  c) Keep their version as-is (skip)\n")
		}
	} else {
		b.WriteString("\nMerge the files, then run 'airskills push --force' to resolve.\n")
		if hasSourced {
			b.WriteString("\nFor skills originally from another user, your agent can:\n")
			b.WriteString("  a) Replace your version with the owner's (accept their update)\n")
			b.WriteString("  b) Merge the owner's changes into your version\n")
			b.WriteString("  c) Keep your version as-is (skip)\n")
		}
	}
	return b.String()
}

// reviewGuideText returns the step-by-step review workflow printed after the
// list of pending suggestions. When isAgent is true the opening paragraph
// addresses the agent directly and the steps are phrased as imperatives the
// agent should execute in collaboration with the user. When false it reads
// as guidance to a human operator.
func reviewGuideText(isAgent bool) string {
	var intro string
	if isAgent {
		intro = `=== How to review and merge suggestions ===

You are an agent. The user has asked you to drive the review of the
pending suggestions above. Walk them through it together — don't
auto-accept anything, and don't merge without showing the user the
diff first.

You can batch multiple suggestions into a single push — that's the
intended workflow. Read all pending suggestions, discuss what to keep
with the user, merge into the local skill, push once, then accept or
decline each individually.

For each suggestion:

  1. Download the suggested version:
       airskills review download <suggestion-id>
     Prints a tmp path containing the suggester's files.

  2. Read both the suggested files and the user's current skill files.
     The suggestion was built against a specific version hash of the
     owner's skill — shown above. The current version may have moved on.

  3. Show the user the diff and decide together what to incorporate.
     Merge the chosen changes into the local skill directory — or
     replace entirely, or leave as-is. Nothing auto-merges; you stay
     in control of versioning and the changelog.

  4. Once the user has agreed on everything to merge from all
     suggestions, push:
       airskills push

  5. Mark each suggestion resolved:
       airskills review accept <suggestion-id>
       airskills review decline <suggestion-id> --message "why"

`
	} else {
		intro = `=== How to review and merge suggestions ===

You can batch multiple suggestions into a single push — that's the
intended workflow. Read all pending suggestions, merge what you want
from each, push once, then accept/decline each individually.

For each suggestion:

  1. Download the suggested version:
       airskills review download <suggestion-id>
     Prints a tmp path containing the suggester's files.

  2. Read both the suggested files and your current skill files.
     The suggestion was built against a specific version hash of your
     skill — shown above. Your current version may have moved on.

  3. Decide what to incorporate. Merge desired changes into your
     local skill directory — or replace entirely, or leave as-is.
     Nothing auto-merges; you stay in control of versioning and the
     changelog.

  4. Once you've merged everything you want from all suggestions,
     push your changes:
       airskills push

  5. Mark each suggestion resolved:
       airskills review accept <suggestion-id>
       airskills review decline <suggestion-id> --message "why"

`
	}
	return intro
}
