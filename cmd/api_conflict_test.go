package cmd

import (
	"errors"
	"strings"
	"testing"
)

func TestSlugifyMatchesPlatform(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Retro", "retro"},
		{"My Skill Name", "my-skill-name"},
		{"foo--bar", "foo-bar"},
		{"  weird _ chars !!", "weird-chars"},
		{"trailing-", "trailing"},
		{"  ", ""},
	}
	for _, c := range cases {
		if got := slugify(c.in); got != c.want {
			t.Errorf("slugify(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestParseSkillConflict_RecognisesMig047Payload(t *testing.T) {
	raw := errors.New(`API error (409): {"error":"conflict","message":"slug \"retro\" already exists in your effective skill set","conflict_with":{"source":"org","owner_or_org_slug":"parsons-home","slug":"retro"}}`)
	c := parseSkillConflict(raw)
	if c == nil {
		t.Fatal("expected non-nil conflict")
	}
	if c.Source != "org" || c.OwnerOrOrgSlug != "parsons-home" || c.Slug != "retro" {
		t.Errorf("unexpected conflict: %+v", c)
	}
	msg := c.Error()
	if !strings.Contains(msg, "parsons-home") || !strings.Contains(msg, "airskills mv retro") {
		t.Errorf("error message lacks actionable hint: %q", msg)
	}
}

func TestParseSkillConflict_IgnoresNon409(t *testing.T) {
	if c := parseSkillConflict(errors.New("API error (500): boom")); c != nil {
		t.Errorf("expected nil for 500, got %+v", c)
	}
	if c := parseSkillConflict(errors.New("API error (409): plain text body")); c != nil {
		t.Errorf("expected nil for 409 with no JSON conflict_with payload, got %+v", c)
	}
	if c := parseSkillConflict(nil); c != nil {
		t.Errorf("expected nil for nil error, got %+v", c)
	}
}

func TestSkillConflictError_FallbackMessage(t *testing.T) {
	e := &SkillConflictError{Slug: "foo", Source: "user"}
	if !strings.Contains(e.Error(), "foo") {
		t.Errorf("expected fallback message to name slug: %q", e.Error())
	}
}
