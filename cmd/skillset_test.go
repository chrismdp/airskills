package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chrismdp/airskills/config"
	"github.com/chrismdp/airskills/internal/apitypes"
)

// withTempHome points config.Dir() at a fresh temp directory for the
// duration of t so resolveSkillsetFlag's cfg.Save() writes to a sandbox
// instead of the user's real ~/.config.
func withTempHome(t *testing.T) {
	t.Helper()
	orig := os.Getenv("HOME")
	tmp := t.TempDir()
	os.Setenv("HOME", tmp)
	t.Cleanup(func() { os.Setenv("HOME", orig) })
}

func readStoredSkillset(t *testing.T) string {
	t.Helper()
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	return cfg.Skillset
}

func TestResolveSkillsetFlag_FirstRunNoFlag(t *testing.T) {
	withTempHome(t)
	cfg := &config.Config{APIURL: "http://x"}

	slug, err := resolveSkillsetFlag(cfg, "", strings.NewReader(""), &bytes.Buffer{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if slug != "" {
		t.Errorf("expected empty slug (server picks default), got %q", slug)
	}
}

// Mig 047 collapsed user-side skillsets to a single implicit 'default'.
// resolveSkillsetFlag now returns "" unconditionally (the server's
// /api/v1/skills GET coerces any passed slug to the user's default).
// It warns and clears the stale cfg.Skillset if one was remembered.

func TestResolveSkillsetFlag_NonDefaultFlagWarnsAndIgnored(t *testing.T) {
	withTempHome(t)
	cfg := &config.Config{APIURL: "http://x"}
	var writer bytes.Buffer

	slug, err := resolveSkillsetFlag(cfg, "writing", strings.NewReader(""), &writer)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if slug != "" {
		t.Errorf("expected slug to be ignored (empty), got %q", slug)
	}
	if !strings.Contains(writer.String(), `--skillset is no longer used`) {
		t.Errorf("expected deprecation notice, got: %q", writer.String())
	}
}

func TestResolveSkillsetFlag_StaleConfigGetsCleared(t *testing.T) {
	withTempHome(t)
	cfg := &config.Config{APIURL: "http://x", Skillset: "work"}

	if _, err := resolveSkillsetFlag(cfg, "", strings.NewReader(""), &bytes.Buffer{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Skillset != "" {
		t.Errorf("stale cfg.Skillset should have been cleared, got %q", cfg.Skillset)
	}
	if readStoredSkillset(t) != "" {
		t.Errorf("on-disk cfg.Skillset should have been cleared")
	}
}

func TestResolveSkillsetFlag_DefaultFlagSilent(t *testing.T) {
	withTempHome(t)
	cfg := &config.Config{APIURL: "http://x"}
	var writer bytes.Buffer

	if _, err := resolveSkillsetFlag(cfg, "default", strings.NewReader(""), &writer); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if writer.Len() != 0 {
		t.Errorf("passing 'default' should be silent (it's the canonical value), got: %q", writer.String())
	}
}

func TestRememberSkillsetAfterSuccess_NoOpWhenAlreadyRemembered(t *testing.T) {
	withTempHome(t)
	cfg := &config.Config{APIURL: "http://x", Skillset: "writing"}
	rememberSkillsetAfterSuccess(cfg, "default")
	if cfg.Skillset != "writing" {
		t.Errorf("should not overwrite existing remembered slug")
	}
}

func TestRememberSkillsetAfterSuccess_StoresResolvedOnFirstRun(t *testing.T) {
	withTempHome(t)
	cfg := &config.Config{APIURL: "http://x"}
	rememberSkillsetAfterSuccess(cfg, "default")
	if cfg.Skillset != "default" {
		t.Errorf("expected cfg.Skillset=default, got %q", cfg.Skillset)
	}
	if readStoredSkillset(t) != "default" {
		t.Errorf("on-disk not updated")
	}
}

func TestSkillsetNotFoundError_Formats(t *testing.T) {
	err := &SkillsetNotFoundError{RequestedSlug: "nope", Available: []string{"default", "work"}}
	if !strings.Contains(err.Error(), `"nope"`) || !strings.Contains(err.Error(), "default, work") {
		t.Errorf("unexpected error text: %s", err.Error())
	}
	err = &SkillsetNotFoundError{RequestedSlug: "nope", Available: nil}
	if !strings.Contains(err.Error(), "no personal skillsets") {
		t.Errorf("empty-available path wrong: %s", err.Error())
	}
}

// Sanity check that the Skillset field round-trips through JSON.
func TestConfigSkillsetRoundtrip(t *testing.T) {
	withTempHome(t)
	cfg := &config.Config{APIURL: "http://x", Skillset: "work"}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	dir, _ := config.Dir()
	raw, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"skillset": "work"`) {
		t.Errorf("on-disk missing skillset field: %s", string(raw))
	}
}

// --- CRUD command tests -----------------------------------------------------

func TestValidSkillsetSlug_Accepts(t *testing.T) {
	for _, ok := range []string{"default", "work", "a1", "my-work-skills", "abc-123"} {
		if err := validSkillsetSlug(ok); err != nil {
			t.Errorf("expected %q to be valid, got %v", ok, err)
		}
	}
}

func TestValidSkillsetSlug_Rejects(t *testing.T) {
	cases := []struct {
		slug string
		why  string
	}{
		{"", "empty"},
		{"UPPER", "uppercase"},
		{"-leading", "leading dash"},
		{"trailing-", "trailing dash"},
		{"con--secutive", "consecutive dashes"},
		{"under_score", "underscore"},
		{"has space", "space"},
		{strings.Repeat("a", 65), "too long"},
	}
	for _, c := range cases {
		if err := validSkillsetSlug(c.slug); err == nil {
			t.Errorf("expected %q (%s) to be rejected", c.slug, c.why)
		}
	}
}

func TestRenderSkillsetList_MarksSelected(t *testing.T) {
	skillsets := []apitypes.SkillsetListItem{
		{Slug: "default", SkillCount: 12, IsDefault: true},
		{Slug: "writing", SkillCount: 3},
		{Slug: "minimal", SkillCount: 5},
	}
	var out bytes.Buffer
	renderSkillsetList(&out, skillsets, "writing")
	s := out.String()
	// writing should be asterisked, the others bare.
	if !strings.Contains(s, "* writing (3 skills)") {
		t.Errorf("selected row missing '*' prefix, got:\n%s", s)
	}
	if !strings.Contains(s, "  default (12 skills)") {
		t.Errorf("non-selected row should have leading space, got:\n%s", s)
	}
	if strings.Count(s, "*") != 1 {
		t.Errorf("expected exactly one '*' marker, got:\n%s", s)
	}
}

func TestRenderSkillsetList_FallsBackToIsDefault(t *testing.T) {
	// When no local preference is set, the is_default row should still
	// show an asterisk so the user knows what they're on.
	skillsets := []apitypes.SkillsetListItem{
		{Slug: "default", SkillCount: 1, IsDefault: true},
		{Slug: "other", SkillCount: 0},
	}
	var out bytes.Buffer
	renderSkillsetList(&out, skillsets, "")
	s := out.String()
	if !strings.Contains(s, "* default (1 skills)") {
		t.Errorf("is_default row should be marked when nothing remembered, got:\n%s", s)
	}
}

func TestRenderSkillsetList_Empty(t *testing.T) {
	var out bytes.Buffer
	renderSkillsetList(&out, nil, "")
	if !strings.Contains(out.String(), "No skillsets") {
		t.Errorf("expected empty message, got %q", out.String())
	}
}
