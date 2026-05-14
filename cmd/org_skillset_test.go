package cmd

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNormalizeOrgSkillSlugStripsMatchingOrgPrefix(t *testing.T) {
	gotOwner, gotSlug := normalizeOrgSkillRef("acme", "acme-slides")
	if gotOwner != "acme" || gotSlug != "slides" {
		t.Fatalf("got owner=%q slug=%q, want acme/slides", gotOwner, gotSlug)
	}
}

func TestNormalizeOrgSkillSlugKeepsBareSlug(t *testing.T) {
	gotOwner, gotSlug := normalizeOrgSkillRef("acme", "slides")
	if gotOwner != "acme" || gotSlug != "slides" {
		t.Fatalf("got owner=%q slug=%q, want acme/slides", gotOwner, gotSlug)
	}
}

func TestNormalizeOrgSkillSlugKeepsQualifiedOtherOwner(t *testing.T) {
	gotOwner, gotSlug := normalizeOrgSkillRef("acme", "other/slides")
	if gotOwner != "other" || gotSlug != "slides" {
		t.Fatalf("got owner=%q slug=%q, want other/slides", gotOwner, gotSlug)
	}
}

func TestRenderOrgSkillsetList(t *testing.T) {
	var buf bytes.Buffer
	renderOrgSkillsetList(&buf, []apiOrgSkillset{
		{Slug: "default", Name: "Default", SkillCount: 2, IsDefault: true},
		{Slug: "core", Name: "Core", SkillCount: 1},
	})
	out := buf.String()
	if !strings.Contains(out, "default") || !strings.Contains(out, "core") {
		t.Fatalf("expected both skillsets in output, got %q", out)
	}
	if !strings.Contains(out, "(default)") {
		t.Fatalf("expected default marker in output, got %q", out)
	}
}

func TestOrgSkillsetAPIHelpersUseOwnerScopedPaths(t *testing.T) {
	var seen []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.URL.String())
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/skillsets/acme":
			w.Write([]byte(`[{"slug":"core","name":"Core","skill_count":0,"is_default":false}]`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/skillsets/acme":
			var payload map[string]string
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("invalid JSON payload: %v", err)
			}
			if payload["slug"] != "core" || payload["name"] != "Core" {
				t.Fatalf("unexpected create payload: %#v", payload)
			}
			w.Write([]byte(`{"id":"00000000-0000-0000-0000-000000000001","slug":"core","name":"Core","description":"","is_default":false,"auto_absorb_new_skills":true,"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}`))
		case r.Method == http.MethodPut && r.URL.Path == "/api/v1/skillsets/acme/core/skills/acme/slides":
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodDelete && r.URL.Path == "/api/v1/skillsets/acme/core/skills/acme/slides":
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodDelete && r.URL.Path == "/api/v1/skillsets/acme/core":
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()

	client := &apiClient{baseURL: server.URL, token: "tok", http: server.Client()}
	if _, err := client.listOrgSkillsets("acme"); err != nil {
		t.Fatalf("listOrgSkillsets: %v", err)
	}
	if _, err := client.createOrgSkillset("acme", "core", "Core", ""); err != nil {
		t.Fatalf("createOrgSkillset: %v", err)
	}
	if err := client.addSkillToOrgSkillset("acme", "core", "acme", "slides"); err != nil {
		t.Fatalf("addSkillToOrgSkillset: %v", err)
	}
	if err := client.removeSkillFromOrgSkillset("acme", "core", "acme", "slides"); err != nil {
		t.Fatalf("removeSkillFromOrgSkillset: %v", err)
	}
	if err := client.deleteOrgSkillset("acme", "core"); err != nil {
		t.Fatalf("deleteOrgSkillset: %v", err)
	}

	want := []string{
		"GET /api/v1/skillsets/acme",
		"POST /api/v1/skillsets/acme",
		"PUT /api/v1/skillsets/acme/core/skills/acme/slides",
		"DELETE /api/v1/skillsets/acme/core/skills/acme/slides",
		"DELETE /api/v1/skillsets/acme/core",
	}
	if strings.Join(seen, "\n") != strings.Join(want, "\n") {
		t.Fatalf("requests:\n%s\nwant:\n%s", strings.Join(seen, "\n"), strings.Join(want, "\n"))
	}
}
