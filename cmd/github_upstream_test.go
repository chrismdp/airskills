package cmd

import (
	"os"
	"path/filepath"
	"testing"
)

// GitHub-sourced skills must behave like any other non-owned upstream: an
// upstream that moves NEVER overwrites local edits. Before this,
// syncGitHubSkills compared upstream's hash against the recorded baseline
// only, and os.WriteFile'd the new bytes straight over the user's edited
// files — no backup, no prompt, no way back. The airskills-native path for
// the same situation keeps local and offers `add --force` / `resolve`.

func githubMarker(baseline string) *SyncEntry {
	return &SyncEntry{
		Version: "github",
		Tool:    "claude-code",
		Source: &skillSource{
			Owner:       "AminBlg",
			Slug:        "simple-english",
			ContentHash: baseline,
			GitHubURL:   "https://github.com/AminBlg/SimpleEnglish",
			GitHubSkill: "simple-english",
		},
	}
}

func TestDecideGitHubUpdate(t *testing.T) {
	resolved := githubMarker("installed")
	resolved.ResolvedHash = "upstream-v2"

	resolvedThenMoved := githubMarker("installed")
	resolvedThenMoved.ResolvedHash = "upstream-v2"

	cases := []struct {
		name         string
		marker       *SyncEntry
		upstreamHash string
		localHash    string
		want         githubUpdateAction
	}{
		{
			name:         "upstream unchanged is a no-op",
			marker:       githubMarker("installed"),
			upstreamHash: "installed",
			localHash:    "installed",
			want:         gitHubUpdateNone,
		},
		{
			name:         "upstream moved and local is untouched takes upstream",
			marker:       githubMarker("installed"),
			upstreamHash: "upstream-v2",
			localHash:    "installed",
			want:         gitHubUpdateTake,
		},
		{
			name:         "upstream moved and local was edited keeps local",
			marker:       githubMarker("installed"),
			upstreamHash: "upstream-v2",
			localHash:    "my-edits",
			want:         gitHubUpdateKeepLocal,
		},
		{
			name:         "already resolved against this upstream stays quiet",
			marker:       resolved,
			upstreamHash: "upstream-v2",
			localHash:    "my-edits",
			want:         gitHubUpdateNone,
		},
		{
			name:         "resolved, then upstream moved again warns again",
			marker:       resolvedThenMoved,
			upstreamHash: "upstream-v3",
			localHash:    "my-edits",
			want:         gitHubUpdateKeepLocal,
		},
		{
			name:         "unknown local hash never clobbers",
			marker:       githubMarker("installed"),
			upstreamHash: "upstream-v2",
			localHash:    "",
			want:         gitHubUpdateKeepLocal,
		},
		{
			name:         "unknown upstream hash is a no-op",
			marker:       githubMarker("installed"),
			upstreamHash: "",
			localHash:    "installed",
			want:         gitHubUpdateNone,
		},
		{
			name:         "owned skill (no source) is a no-op",
			marker:       &SyncEntry{Version: "1.0.0"},
			upstreamHash: "upstream-v2",
			localHash:    "whatever",
			want:         gitHubUpdateNone,
		},
		{
			name:         "airskills-sourced skill is not the GitHub path",
			marker:       &SyncEntry{Source: &skillSource{Owner: "chrismdp", Slug: "retro", ID: "abc"}},
			upstreamHash: "upstream-v2",
			localHash:    "whatever",
			want:         gitHubUpdateNone,
		},
		{
			name:         "nil marker is a no-op",
			marker:       nil,
			upstreamHash: "upstream-v2",
			localHash:    "whatever",
			want:         gitHubUpdateNone,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := decideGitHubUpdate(tc.marker, tc.upstreamHash, tc.localHash)
			if got != tc.want {
				t.Errorf("decideGitHubUpdate = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestGitHubUpstreamHash(t *testing.T) {
	allFiles := map[string][]byte{
		"skills/simple-english/SKILL.md":                []byte("---\nname: simple-english\n---\nbody"),
		"skills/simple-english/references/checklist.md": []byte("checklist"),
		"skills/other/SKILL.md":                         []byte("---\nname: other\n---"),
		"README.md":                                     []byte("# repo"),
	}

	got := gitHubUpstreamHash(allFiles, "simple-english")
	want := sha256Hex([]byte("---\nname: simple-english\n---\nbody"))
	if got != want {
		t.Errorf("hash = %q, want the sha256 of that skill's SKILL.md (%q)", got, want)
	}

	if h := gitHubUpstreamHash(allFiles, "no-such-skill"); h != "" {
		t.Errorf("missing skill should hash to empty, got %q", h)
	}
}

// Markers written before the two-baseline split carry only ContentHash —
// the hash of the bytes that landed on disk, which install may have
// rewritten (SKILL.md's name field is forced to match the directory).
// Reading that as the upstream baseline makes an untouched repo look like
// it moved. Backfill repairs those markers in place, with no user action
// and no message, whenever we can prove upstream has NOT moved since the
// install.
func TestBackfillGitHubUpstreamHash(t *testing.T) {
	// A repo whose SKILL.md declares a name that does NOT match the local
	// directory, so install rewrites it and the disk bytes differ.
	repoBytes := []byte("---\nname: SimpleEnglish\n---\nbody")
	rawHash := sha256Hex(repoBytes)
	fixed, changed := fixSkillNameInContent("simple-english", repoBytes)
	if !changed {
		t.Fatal("fixture must trigger the name rewrite, otherwise it tests nothing")
	}
	fixedHash := sha256Hex(fixed)

	cases := []struct {
		name       string
		marker     *SyncEntry
		wantFilled string
	}{
		{
			name: "already migrated markers are left alone",
			marker: func() *SyncEntry {
				m := githubMarker(fixedHash)
				m.Source.UpstreamContentHash = "already-set"
				return m
			}(),
			wantFilled: "already-set",
		},
		{
			name:       "install did not rewrite: baseline is the repo hash",
			marker:     githubMarker(rawHash),
			wantFilled: rawHash,
		},
		{
			name:       "install rewrote the name: baseline is still the repo hash",
			marker:     githubMarker(fixedHash),
			wantFilled: rawHash,
		},
		{
			name:       "upstream genuinely moved since install: do not guess",
			marker:     githubMarker(sha256Hex([]byte("something else entirely"))),
			wantFilled: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			backfillGitHubUpstreamHash(tc.marker, "simple-english", repoBytes)
			if got := tc.marker.Source.UpstreamContentHash; got != tc.wantFilled {
				t.Errorf("UpstreamContentHash = %q, want %q", got, tc.wantFilled)
			}
		})
	}
}

// --- behaviour at the boundary: what lands on disk ---

// installedSkill writes a skill into the fake HOME as though airskills had
// installed it, and returns the primary SKILL.md path.
func installedSkill(t *testing.T, home, name, body string) string {
	t.Helper()
	dir := filepath.Join(home, ".claude", "skills", name)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	p := filepath.Join(dir, "SKILL.md")
	if err := os.WriteFile(p, []byte(body), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	return p
}

// stubTarball points the GitHub fetch at fixed bytes for the duration of a test.
func stubTarball(t *testing.T, files map[string][]byte) {
	t.Helper()
	prev := downloadGitHubTarballFn
	downloadGitHubTarballFn = func(owner, repo string) (map[string][]byte, error) {
		return files, nil
	}
	t.Cleanup(func() { downloadGitHubTarballFn = prev })
}

func writeSyncState(t *testing.T, home string, state *SyncState) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(home, ".config", "airskills"), 0755); err != nil {
		t.Fatalf("mkdir config: %v", err)
	}
	if err := saveSyncState(state); err != nil {
		t.Fatalf("save sync state: %v", err)
	}
}

func TestSyncGitHubSkillsKeepsLocalEdits(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	original := "---\nname: simple-english\n---\nupstream v1"
	edited := "---\nname: simple-english\n---\nMY LOCAL EDITS"
	skillPath := installedSkill(t, home, "simple-english", edited)

	writeSyncState(t, home, &SyncState{Version: 1, Skills: map[string]*SyncEntry{
		"simple-english": githubMarker(sha256Hex([]byte(original))),
	}})

	stubTarball(t, map[string][]byte{
		"skills/simple-english/SKILL.md": []byte("---\nname: simple-english\n---\nupstream v2"),
	})

	syncGitHubSkills()

	got, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != edited {
		t.Errorf("local edits were overwritten by upstream.\n got: %q\nwant: %q", got, edited)
	}

	// The marker must NOT absorb the new upstream hash — otherwise the next
	// sync thinks the user has seen it and goes quiet with edits unreviewed.
	after := loadSyncState().Skills["simple-english"]
	if after.Source.ContentHash != sha256Hex([]byte(original)) {
		t.Errorf("baseline moved to %q; it must stay at the last-installed bytes until the user resolves",
			after.Source.ContentHash)
	}
}

func TestSyncGitHubSkillsTakesUpstreamWhenLocalIsClean(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	original := "---\nname: simple-english\n---\nupstream v1"
	upstream := "---\nname: simple-english\n---\nupstream v2"
	skillPath := installedSkill(t, home, "simple-english", original)

	writeSyncState(t, home, &SyncState{Version: 1, Skills: map[string]*SyncEntry{
		"simple-english": githubMarker(sha256Hex([]byte(original))),
	}})

	stubTarball(t, map[string][]byte{
		"skills/simple-english/SKILL.md": []byte(upstream),
	})

	syncGitHubSkills()

	got, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != upstream {
		t.Errorf("clean local should have taken upstream.\n got: %q\nwant: %q", got, upstream)
	}

	after := loadSyncState().Skills["simple-english"]
	newHash := sha256Hex([]byte(upstream))
	if after.Source.ContentHash != newHash {
		t.Errorf("baseline = %q, want the new upstream hash %q", after.Source.ContentHash, newHash)
	}
	if after.ResolvedHash != newHash {
		t.Errorf("ResolvedHash = %q, want %q — taking upstream acknowledges it", after.ResolvedHash, newHash)
	}
}

func TestSyncGitHubSkillsStaysQuietAfterResolve(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	original := "---\nname: simple-english\n---\nupstream v1"
	upstream := "---\nname: simple-english\n---\nupstream v2"
	edited := "---\nname: simple-english\n---\nMY LOCAL EDITS"
	skillPath := installedSkill(t, home, "simple-english", edited)

	marker := githubMarker(sha256Hex([]byte(original)))
	marker.ResolvedHash = sha256Hex([]byte(upstream))
	writeSyncState(t, home, &SyncState{Version: 1, Skills: map[string]*SyncEntry{
		"simple-english": marker,
	}})

	stubTarball(t, map[string][]byte{
		"skills/simple-english/SKILL.md": []byte(upstream),
	})

	syncGitHubSkills()

	got, _ := os.ReadFile(skillPath)
	if string(got) != edited {
		t.Errorf("resolved skill must keep local bytes, got %q", got)
	}
}

// The migration in place: a pre-split marker whose install rewrote the
// name field must not read as a moved upstream. Before backfill this
// took upstream on the very first sync — a pointless rewrite, and a
// pointless warning for anyone holding local edits.
func TestSyncGitHubSkillsMigratesPreSplitMarkerSilently(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	repoBytes := []byte("---\nname: SimpleEnglish\n---\nbody")
	onDisk, _ := fixSkillNameInContent("simple-english", repoBytes)
	installedSkill(t, home, "simple-english", string(onDisk))

	// Pre-split marker: one hash, and it is the DISK hash.
	writeSyncState(t, home, &SyncState{Version: 1, Skills: map[string]*SyncEntry{
		"simple-english": githubMarker(sha256Hex(onDisk)),
	}})

	stubTarball(t, map[string][]byte{"skills/simple-english/SKILL.md": repoBytes})

	syncGitHubSkills()

	after := loadSyncState().Skills["simple-english"]
	if after.Source.UpstreamContentHash != sha256Hex(repoBytes) {
		t.Errorf("UpstreamContentHash = %q, want the repo hash %q — marker was not migrated",
			after.Source.UpstreamContentHash, sha256Hex(repoBytes))
	}
	if after.Source.ContentHash != sha256Hex(onDisk) {
		t.Errorf("ContentHash = %q, want the disk hash — the local baseline must not move",
			after.Source.ContentHash)
	}
}

// "Take theirs" is `airskills add <url> --force`, and the add docs promise
// the local copy is backed up to ~/.airskills/undo/<timestamp>/ first. That
// promise has to hold on the GitHub path too — it is the only exit that
// discards local edits, so it is the one that must be undoable.
func TestGitHubInstallBacksUpAnExistingLocalCopy(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	edited := "---\nname: simple-english\n---\nMY LOCAL EDITS"
	skillPath := installedSkill(t, home, "simple-english", edited)

	writeSyncState(t, home, &SyncState{Version: 1, Skills: map[string]*SyncEntry{
		"simple-english": githubMarker(sha256Hex([]byte("installed"))),
	}})

	upstream := "---\nname: simple-english\n---\nupstream v2"
	err := installOneGitHubSkill("AminBlg", "SimpleEnglish", "https://github.com/AminBlg/SimpleEnglish", foundSkill{
		FullPath: "skills/simple-english",
		LeafName: "simple-english",
		Files:    map[string][]byte{"SKILL.md": []byte(upstream)},
	})
	if err != nil {
		t.Fatalf("install: %v", err)
	}

	got, _ := os.ReadFile(skillPath)
	if string(got) != upstream {
		t.Errorf("install should have taken upstream bytes, got %q", got)
	}

	matches, _ := filepath.Glob(filepath.Join(home, ".airskills", "undo", "*", "simple-english", "*", "SKILL.md"))
	if len(matches) == 0 {
		t.Fatal("no backup under ~/.airskills/undo — the overwritten edits are unrecoverable")
	}
	backed, _ := os.ReadFile(matches[0])
	if string(backed) != edited {
		t.Errorf("backup holds %q, want the pre-install local bytes %q", backed, edited)
	}
}

// `airskills resolve` is the "keep mine" exit. It must work for a
// GitHub-sourced skill, which has no upstream skill id on the server —
// the hash comes from the repo tarball instead.
func TestResolveUsesGitHubHashWhenThereIsNoUpstreamID(t *testing.T) {
	upstream := "---\nname: simple-english\n---\nupstream v2"
	stubTarball(t, map[string][]byte{
		"skills/simple-english/SKILL.md": []byte(upstream),
	})

	got, err := fetchUpstreamHash(githubMarker("installed"))
	if err != nil {
		t.Fatalf("resolve could not reach the GitHub upstream: %v", err)
	}
	if want := sha256Hex([]byte(upstream)); got != want {
		t.Errorf("hash = %q, want %q", got, want)
	}
}
