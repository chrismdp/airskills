package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/chrismdp/airskills/telemetry"
	"github.com/spf13/cobra"
)

var forkAsName string

var forkCmd = &cobra.Command{
	Use:   "fork <owner-or-org>/<slug>",
	Short: "Create your own independent copy of a skill",
	Long: `Creates a visible, first-class personal copy of someone else's skill.

This is the deliberate alternative to the transparent overlay you get by
just editing a non-owned skill: a fork is an independent skill in YOUR
namespace with its own name, tracked as itself. It records lineage
(forked_from) so upstream awareness still works, but it is never
auto-retired, never hidden, and push goes to your fork — no suggest loop.

--as sets the new skill's SERVER name (and slug) — required when the
upstream's slug already exists in your effective skill set (the fork must
not shadow the original; explicit is the point).

  airskills fork chrismdp/retro                # fork under the same slug
  airskills fork parsons-home/home --as my-home  # fork under a new name`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		input := strings.TrimPrefix(strings.TrimPrefix(args[0], "https://"), "http://")
		parts := strings.SplitN(input, "/", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return fmt.Errorf("expected format: <owner-or-org>/<skill-slug>")
		}
		owner, slug := parts[0], parts[1]

		client, err := newAPIClientAuto()
		if err != nil {
			return fmt.Errorf("fork requires login: %w", err)
		}

		newName := slug
		if forkAsName != "" {
			if err := validateSkillName(forkAsName); err != nil {
				return fmt.Errorf("--as: %w", err)
			}
			newName = forkAsName
		}

		// Resolve the upstream.
		body, status, err := client.getWithStatus(fmt.Sprintf("/api/v1/resolve/%s/%s", owner, slug))
		if err != nil {
			return fmt.Errorf("resolving %s/%s: %w", owner, slug, err)
		}
		if status == 404 {
			return fmt.Errorf("skill %s/%s not found (is it public, or shared with you?)", owner, slug)
		}
		if status == 410 {
			return fmt.Errorf("skill %s/%s was transferred or deleted — resolve its new location first ('airskills add %s/%s' reports it)", owner, slug, owner, slug)
		}
		if status != 200 {
			return fmt.Errorf("server returned %d resolving %s/%s", status, owner, slug)
		}
		var upstream struct {
			ID          string `json:"id"`
			Slug        string `json:"slug"`
			Version     string `json:"version"`
			Content     string `json:"content"`
			ContentHash string `json:"content_hash"`
		}
		if err := parseJSON(body, &upstream); err != nil {
			return fmt.Errorf("parsing resolve response: %w", err)
		}

		// Download the upstream's files and rewrite the SKILL.md name to the
		// new slug (the agentskills.io spec requires name == dir name, and
		// the server validates name == slug on push). downloadSkillFiles
		// goes through the authenticated client (token refresh handled);
		// fall back to the resolve body for archive-less skills.
		files, derr := downloadSkillFiles(client, upstream.ID)
		if (derr != nil || len(files) == 0) && upstream.Content != "" {
			files = map[string][]byte{"SKILL.md": []byte(upstream.Content)}
		}
		if len(files) == 0 {
			return fmt.Errorf("downloading %s/%s: %v", owner, slug, derr)
		}
		if skillMd, ok := files["SKILL.md"]; ok {
			if fixed, changed := fixSkillNameInContent(newName, skillMd); changed {
				files["SKILL.md"] = fixed
			}
		}

		// Create the fork: an independent VISIBLE skill (backup=false) with
		// forked_from lineage. A same-slug 409 means the slug is taken in
		// the caller's effective set — shadowing the original requires the
		// overlay (just edit it), not a fork; pick a new name instead.
		fork, err := client.createSkill(newName, "", []string{"claude-code"}, upstream.ID, "")
		if err != nil {
			var sc *SkillConflictError
			if errors.As(err, &sc) {
				return fmt.Errorf("slug %q already exists in your skill set — pick a different name with: airskills fork %s/%s --as <new-name>", slugify(newName), owner, slug)
			}
			return fmt.Errorf("creating fork: %w", err)
		}

		// Local install + upload the (name-rewritten) bytes so server and
		// disk agree from the start.
		installed, err := installSkillToAgents(newName, files)
		if err != nil {
			return fmt.Errorf("fork created server-side, but local install failed: %w", err)
		}
		contentHash := computeMerkleHash(files)
		if home, herr := os.UserHomeDir(); herr == nil {
			primaryDir := filepath.Join(home, ".claude", "skills", newName)
			if archive, aerr := createTarGz(primaryDir); aerr == nil {
				if updated, _, perr := client.putArchive(fork.Id.String(), archive, "", contentHash); perr == nil && updated != nil {
					fork.Version = updated.Version
				}
			}
		}

		// The marker tracks the NEW skill as identity. Source records the
		// upstream for awareness (incoming notifications) — no Backup, and
		// no suggest loop by default.
		syncState := loadSyncState()
		entry := &SyncEntry{
			SkillID:     fork.Id.String(),
			Version:     fork.Version,
			ContentHash: contentHash,
			Tool:        "claude-code",
			OwnerKind:   "user",
			Source: &skillSource{
				Owner:               owner,
				Slug:                slug,
				ID:                  upstream.ID,
				ContentHash:         upstream.ContentHash,
				UpstreamSkillID:     upstream.ID,
				UpstreamContentHash: upstream.ContentHash,
				UpstreamVersion:     upstream.Version,
			},
		}
		if profile, perr := client.getMe(); perr == nil && profile != nil {
			entry.OwnerSlug = profile.Username
		}
		seedCopyLedgerFromDisk(entry, newName)
		syncState.Skills[newName] = entry
		if err := saveSyncState(syncState); err != nil {
			return err
		}

		fmt.Printf("\n  %s forked %s/%s → %s (%d agents)\n", green("✓"), owner, slug, newName, len(installed))
		fmt.Printf("  Your fork is an independent skill: edits push to it directly.\n")

		telemetry.Capture("cli_fork", map[string]interface{}{
			"owner":    owner,
			"slug":     slug,
			"as":       forkAsName != "",
			"skill_id": fork.Id.String(),
		})

		printAgentNextSteps(cmd.OutOrStdout(), []agentNextStep{
			{Cmd: "airskills status", Why: "see the fork alongside the rest"},
			{Cmd: "airskills push", Why: "publish your changes to the fork after editing"},
		})
		return nil
	},
}

func init() {
	forkCmd.Flags().StringVar(&forkAsName, "as", "", "Name (and slug) for the new fork on the server — required when the upstream's slug is already taken in your skill set")
	rootCmd.AddCommand(forkCmd)
}
