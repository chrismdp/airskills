package cmd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chrismdp/airskills/config"
)

// agentForDir returns an agentDef whose GlobalDir resolves to dir after
// filepath.Join(home, GlobalDir). It computes the relative path from home to dir.
func agentForDir(t *testing.T, dir string) agentDef {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	rel, err := filepath.Rel(home, dir)
	if err != nil {
		t.Fatal(err)
	}
	return agentDef{Name: "test", GlobalDir: rel}
}

// TestExtractRefSlugsRejectsFalsePositives verifies that filesystem paths,
// URL path components, and built-in Claude Code slash commands are not
// reported as broken skill references. Caught in the wild on a real install
// where doctor surfaced 28 false positives across 21 skills (e.g. /tmp,
// /dev, /clear, /rename, /v3, /v4, /broadcasts, /webinar).
func TestExtractRefSlugsRejectsFalsePositives(t *testing.T) {
	cases := []struct {
		name string
		text string
	}{
		{"filesystem paths", "Write to /tmp/scratch.txt and read /dev/null. Path /var/log/x.log."},
		{"placeholder /path", "Replace /path/to/file with the real path."},
		{"built-in CC commands", "Look for /clear or /rename noise; ignore /init and /help."},
		{"trailing slash on URLs", "Visit /webinar/ and /assets/img/foo.jpg."},
		{"URL path with another segment", `Hit "https://api.kit.com/v4/broadcasts" with curl.`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			slugs := extractRefSlugs(tc.text)
			if len(slugs) > 0 {
				t.Errorf("expected no slugs from %q, got %v", tc.text, slugs)
			}
		})
	}
}

// TestExtractRefSlugsStripsFencedCodeBlocks verifies that ```...``` blocks
// are excluded from scanning. Caught in the wild: webinar-management/SKILL.md
// has a ```yaml block teaching Jekyll frontmatter that contains
// "redirect_from: /webinar" — that's a URL sample, not a skill reference,
// and shouldn't trigger a broken-ref report.
func TestExtractRefSlugsStripsFencedCodeBlocks(t *testing.T) {
	text := "Use /heartbeat normally.\n\n" +
		"```yaml\nredirect_from: /webinar\nredirect_from: /webinar/\n```\n\n" +
		"And then /retro.\n"
	slugs := extractRefSlugs(text)
	want := map[string]bool{"heartbeat": true, "retro": true}
	if len(slugs) != len(want) {
		t.Fatalf("expected %d slugs, got %d: %v", len(want), len(slugs), slugs)
	}
	for _, s := range slugs {
		if !want[s] {
			t.Errorf("unexpected slug: %q", s)
		}
	}
}

// TestExtractRefSlugsHandlesMultipleFencedBlocks verifies multiple code
// blocks in the same document are all stripped.
func TestExtractRefSlugsHandlesMultipleFencedBlocks(t *testing.T) {
	text := "Real ref: /first.\n\n" +
		"```bash\necho /not-a-ref\n```\n\n" +
		"Another real ref: /second.\n\n" +
		"```\n/also-not-a-ref\n```\n"
	slugs := extractRefSlugs(text)
	want := map[string]bool{"first": true, "second": true}
	if len(slugs) != len(want) {
		t.Fatalf("expected %d slugs, got %d: %v", len(want), len(slugs), slugs)
	}
	for _, s := range slugs {
		if !want[s] {
			t.Errorf("unexpected slug: %q", s)
		}
	}
}

// TestExtractRefSlugsKeepsRealRefsAlongsideFalsePositives verifies the
// tightened regex still catches legitimate references mixed with noise.
func TestExtractRefSlugsKeepsRealRefsAlongsideFalsePositives(t *testing.T) {
	text := `Write to /tmp/x then call /heartbeat. Use /clear to clear, then run /retro.`
	slugs := extractRefSlugs(text)
	want := map[string]bool{"heartbeat": true, "retro": true}
	if len(slugs) != len(want) {
		t.Fatalf("expected %d real refs, got %d: %v", len(want), len(slugs), slugs)
	}
	for _, s := range slugs {
		if !want[s] {
			t.Errorf("unexpected slug: %q", s)
		}
	}
}

// TestExtractRefSlugsBasic verifies that /slug tokens in SKILL.md body are extracted.
func TestExtractRefSlugsBasic(t *testing.T) {
	text := `---
name: my-skill
---
Use /deploy-check for deployments and /retro for retrospectives.
Also see /plan-skill.
`
	slugs := extractRefSlugs(text)
	want := map[string]bool{"deploy-check": true, "retro": true, "plan-skill": true}
	if len(slugs) != len(want) {
		t.Fatalf("expected %d slugs, got %d: %v", len(want), len(slugs), slugs)
	}
	for _, s := range slugs {
		if !want[s] {
			t.Errorf("unexpected slug: %q", s)
		}
	}
}

// TestExtractRefSlugsStripsURLs verifies that https:// paths are not extracted.
func TestExtractRefSlugsStripsURLs(t *testing.T) {
	text := "Load /good-skill. See https://example.com/path/to/thing for details."
	slugs := extractRefSlugs(text)
	if len(slugs) != 1 || slugs[0] != "good-skill" {
		t.Errorf("expected [good-skill], got %v", slugs)
	}
}

// TestExtractRefSlugsStripsFrontmatter verifies that /name in frontmatter is not matched.
func TestExtractRefSlugsStripsFrontmatter(t *testing.T) {
	text := `---
name: my-skill
description: use /other-skill
---
Body: /other-skill is great.
`
	slugs := extractRefSlugs(text)
	// /other-skill appears in both frontmatter and body; after stripping frontmatter
	// it should appear exactly once (deduped).
	if len(slugs) != 1 || slugs[0] != "other-skill" {
		t.Errorf("expected [other-skill], got %v", slugs)
	}
}

// TestExtractRefSlugsNoDuplicates verifies deduplication.
func TestExtractRefSlugsNoDuplicates(t *testing.T) {
	text := "Use /foo and then /foo again and /foo once more."
	slugs := extractRefSlugs(text)
	if len(slugs) != 1 || slugs[0] != "foo" {
		t.Errorf("expected [foo], got %v", slugs)
	}
}

// TestExtractRefSlugsNoFrontmatter verifies operation when there is no frontmatter.
func TestExtractRefSlugsNoFrontmatter(t *testing.T) {
	text := "Run /skill-a and /skill-b."
	slugs := extractRefSlugs(text)
	want := map[string]bool{"skill-a": true, "skill-b": true}
	if len(slugs) != 2 {
		t.Fatalf("expected 2 slugs, got %d: %v", len(slugs), slugs)
	}
	for _, s := range slugs {
		if !want[s] {
			t.Errorf("unexpected slug: %q", s)
		}
	}
}

// TestExtractRefSlugsSkipsFilesystemPaths verifies that paths like /tmp/foo,
// /dev/null, /etc/hosts are not mistaken for skill refs (slug followed by /).
func TestExtractRefSlugsSkipsFilesystemPaths(t *testing.T) {
	text := `Use /real-skill.
Save to /tmp/output.txt and /dev/null and /etc/hosts.
Other paths: /var/log/foo, /usr/local/bin.
`
	slugs := extractRefSlugs(text)
	if len(slugs) != 1 || slugs[0] != "real-skill" {
		t.Errorf("expected [real-skill], got %v", slugs)
	}
}

// TestExtractRefSlugsSkipsFencedCodeBlocks verifies that /slug references
// inside fenced code blocks are ignored — they're code examples, not refs.
func TestExtractRefSlugsSkipsFencedCodeBlocks(t *testing.T) {
	text := "Real ref: /good-skill.\n\n```bash\npython3 script.py /tmp/foo.mp3\nrm /var/cache/x\n```\n\nAnother: /also-good.\n"
	slugs := extractRefSlugs(text)
	want := map[string]bool{"good-skill": true, "also-good": true}
	if len(slugs) != 2 {
		t.Fatalf("expected 2 slugs, got %d: %v", len(slugs), slugs)
	}
	for _, s := range slugs {
		if !want[s] {
			t.Errorf("unexpected slug: %q", s)
		}
	}
}

// TestExtractRefSlugsSkipsInlineCode verifies that /slug references inside
// inline code spans (`...`) are ignored.
func TestExtractRefSlugsSkipsInlineCode(t *testing.T) {
	text := "See `/clear` and `/rename` are built-ins, not skills. But /real-skill is."
	slugs := extractRefSlugs(text)
	if len(slugs) != 1 || slugs[0] != "real-skill" {
		t.Errorf("expected [real-skill], got %v", slugs)
	}
}

// TestExtractRefSlugsSkipsMarkdownLinkURLs verifies that URL paths inside
// markdown link parens — [text](/path/) — are not mistaken for skill refs.
func TestExtractRefSlugsSkipsMarkdownLinkURLs(t *testing.T) {
	text := "See [my talk](/ralph-loops-aie-europe/) and use /real-skill afterwards."
	slugs := extractRefSlugs(text)
	if len(slugs) != 1 || slugs[0] != "real-skill" {
		t.Errorf("expected [real-skill], got %v", slugs)
	}
}

// TestExtractRefSlugsSkipsAPIVersionPaths verifies paths like /v3/, /v4/api
// (slug-followed-by-slash) are not mistaken for skill refs.
func TestExtractRefSlugsSkipsAPIVersionPaths(t *testing.T) {
	text := "Use /real-skill. The API is at https://api.example.com or relative paths /v3/users and /v4/broadcasts."
	slugs := extractRefSlugs(text)
	if len(slugs) != 1 || slugs[0] != "real-skill" {
		t.Errorf("expected [real-skill], got %v", slugs)
	}
}

// TestWalkBrokenRefsNoInstalled verifies that walkBrokenRefs returns nil when no
// skills are installed (nothing to scan).
func TestWalkBrokenRefsNoInstalled(t *testing.T) {
	// Override the agents list to point at an empty temp dir so the scanner
	// finds no skills without touching the real home dir.
	tmp := t.TempDir()
	skillsDir := filepath.Join(tmp, "skills")
	os.MkdirAll(skillsDir, 0755)

	origAgents := agents
	agents = []agentDef{agentForDir(t, skillsDir)}
	defer func() { agents = origAgents }()

	issues, err := walkBrokenRefs()
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 0 {
		t.Errorf("expected no issues for empty install, got %d", len(issues))
	}
}

// TestWalkBrokenRefsAllKnown verifies that refs satisfied by other local skills
// are not reported as broken.
func TestWalkBrokenRefsAllKnown(t *testing.T) {
	tmp := t.TempDir()
	skillsDir := filepath.Join(tmp, "skills")

	writeSkill := func(name, body string) {
		dir := filepath.Join(skillsDir, name)
		os.MkdirAll(dir, 0755)
		os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0644)
	}

	writeSkill("skill-a", "---\nname: skill-a\n---\nUse /skill-b.\n")
	writeSkill("skill-b", "---\nname: skill-b\n---\nNo refs here.\n")

	origAgents := agents
	agents = []agentDef{agentForDir(t, skillsDir)}
	defer func() { agents = origAgents }()

	issues, err := walkBrokenRefs()
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 0 {
		t.Errorf("expected no issues when refs are satisfied locally, got %d: %v", len(issues), issues)
	}
}

// TestWalkBrokenRefsDetectsUnknown verifies that a ref not satisfied locally is
// flagged as unknown when there is no sync state (offline/untracked).
func TestWalkBrokenRefsDetectsUnknown(t *testing.T) {
	tmp := t.TempDir()
	skillsDir := filepath.Join(tmp, "skills")
	skillDir := filepath.Join(skillsDir, "my-skill")
	os.MkdirAll(skillDir, 0755)
	os.WriteFile(filepath.Join(skillDir, "SKILL.md"),
		[]byte("---\nname: my-skill\n---\nLoad /gone-skill.\n"), 0644)

	origAgents := agents
	agents = []agentDef{agentForDir(t, skillsDir)}
	defer func() { agents = origAgents }()

	issues, err := walkBrokenRefs()
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 1 {
		t.Fatalf("expected 1 issue, got %d: %v", len(issues), issues)
	}
	if issues[0].refSlug != "gone-skill" {
		t.Errorf("expected refSlug=gone-skill, got %q", issues[0].refSlug)
	}
	if issues[0].status != "unknown" {
		t.Errorf("expected status=unknown, got %q", issues[0].status)
	}
}

func TestRenderSyncStateReportSummarisesSynced(t *testing.T) {
	var buf strings.Builder
	renderSyncStateReport(&buf, []SkillStateInfo{
		{Name: "a", State: StateTracked},
		{Name: "b", State: StateTracked},
	})
	out := buf.String()
	if !strings.Contains(out, "2 synced") {
		t.Errorf("expected '2 synced' summary, got: %q", out)
	}
}

func TestRenderSyncStateReportSurfacesNotableStates(t *testing.T) {
	var buf strings.Builder
	renderSyncStateReport(&buf, []SkillStateInfo{
		{Name: "owned-edited", State: StateTracked, LocalDirty: true},
		{
			Name:          "sourced-pending",
			State:         StateTracked,
			Sourced:       true,
			UpstreamMoved: true,
			Marker: &SyncEntry{
				Version: "1.2.0",
				Source:  &skillSource{Owner: "chrismdp", Slug: "sourced-pending"},
			},
			Remote: &apiSkill{Version: "1.3.0"},
		},
		{Name: "drive-by", State: StateUntracked},
		{
			Name:   "matches-server",
			State:  StateAdoptable,
			Remote: &apiSkill{Version: "1.0.8"},
		},
		{
			Name:   "name-collides",
			State:  StateConflict,
			Remote: &apiSkill{Version: "2.1.0"},
		},
		{Name: "elsewhere", State: StateAvailable},
	})
	out := buf.String()
	for _, want := range []string{
		"owned-edited", "local has unpublished changes",
		"sourced-pending", "customised copy of chrismdp/sourced-pending",
		"Original moved 1.2.0 → 1.3.0", "airskills resolve sourced-pending",
		"drive-by", "local exists, not tracked",
		"matches-server", "Original v1.0.8 matches bytes",
		"name-collides", "Original v2.1.0 differs",
		"elsewhere", "on server, not installed here",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("expected output to mention %q, got:\n%s", want, out)
		}
	}
	// Uniform `!` prefix — every notable line should start with `!`.
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "Sync state:" || strings.HasPrefix(trimmed, "✓") {
			continue
		}
		if !strings.HasPrefix(trimmed, "!") {
			t.Errorf("expected line to start with `!`, got: %q", trimmed)
		}
	}
}

func TestGatherSyncStateIgnoresStaleRememberedSkillset(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/skills" {
			http.NotFound(w, r)
			return
		}
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"skillset":{"slug":"poppins","name":"Poppins"},"skills":[]}`))
	}))
	defer srv.Close()

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	cfgDir := filepath.Join(home, ".config", "airskills")
	if err := os.MkdirAll(cfgDir, 0700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	cfgData, _ := json.Marshal(config.Config{APIURL: srv.URL, Skillset: "poppins"})
	if err := os.WriteFile(filepath.Join(cfgDir, "config.json"), cfgData, 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	tokenData, _ := json.Marshal(config.TokenData{
		AccessToken:  "x",
		RefreshToken: "y",
		ExpiresAt:    time.Now().Add(time.Hour).Unix(),
	})
	if err := os.WriteFile(filepath.Join(cfgDir, "token.json"), tokenData, 0600); err != nil {
		t.Fatalf("write token: %v", err)
	}

	if _, err := gatherSyncState(); err != nil {
		t.Fatalf("gatherSyncState: %v", err)
	}
	if gotQuery != "" {
		t.Fatalf("gatherSyncState sent stale remembered skillset; gotQuery = %q, want default query", gotQuery)
	}
}

func TestRenderSyncStateReportEmpty(t *testing.T) {
	var buf strings.Builder
	renderSyncStateReport(&buf, nil)
	out := buf.String()
	if !strings.Contains(out, "no skills tracked") {
		t.Errorf("expected empty-state message, got: %q", out)
	}
}
