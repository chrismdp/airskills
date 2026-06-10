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

// conflictEntry describes one skill in a conflict — used by conflictResolutionMessage.
type conflictEntry struct {
	name      string
	localDir  string
	remoteDir string
	source    *skillSource
	// kind discriminates the cause of the conflict so the headline
	// wording can be accurate. "" / "tracked" — the skill was tracked
	// and has diverged on both sides since the last sync. "untracked"
	// — the local skill exists but airskills has never tracked it,
	// and the server happens to have a same-named skill with different
	// bytes; the "diverged on both sides" framing doesn't apply.
	kind string
	// orgSlug is set when the conflicting server skill belongs to an org.
	// Drives a warning that after --keep-local, push proposes edits as a
	// suggestion rather than updating the org skill in place — admins
	// accept their own suggestion via review (no role in the API yet, so
	// the warning fires for any org skill). Empty for personal skills.
	orgSlug string
}

// conflictResolutionMessage renders the canonical conflict-resolution
// instructions used by both push and pull conflict paths. entries is the list
// of skills currently in conflict; isAgent switches the opening guidance
// between human-at-terminal and agent-as-reader wording.
func conflictResolutionMessage(entries []conflictEntry, isAgent bool) string {
	var b strings.Builder
	for _, e := range entries {
		if e.kind == "untracked" {
			fmt.Fprintf(&b, "\nConflict: %q exists both locally and in your skillset, but airskills hasn't tracked your local copy — and your bytes differ from the server's.\n", e.name)
			fmt.Fprintf(&b, "  Local:  %s\n", e.localDir)
			fmt.Fprintf(&b, "  Remote: %s  (review copy; refreshed only when the server changes)\n", e.remoteDir)
			if isAgent {
				fmt.Fprintf(&b, "\nYou are an agent. Read both directories, decide with the user, then run one of:\n")
			} else {
				fmt.Fprintf(&b, "\nAsk your agent to read both directories, decide with you, then run one of:\n")
			}
			if e.orgSlug != "" {
				fmt.Fprintf(&b, "\n  ⚠ %q is an org skill (%s/%s). After 'pull --keep-local', pushing your edits backs\n", e.name, e.orgSlug, e.name)
				b.WriteString("    them up to your own account and proposes them to the org as a suggestion — it does\n")
				b.WriteString("    not update the org skill directly. If you administer this org skill, accept your\n")
				b.WriteString("    own suggestion with 'airskills review'.\n")
			}
			b.WriteString("\n  Keep your local copy, track it against the server version (stops this warning):\n")
			fmt.Fprintf(&b, "      airskills pull --keep-local %s\n", e.name)
			b.WriteString("\n  Take the server version, overwrite your local (backed up first):\n")
			fmt.Fprintf(&b, "      airskills pull --force %s\n", e.name)
			b.WriteString("\n  Keep both — rename YOUR local copy aside (never the server skill), then pull:\n")
			fmt.Fprintf(&b, "      airskills mv %s <new-local-name>\n", e.name)
			b.WriteString("      airskills pull\n")
			b.WriteString("\nDo not edit ~/.config/airskills/sync.json directly. Move content with CLI commands and normal file edits only.\n")
			continue
		} else {
			fmt.Fprintf(&b, "\nConflict: %s has changed both locally and on the server.\n", e.name)
		}
		fmt.Fprintf(&b, "  Local:  %s\n", e.localDir)
		fmt.Fprintf(&b, "  Remote: %s\n", e.remoteDir)
		if isAgent {
			fmt.Fprintf(&b, "\nYou are an agent. Read %s and %s and decide with the\nuser how to resolve. Then run one of:\n\n", e.localDir, e.remoteDir)
			fmt.Fprintf(&b, "  Merge — combine the changes to keep into %s, then:\n", e.localDir)
		} else {
			fmt.Fprintf(&b, "\nAsk your agent to read %s and %s and decide with you\nhow to resolve. The agent should then run one of:\n\n", e.localDir, e.remoteDir)
			fmt.Fprintf(&b, "  Merge — combine the changes you want to keep into %s, then:\n", e.localDir)
		}
		b.WriteString("    airskills sync\n")
		b.WriteString("  (Sync will silently auto-resolve if your merged copy ends up with the\n")
		b.WriteString("   same content hash as the remote.)\n\n")
		b.WriteString("  Keep local, discard remote:\n")
		fmt.Fprintf(&b, "    airskills push --force %s\n\n", e.name)
		b.WriteString("  Take remote, discard local:\n")
		fmt.Fprintf(&b, "    airskills pull --force %s\n", e.name)
		b.WriteString("\nRecovery:\n")
		b.WriteString("  After push --force: previous remote kept in server-side version history\n")
		fmt.Fprintf(&b, "                      → airskills pull --version <prev-commit> %s\n", e.name)
		fmt.Fprintf(&b, "                      → list commits with: airskills log %s\n", e.name)
		b.WriteString("  After pull --force: previous local saved to ~/.airskills/undo/<ts>/<name>/<agent>/\n")
		b.WriteString("                      → cp -r that back if needed (one subdir per agent)\n")
		b.WriteString("\nNEVER edit airskills metadata (~/.config/airskills/sync.json) directly — the\n")
		b.WriteString("CLI owns state. You can edit content files freely.\n")
		if e.source != nil {
			fmt.Fprintf(&b, "\n(This skill is sourced from %s/%s. Fork-aware behaviour — selective incorporation,\n suggestions back to upstream — is being designed in a separate spec. Today, push --force\n pushes to your namespace, not upstream.)\n", e.source.Owner, e.source.Slug)
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
