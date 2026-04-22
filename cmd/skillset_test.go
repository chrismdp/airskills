package cmd

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chrismdp/airskills/config"
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

func TestResolveSkillsetFlag_FirstRunWithFlagRemembers(t *testing.T) {
	withTempHome(t)
	cfg := &config.Config{APIURL: "http://x"}

	slug, err := resolveSkillsetFlag(cfg, "writing", strings.NewReader(""), &bytes.Buffer{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if slug != "writing" {
		t.Errorf("expected slug=writing, got %q", slug)
	}
	if cfg.Skillset != "writing" {
		t.Errorf("cfg not updated: %q", cfg.Skillset)
	}
	if got := readStoredSkillset(t); got != "writing" {
		t.Errorf("on-disk config not updated: %q", got)
	}
}

func TestResolveSkillsetFlag_NoFlagUsesRemembered(t *testing.T) {
	withTempHome(t)
	cfg := &config.Config{APIURL: "http://x", Skillset: "work"}

	slug, err := resolveSkillsetFlag(cfg, "", strings.NewReader(""), &bytes.Buffer{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if slug != "work" {
		t.Errorf("expected slug=work, got %q", slug)
	}
}

func TestResolveSkillsetFlag_MatchesRememberedNoPrompt(t *testing.T) {
	withTempHome(t)
	cfg := &config.Config{APIURL: "http://x", Skillset: "work"}
	var writer bytes.Buffer

	slug, err := resolveSkillsetFlag(cfg, "work", strings.NewReader(""), &writer)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if slug != "work" {
		t.Errorf("expected slug=work, got %q", slug)
	}
	if writer.Len() != 0 {
		t.Errorf("should not have prompted: %q", writer.String())
	}
}

func TestResolveSkillsetFlag_SwitchConfirmed(t *testing.T) {
	withTempHome(t)
	cfg := &config.Config{APIURL: "http://x", Skillset: "work"}
	var writer bytes.Buffer

	slug, err := resolveSkillsetFlag(cfg, "personal", strings.NewReader("y\n"), &writer)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if slug != "personal" {
		t.Errorf("expected slug=personal, got %q", slug)
	}
	if !strings.Contains(writer.String(), `"work" to "personal"`) {
		t.Errorf("expected switch prompt, got: %q", writer.String())
	}
	if readStoredSkillset(t) != "personal" {
		t.Errorf("on-disk not updated")
	}
}

func TestResolveSkillsetFlag_SwitchCancelled(t *testing.T) {
	withTempHome(t)
	cfg := &config.Config{APIURL: "http://x", Skillset: "work"}
	var writer bytes.Buffer

	_, err := resolveSkillsetFlag(cfg, "personal", strings.NewReader("N\n"), &writer)
	if !errors.Is(err, ErrSkillsetSwitchCancelled) {
		t.Fatalf("expected cancel error, got: %v", err)
	}
	if cfg.Skillset != "work" {
		t.Errorf("cfg mutated on cancel: %q", cfg.Skillset)
	}
}

func TestResolveSkillsetFlag_SwitchEOFCancels(t *testing.T) {
	withTempHome(t)
	cfg := &config.Config{APIURL: "http://x", Skillset: "work"}

	_, err := resolveSkillsetFlag(cfg, "personal", strings.NewReader(""), &bytes.Buffer{})
	if !errors.Is(err, ErrSkillsetSwitchCancelled) {
		t.Fatalf("expected cancel error on EOF, got: %v", err)
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
	skillsets := []apiSkillset{
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
	skillsets := []apiSkillset{
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
