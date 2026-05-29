package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/chrismdp/airskills/config"
	"github.com/spf13/cobra"
)

// buildKeepLocalEntry produces the marker that adopts a conflicting
// skillset skill as the upstream of the user's LOCAL copy, keeping the
// local bytes on disk.
//
// ContentHash is set to the server's CURRENT hash (the sync baseline), not
// the local hash. That's what silences the conflict: decidePullActions
// skips when remoteHash == marker.ContentHash, so pull stays quiet while
// the local dir keeps its own differing bytes. Status will still show the
// skill as "to push" — correct, since you do have local changes the server
// doesn't.
//
// For sourced skills (org / another user — Source != nil) ResolvedHash
// acknowledges the current upstream, mirroring `airskills resolve`, so the
// modified-pending prompt stays quiet until upstream actually moves.
//
// No server write happens here — keep-local never forks or suggests. If the
// user wants their version on the server, that's a later, deliberate
// `airskills push` (which forks-then-suggests for sourced skills).
func buildKeepLocalEntry(skill apiSkill, ownerKind, ownerSlug string, src *skillSource) *SyncEntry {
	remoteHash := strDeref(skill.ContentHash)
	e := &SyncEntry{
		SkillID:     skill.Id.String(),
		Version:     skill.Version,
		ContentHash: remoteHash,
		Tool:        "claude-code",
		OwnerKind:   ownerKind,
		OwnerSlug:   ownerSlug,
		Source:      src,
	}
	if src != nil {
		e.ResolvedHash = remoteHash
	}
	return e
}

// runPullKeepLocal implements `airskills pull --keep-local <name>...`.
// For each named skill currently in conflict, it keeps the local files
// as-is and writes a marker adopting the server copy as upstream, so the
// conflict stops recurring. The opposite of `pull --force` (take remote).
func runPullKeepLocal(cmd *cobra.Command, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("pull --keep-local requires at least one skill name: airskills pull --keep-local <name>")
	}

	client, err := newAPIClientAuto()
	if err != nil {
		return fmt.Errorf("pull --keep-local requires authentication: %w", err)
	}

	syncState := loadSyncState()
	localSkills, err := scanSkillsFromAgents()
	if err != nil {
		return err
	}

	cfg, cfgErr := config.Load()
	if cfgErr != nil {
		return cfgErr
	}
	sendSlug, err := resolveSkillsetFlag(cfg, skillsetFlag, stdinReader(), stderrWriter())
	if err != nil {
		return err
	}
	remoteSkills, resolvedSlug, err := client.listPersonalSkillsInSkillset(sendSlug)
	if err != nil {
		return fmt.Errorf("fetching skills: %w", err)
	}
	rememberSkillsetAfterSuccess(cfg, resolvedSlug)

	toPull, _, _ := decidePullActions(remoteSkills, localSkills, syncState)
	conflicts := map[string]pullEntry{}
	for _, p := range toPull {
		if p.reason == "diverged" || p.reason == "untracked-conflict" {
			conflicts[p.skill.Name] = p
		}
	}

	owners := newOwnerResolver(client)
	kept := 0
	for _, name := range args {
		p, ok := conflicts[name]
		if !ok {
			return fmt.Errorf("%s: not in conflict; nothing to keep-local. Use 'airskills sync' for normal updates.", name)
		}
		dirName := name
		if p.localDir != "" {
			dirName = filepath.Base(p.localDir)
		}
		ownerKind, ownerSlug := owners.resolve(&p.skill)
		src := owners.sourceFor(&p.skill)
		// Org-skill caveat: keep-local marks this as sourced, so the next
		// push FORKS it into your personal namespace rather than updating
		// the org skill. That's correct for a member, wrong for an admin
		// who owns the org skill. We can't yet tell admins apart (no role
		// in the API), so warn whenever it's an org skill.
		if ownerKind == "org" {
			org := ownerSlug
			if org == "" {
				org = "the org"
			}
			fmt.Printf("  %s %q is an org skill (%s). Your next push will FORK it into your personal\n", yellow("⚠"), name, org)
			fmt.Printf("    namespace, not update the org skill. If you administer it, run 'airskills pull --force %s' instead.\n", name)
		}
		entry := buildKeepLocalEntry(p.skill, ownerKind, ownerSlug, src)
		// Preserve identity/suggestion fields when the skill was already
		// tracked (the diverged path) — buildKeepLocalEntry returns a fresh
		// marker, which would otherwise wipe a LocalAlias from `add --as` or
		// a pending suggestion. Untracked conflicts have no prior marker.
		if prev := syncState.Skills[dirName]; prev != nil {
			entry.LocalAlias = prev.LocalAlias
			entry.SuggestionID = prev.SuggestionID
			entry.SuggestDeclined = prev.SuggestDeclined
		}
		syncState.Skills[dirName] = entry
		// Drop the parked review copy — it's been resolved.
		_, _ = removePendingConflictDirs(name)
		kept++
		fmt.Printf("  %s %s kept local; now tracked against the server copy\n", green("✓"), name)
	}

	if err := saveSyncState(syncState); err != nil {
		return fmt.Errorf("saving sync state: %w", err)
	}

	if kept > 0 {
		fmt.Printf("\n%d kept local — your files are unchanged and the conflict won't recur.\n", kept)
		printAgentNextSteps(os.Stdout, []agentNextStep{
			{Cmd: "airskills push", Why: "publish your local version to the server (forks for sourced/org skills)"},
			{Cmd: "airskills status", Why: "confirm the conflict is cleared"},
		})
	}
	return nil
}
