package cmd

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
)

func TestRenderOrgListEmpty(t *testing.T) {
	var buf bytes.Buffer
	renderOrgList(&buf, nil)
	got := buf.String()
	if !strings.Contains(got, "no organizations") {
		t.Errorf("empty render should mention 'no organizations', got: %q", got)
	}
	if !strings.Contains(got, "dashboard/organizations/new") {
		t.Errorf("empty render should point to the dashboard create URL, got: %q", got)
	}
}

func TestRenderOrgListSortsBySlug(t *testing.T) {
	orgs := []apiOrg{
		{Slug: "zeta", Name: "Zeta Corp", Role: "member", MemberCount: 3},
		{Slug: "alpha", Name: "Alpha Co", Role: "admin", MemberCount: 7},
		{Slug: "mu", Name: "Mu Ltd", Role: "member", MemberCount: 1},
	}
	var buf bytes.Buffer
	renderOrgList(&buf, orgs)
	out := buf.String()

	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) < 4 {
		t.Fatalf("expected header + 3 rows, got %d lines: %q", len(lines), out)
	}
	if !strings.HasPrefix(lines[0], "SLUG") {
		t.Errorf("first line should be the header, got: %q", lines[0])
	}
	idxAlpha := strings.Index(out, "alpha")
	idxMu := strings.Index(out, "mu")
	idxZeta := strings.Index(out, "zeta")
	if !(idxAlpha < idxMu && idxMu < idxZeta) {
		t.Errorf("rows should be alphabetical by slug, got order: alpha=%d mu=%d zeta=%d\n%s",
			idxAlpha, idxMu, idxZeta, out)
	}
	if !strings.Contains(out, "admin") || !strings.Contains(out, "Alpha Co") {
		t.Errorf("output should include role + name fields, got: %q", out)
	}
}

func TestRenderOrgListMissingFieldsFallBackToDash(t *testing.T) {
	orgs := []apiOrg{{Slug: "bare", MemberCount: 1}}
	var buf bytes.Buffer
	renderOrgList(&buf, orgs)
	out := buf.String()
	// Both role and name fall back to "—" when the server response
	// doesn't carry them. The literal em-dash character must appear in
	// the row, not elsewhere.
	rows := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(rows) < 2 {
		t.Fatalf("expected header + 1 row, got: %q", out)
	}
	if !strings.Contains(rows[1], "—") {
		t.Errorf("missing-field row should render em-dash placeholders, got: %q", rows[1])
	}
}

func TestSplitCSV(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"a", []string{"a"}},
		{"a,b,c", []string{"a", "b", "c"}},
		{" a , b , c ", []string{"a", "b", "c"}},
		{"a,,b", []string{"a", "b"}},
		{",,,", []string{}},
	}
	for _, c := range cases {
		got := splitCSV(c.in)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("splitCSV(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestSlugsToIDsResolvesKnown(t *testing.T) {
	skillsets := []apiMemberSkillset{
		{ID: "id-a", Slug: "alpha"},
		{ID: "id-b", Slug: "beta"},
		{ID: "id-c", Slug: "gamma"},
	}
	got, err := slugsToIDs(skillsets, []string{"alpha", "gamma"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"id-a", "id-c"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestSlugsToIDsErrorsOnUnknown(t *testing.T) {
	skillsets := []apiMemberSkillset{{ID: "id-a", Slug: "alpha"}}
	_, err := slugsToIDs(skillsets, []string{"alpha", "missing"})
	if err == nil {
		t.Fatal("expected error on unknown slug, got nil")
	}
	if !strings.Contains(err.Error(), "missing") {
		t.Errorf("error should mention the missing slug, got: %v", err)
	}
}

func TestAppendUniqueNoDupe(t *testing.T) {
	got := appendUnique([]string{"a", "b"}, "a")
	want := []string{"a", "b"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestAppendUniqueAppends(t *testing.T) {
	got := appendUnique([]string{"a", "b"}, "c")
	want := []string{"a", "b", "c"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestRemoveIDRemovesAll(t *testing.T) {
	got := removeID([]string{"a", "b", "a"}, "a")
	want := []string{"b"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestRemoveIDNoMatch(t *testing.T) {
	got := removeID([]string{"a", "b"}, "c")
	want := []string{"a", "b"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}
