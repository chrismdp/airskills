package cmd

import (
	"reflect"
	"strings"
	"testing"
)

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
